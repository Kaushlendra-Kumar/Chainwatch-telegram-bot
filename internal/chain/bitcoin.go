package chain

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"chainwatch/internal/httpx"
)

var (
	btcBase58Re = regexp.MustCompile(`^[13][a-km-zA-HJ-NP-Z1-9]{25,34}$`)
	btcBech32Re = regexp.MustCompile(`^bc1[02-9ac-hj-np-z]{11,87}$`)
)

const btcMaxPages = 3

type esploraTx struct {
	TxID string `json:"txid"`
	Vin  []struct {
		Prevout *struct {
			Address string `json:"scriptpubkey_address"`
			Value   int64  `json:"value"`
		} `json:"prevout"`
	} `json:"vin"`
	Vout []struct {
		Address string `json:"scriptpubkey_address"`
		Value   int64  `json:"value"`
	} `json:"vout"`
	Status struct {
		Confirmed   bool  `json:"confirmed"`
		BlockHeight int64 `json:"block_height"`
		BlockTime   int64 `json:"block_time"`
	} `json:"status"`
}

type Bitcoin struct {
	base     string
	http     *httpx.Client
	explorer string
}

func NewBitcoin(base string) *Bitcoin {
	return &Bitcoin{
		base:     strings.TrimRight(base, "/"),
		http:     httpx.New(20*time.Second, httpx.NewLimiter(3), nil),
		explorer: "https://mempool.space",
	}
}

func (b *Bitcoin) ID() string           { return "btc" }
func (b *Bitcoin) Name() string         { return "Bitcoin" }
func (b *Bitcoin) NativeSymbol() string { return "BTC" }

func (b *Bitcoin) TxURL(hash string) string {
	return b.explorer + "/tx/" + hash
}

func (b *Bitcoin) AddressURL(address string) string {
	return b.explorer + "/address/" + address
}

func (b *Bitcoin) Normalize(address string) (string, error) {
	a := strings.TrimSpace(address)
	if lower := strings.ToLower(a); strings.HasPrefix(lower, "bc1") {
		if btcBech32Re.MatchString(lower) {
			return lower, nil
		}
		return "", ErrInvalidAddress
	}
	if btcBase58Re.MatchString(a) {
		return a, nil
	}
	return "", ErrInvalidAddress
}

func (b *Bitcoin) Bootstrap(ctx context.Context, address string) (string, error) {
	if _, err := b.Balance(ctx, address); err != nil {
		return "", err
	}
	tip, err := b.http.GetText(ctx, b.base+"/blocks/tip/height")
	if err != nil {
		return "", fmt.Errorf("bitcoin tip: %w", err)
	}
	if _, err := strconv.ParseInt(tip, 10, 64); err != nil {
		return "", fmt.Errorf("bitcoin tip %q: %w", tip, err)
	}
	return tip, nil
}

func (b *Bitcoin) Poll(ctx context.Context, address, cursor string) ([]Event, string, error) {
	last, _ := strconv.ParseInt(cursor, 10, 64)
	var all []esploraTx
	u := fmt.Sprintf("%s/address/%s/txs", b.base, address)
	for page := 0; page < btcMaxPages; page++ {
		var txs []esploraTx
		if err := b.http.GetJSON(ctx, u, &txs); err != nil {
			return nil, cursor, fmt.Errorf("bitcoin txs: %w", err)
		}
		all = append(all, txs...)
		var confirmed []esploraTx
		for _, tx := range txs {
			if tx.Status.Confirmed {
				confirmed = append(confirmed, tx)
			}
		}
		if len(confirmed) < 25 || confirmed[len(confirmed)-1].Status.BlockHeight <= last {
			break
		}
		u = fmt.Sprintf("%s/address/%s/txs/chain/%s", b.base, address, confirmed[len(confirmed)-1].TxID)
	}
	events, maxHeight := buildBitcoinEvents(address, all, last)
	return events, strconv.FormatInt(max(last, maxHeight), 10), nil
}

func (b *Bitcoin) Balance(ctx context.Context, address string) (Balance, error) {
	var res struct {
		Chain struct {
			Funded int64 `json:"funded_txo_sum"`
			Spent  int64 `json:"spent_txo_sum"`
		} `json:"chain_stats"`
		Mempool struct {
			Funded int64 `json:"funded_txo_sum"`
			Spent  int64 `json:"spent_txo_sum"`
		} `json:"mempool_stats"`
	}
	if err := b.http.GetJSON(ctx, b.base+"/address/"+address, &res); err != nil {
		if httpx.IsStatus(err, 400) {
			return Balance{}, ErrInvalidAddress
		}
		return Balance{}, fmt.Errorf("bitcoin balance: %w", err)
	}
	sats := res.Chain.Funded - res.Chain.Spent + res.Mempool.Funded - res.Mempool.Spent
	return Balance{Amount: float64(sats) / 1e8, Symbol: "BTC"}, nil
}

func buildBitcoinEvents(owner string, txs []esploraTx, after int64) ([]Event, int64) {
	var events []Event
	var maxHeight int64
	seen := map[string]bool{}
	for i := len(txs) - 1; i >= 0; i-- {
		tx := txs[i]
		if !tx.Status.Confirmed || seen[tx.TxID] {
			continue
		}
		seen[tx.TxID] = true
		if tx.Status.BlockHeight > maxHeight {
			maxHeight = tx.Status.BlockHeight
		}
		if tx.Status.BlockHeight <= after {
			continue
		}
		var received, sent int64
		var firstSender, firstReceiver string
		for _, in := range tx.Vin {
			if in.Prevout == nil {
				continue
			}
			if in.Prevout.Address == owner {
				sent += in.Prevout.Value
			} else if firstSender == "" {
				firstSender = in.Prevout.Address
			}
		}
		for _, out := range tx.Vout {
			if out.Address == owner {
				received += out.Value
			} else if firstReceiver == "" && out.Address != "" {
				firstReceiver = out.Address
			}
		}
		net := received - sent
		if net == 0 {
			continue
		}
		ev := Event{
			TxHash:   tx.TxID,
			Key:      "native",
			Symbol:   "BTC",
			Verified: true,
			Block:    tx.Status.BlockHeight,
			Time:     time.Unix(tx.Status.BlockTime, 0).UTC(),
		}
		if net > 0 {
			ev.Direction, ev.Amount, ev.Counterparty = In, float64(net)/1e8, firstSender
		} else {
			ev.Direction, ev.Amount, ev.Counterparty = Out, float64(-net)/1e8, firstReceiver
		}
		events = append(events, ev)
	}
	return events, maxHeight
}
