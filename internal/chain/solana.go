package chain

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"

	"chainwatch/internal/httpx"
)

const (
	solPageSize = 25
	solMaxPages = 3
	solMaxTxs   = 15
	base58Chars = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
)

var solAddrRe = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{32,44}$`)

type solSignature struct {
	Signature string          `json:"signature"`
	Err       json.RawMessage `json:"err"`
	BlockTime *int64          `json:"blockTime"`
}

type solTokenBalance struct {
	AccountIndex  int    `json:"accountIndex"`
	Mint          string `json:"mint"`
	Owner         string `json:"owner"`
	UITokenAmount struct {
		Amount   string `json:"amount"`
		Decimals int    `json:"decimals"`
	} `json:"uiTokenAmount"`
}

type solTx struct {
	Slot      int64  `json:"slot"`
	BlockTime *int64 `json:"blockTime"`
	Meta      *struct {
		Err               json.RawMessage   `json:"err"`
		Fee               int64             `json:"fee"`
		PreBalances       []int64           `json:"preBalances"`
		PostBalances      []int64           `json:"postBalances"`
		PreTokenBalances  []solTokenBalance `json:"preTokenBalances"`
		PostTokenBalances []solTokenBalance `json:"postTokenBalances"`
	} `json:"meta"`
	Transaction struct {
		Message struct {
			AccountKeys []struct {
				Pubkey string `json:"pubkey"`
			} `json:"accountKeys"`
		} `json:"message"`
	} `json:"transaction"`
}

type Solana struct {
	rpc  string
	http *httpx.Client
}

func NewSolana(rpcURL string, rps float64) *Solana {
	return &Solana{rpc: rpcURL, http: httpx.New(20*time.Second, httpx.NewLimiter(rps), nil)}
}

func (s *Solana) ID() string           { return "sol" }
func (s *Solana) Name() string         { return "Solana" }
func (s *Solana) NativeSymbol() string { return "SOL" }

func (s *Solana) TxURL(hash string) string {
	return "https://solscan.io/tx/" + hash
}

func (s *Solana) AddressURL(address string) string {
	return "https://solscan.io/account/" + address
}

func (s *Solana) Normalize(address string) (string, error) {
	a := strings.TrimSpace(address)
	if !solAddrRe.MatchString(a) || decodedBase58Len(a) != 32 {
		return "", ErrInvalidAddress
	}
	return a, nil
}

func (s *Solana) call(ctx context.Context, method string, params []any, out any) error {
	req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := s.http.PostJSON(ctx, s.rpc, req, &resp); err != nil {
		return fmt.Errorf("solana %s: %w", method, err)
	}
	if resp.Error != nil {
		return fmt.Errorf("solana %s: %d %s", method, resp.Error.Code, resp.Error.Message)
	}
	if out == nil || len(resp.Result) == 0 || string(resp.Result) == "null" {
		return nil
	}
	return json.Unmarshal(resp.Result, out)
}

func (s *Solana) signatures(ctx context.Context, address, until, before string) ([]solSignature, error) {
	opts := map[string]any{"limit": solPageSize, "commitment": "confirmed"}
	if until != "" {
		opts["until"] = until
	}
	if before != "" {
		opts["before"] = before
	}
	var sigs []solSignature
	err := s.call(ctx, "getSignaturesForAddress", []any{address, opts}, &sigs)
	return sigs, err
}

func (s *Solana) Bootstrap(ctx context.Context, address string) (string, error) {
	var sigs []solSignature
	opts := map[string]any{"limit": 1, "commitment": "confirmed"}
	if err := s.call(ctx, "getSignaturesForAddress", []any{address, opts}, &sigs); err != nil {
		return "", err
	}
	if len(sigs) == 0 {
		return "", nil
	}
	return sigs[0].Signature, nil
}

func (s *Solana) Poll(ctx context.Context, address, cursor string) ([]Event, string, error) {
	var sigs []solSignature
	before := ""
	for page := 0; page < solMaxPages; page++ {
		batch, err := s.signatures(ctx, address, cursor, before)
		if err != nil {
			return nil, cursor, err
		}
		sigs = append(sigs, batch...)
		if len(batch) < solPageSize || cursor == "" {
			break
		}
		before = batch[len(batch)-1].Signature
	}
	if len(sigs) == 0 {
		return nil, cursor, nil
	}
	newCursor := sigs[0].Signature

	var ok []solSignature
	for _, sig := range sigs {
		if isNullJSON(sig.Err) {
			ok = append(ok, sig)
		}
		if len(ok) == solMaxTxs {
			break
		}
	}

	var events []Event
	for i := len(ok) - 1; i >= 0; i-- {
		var tx solTx
		opts := map[string]any{"encoding": "jsonParsed", "maxSupportedTransactionVersion": 0, "commitment": "confirmed"}
		if err := s.call(ctx, "getTransaction", []any{ok[i].Signature, opts}, &tx); err != nil {
			return nil, cursor, err
		}
		events = append(events, buildSolanaEvents(address, ok[i].Signature, tx)...)
	}
	return events, newCursor, nil
}

func (s *Solana) Balance(ctx context.Context, address string) (Balance, error) {
	var res struct {
		Value int64 `json:"value"`
	}
	if err := s.call(ctx, "getBalance", []any{address, map[string]any{"commitment": "confirmed"}}, &res); err != nil {
		return Balance{}, err
	}
	return Balance{Amount: float64(res.Value) / 1e9, Symbol: "SOL"}, nil
}

func buildSolanaEvents(owner, signature string, tx solTx) []Event {
	if tx.Meta == nil || !isNullJSON(tx.Meta.Err) {
		return nil
	}
	at := time.Time{}
	if tx.BlockTime != nil {
		at = time.Unix(*tx.BlockTime, 0).UTC()
	}
	keys := tx.Transaction.Message.AccountKeys
	var events []Event

	idx := -1
	for i, k := range keys {
		if k.Pubkey == owner {
			idx = i
			break
		}
	}
	m := tx.Meta
	if idx >= 0 && idx < len(m.PreBalances) && idx < len(m.PostBalances) {
		delta := m.PostBalances[idx] - m.PreBalances[idx]
		if idx == 0 {
			delta += m.Fee
		}
		if delta != 0 {
			ev := Event{
				TxHash:   signature,
				Key:      "native",
				Symbol:   "SOL",
				Verified: true,
				Block:    tx.Slot,
				Time:     at,
				Amount:   float64(abs64(delta)) / 1e9,
			}
			ev.Direction = In
			if delta < 0 {
				ev.Direction = Out
			}
			ev.Counterparty = solCounterparty(keys, m.PreBalances, m.PostBalances, idx, delta)
			events = append(events, ev)
		}
	}

	type tokenDelta struct {
		pre, post *big.Int
		decimals  int
	}
	deltas := map[string]*tokenDelta{}
	add := func(bals []solTokenBalance, post bool) {
		for _, b := range bals {
			if b.Owner != owner {
				continue
			}
			n, ok := new(big.Int).SetString(b.UITokenAmount.Amount, 10)
			if !ok {
				continue
			}
			d := deltas[b.Mint]
			if d == nil {
				d = &tokenDelta{pre: new(big.Int), post: new(big.Int), decimals: b.UITokenAmount.Decimals}
				deltas[b.Mint] = d
			}
			if post {
				d.post.Add(d.post, n)
			} else {
				d.pre.Add(d.pre, n)
			}
		}
	}
	add(m.PreTokenBalances, false)
	add(m.PostTokenBalances, true)

	mints := make([]string, 0, len(deltas))
	for mint := range deltas {
		mints = append(mints, mint)
	}
	sort.Strings(mints)
	for _, mint := range mints {
		d := deltas[mint]
		diff := new(big.Int).Sub(d.post, d.pre)
		if diff.Sign() == 0 {
			continue
		}
		dir := In
		if diff.Sign() < 0 {
			dir = Out
			diff.Neg(diff)
		}
		symbol, verified := solanaMints[mint]
		if !verified {
			symbol = mint[:4] + "…" + mint[len(mint)-4:]
		}
		events = append(events, Event{
			TxHash:    signature,
			Key:       "spl:" + mint,
			Direction: dir,
			Amount:    BigToFloat(diff, d.decimals),
			Symbol:    symbol,
			Token:     mint,
			Verified:  verified,
			Block:     tx.Slot,
			Time:      at,
		})
	}
	return events
}

func solCounterparty(keys []struct {
	Pubkey string `json:"pubkey"`
}, pre, post []int64, self int, delta int64) string {
	best, bestAbs := "", int64(0)
	for i := range keys {
		if i == self || i >= len(pre) || i >= len(post) {
			continue
		}
		d := post[i] - pre[i]
		if (delta > 0 && d < 0) || (delta < 0 && d > 0) {
			if abs64(d) > bestAbs {
				best, bestAbs = keys[i].Pubkey, abs64(d)
			}
		}
	}
	return best
}

func isNullJSON(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null"
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func decodedBase58Len(s string) int {
	n := new(big.Int)
	radix := big.NewInt(58)
	for _, r := range s {
		i := strings.IndexRune(base58Chars, r)
		if i < 0 {
			return -1
		}
		n.Mul(n, radix)
		n.Add(n, big.NewInt(int64(i)))
	}
	leading := 0
	for _, r := range s {
		if r != '1' {
			break
		}
		leading++
	}
	return leading + len(n.Bytes())
}
