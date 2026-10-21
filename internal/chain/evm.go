package chain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"chainwatch/internal/httpx"
)

var ErrBudgetExhausted = errors.New("daily API budget exhausted")

type EVMNetwork struct {
	ID        string
	Name      string
	ChainID   int64
	Native    string
	Explorer  string
	LagBlocks int64
}

var EVMNetworks = map[string]EVMNetwork{
	"eth":      {ID: "eth", Name: "Ethereum", ChainID: 1, Native: "ETH", Explorer: "https://etherscan.io", LagBlocks: 3},
	"polygon":  {ID: "polygon", Name: "Polygon", ChainID: 137, Native: "POL", Explorer: "https://polygonscan.com", LagBlocks: 20},
	"arbitrum": {ID: "arbitrum", Name: "Arbitrum", ChainID: 42161, Native: "ETH", Explorer: "https://arbiscan.io", LagBlocks: 200},
	"linea":    {ID: "linea", Name: "Linea", ChainID: 59144, Native: "ETH", Explorer: "https://lineascan.build", LagBlocks: 20},
	"bsc":      {ID: "bsc", Name: "BNB Chain", ChainID: 56, Native: "BNB", Explorer: "https://bscscan.com", LagBlocks: 20},
	"base":     {ID: "base", Name: "Base", ChainID: 8453, Native: "ETH", Explorer: "https://basescan.org", LagBlocks: 30},
}

var evmAddrRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

type Etherscan struct {
	http   *httpx.Client
	base   string
	key    string
	budget *httpx.Budget
}

func NewEtherscan(key string, rps float64, dailyBudget int) *Etherscan {
	return &Etherscan{
		http:   httpx.New(20*time.Second, httpx.NewLimiter(rps), nil),
		base:   "https://api.etherscan.io/v2/api",
		key:    key,
		budget: httpx.NewBudget(dailyBudget),
	}
}

func (e *Etherscan) UsedToday() int {
	return e.budget.Used()
}

func (e *Etherscan) query(ctx context.Context, chainID int64, params url.Values) (json.RawMessage, error) {
	if !e.budget.Take() {
		return nil, ErrBudgetExhausted
	}
	params.Set("chainid", strconv.FormatInt(chainID, 10))
	params.Set("apikey", e.key)
	var env struct {
		Status  string          `json:"status"`
		Message string          `json:"message"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := e.http.GetJSON(ctx, e.base+"?"+params.Encode(), &env); err != nil {
		return nil, fmt.Errorf("etherscan: %w", err)
	}
	if env.Error != nil {
		return nil, fmt.Errorf("etherscan: %s", env.Error.Message)
	}
	if env.Status == "0" {
		msg := strings.ToLower(env.Message)
		if strings.Contains(msg, "no transactions found") || strings.Contains(msg, "no records found") {
			return json.RawMessage("[]"), nil
		}
		var detail string
		_ = json.Unmarshal(env.Result, &detail)
		return nil, fmt.Errorf("etherscan: %s: %s", env.Message, detail)
	}
	return env.Result, nil
}

type etherscanTx struct {
	BlockNumber     string `json:"blockNumber"`
	TimeStamp       string `json:"timeStamp"`
	Hash            string `json:"hash"`
	From            string `json:"from"`
	To              string `json:"to"`
	Value           string `json:"value"`
	IsError         string `json:"isError"`
	ContractAddress string `json:"contractAddress"`
	TokenSymbol     string `json:"tokenSymbol"`
	TokenDecimal    string `json:"tokenDecimal"`
}

type EVM struct {
	net EVMNetwork
	api *Etherscan
}

func NewEVM(net EVMNetwork, api *Etherscan) *EVM {
	return &EVM{net: net, api: api}
}

func (e *EVM) ID() string           { return e.net.ID }
func (e *EVM) Name() string         { return e.net.Name }
func (e *EVM) NativeSymbol() string { return e.net.Native }

func (e *EVM) TxURL(hash string) string {
	return e.net.Explorer + "/tx/" + hash
}

func (e *EVM) AddressURL(address string) string {
	return e.net.Explorer + "/address/" + address
}

func (e *EVM) Normalize(address string) (string, error) {
	a := strings.TrimSpace(address)
	if !evmAddrRe.MatchString(a) {
		return "", ErrInvalidAddress
	}
	return strings.ToLower(a), nil
}

func (e *EVM) Bootstrap(ctx context.Context, address string) (string, error) {
	params := url.Values{"module": {"proxy"}, "action": {"eth_blockNumber"}}
	raw, err := e.api.query(ctx, e.net.ChainID, params)
	if err != nil {
		return "", err
	}
	var hexNum string
	if err := json.Unmarshal(raw, &hexNum); err != nil {
		return "", fmt.Errorf("etherscan block number: %w", err)
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(hexNum, "0x"), 16, 64)
	if err != nil {
		return "", fmt.Errorf("etherscan block number %q: %w", hexNum, err)
	}
	return strconv.FormatInt(n, 10), nil
}

func (e *EVM) Poll(ctx context.Context, address, cursor string) ([]Event, string, error) {
	last, _ := strconv.ParseInt(cursor, 10, 64)
	start := max(last-e.net.LagBlocks, 0)

	native, err := e.list(ctx, "txlist", address, start)
	if err != nil {
		return nil, cursor, err
	}
	tokens, err := e.list(ctx, "tokentx", address, start)
	if err != nil {
		return nil, cursor, err
	}
	events, maxBlock := buildEVMEvents(address, e.net, native, tokens)
	return events, strconv.FormatInt(max(last, maxBlock), 10), nil
}

func (e *EVM) list(ctx context.Context, action, address string, start int64) ([]etherscanTx, error) {
	params := url.Values{
		"module":     {"account"},
		"action":     {action},
		"address":    {address},
		"startblock": {strconv.FormatInt(start, 10)},
		"endblock":   {"99999999"},
		"page":       {"1"},
		"offset":     {"100"},
		"sort":       {"desc"},
	}
	raw, err := e.api.query(ctx, e.net.ChainID, params)
	if err != nil {
		return nil, err
	}
	var rows []etherscanTx
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("etherscan %s: %w", action, err)
	}
	return rows, nil
}

func (e *EVM) Balance(ctx context.Context, address string) (Balance, error) {
	params := url.Values{"module": {"account"}, "action": {"balance"}, "address": {address}, "tag": {"latest"}}
	raw, err := e.api.query(ctx, e.net.ChainID, params)
	if err != nil {
		return Balance{}, err
	}
	var wei string
	if err := json.Unmarshal(raw, &wei); err != nil {
		return Balance{}, fmt.Errorf("etherscan balance: %w", err)
	}
	amount, err := FromBaseUnits(wei, 18)
	if err != nil {
		return Balance{}, err
	}
	return Balance{Amount: amount, Symbol: e.net.Native}, nil
}

func buildEVMEvents(owner string, net EVMNetwork, native, tokens []etherscanTx) ([]Event, int64) {
	owner = strings.ToLower(owner)
	var events []Event
	var maxBlock int64

	track := func(tx etherscanTx) (int64, time.Time) {
		block, _ := strconv.ParseInt(tx.BlockNumber, 10, 64)
		ts, _ := strconv.ParseInt(tx.TimeStamp, 10, 64)
		if block > maxBlock {
			maxBlock = block
		}
		return block, time.Unix(ts, 0).UTC()
	}

	for i := len(native) - 1; i >= 0; i-- {
		tx := native[i]
		block, at := track(tx)
		if tx.IsError == "1" || tx.Value == "" || tx.Value == "0" {
			continue
		}
		amount, err := FromBaseUnits(tx.Value, 18)
		if err != nil || amount == 0 {
			continue
		}
		from, to := strings.ToLower(tx.From), strings.ToLower(tx.To)
		dir := direction(owner, from, to)
		events = append(events, Event{
			TxHash:       tx.Hash,
			Key:          "native",
			Direction:    dir,
			Counterparty: counterparty(dir, from, to),
			Amount:       amount,
			Symbol:       net.Native,
			Verified:     true,
			Block:        block,
			Time:         at,
		})
	}

	known := evmTokens[net.ChainID]
	for i := len(tokens) - 1; i >= 0; i-- {
		tx := tokens[i]
		block, at := track(tx)
		if tx.Value == "" || tx.Value == "0" {
			continue
		}
		decimals, err := strconv.Atoi(tx.TokenDecimal)
		if err != nil {
			decimals = 18
		}
		amount, err := FromBaseUnits(tx.Value, decimals)
		if err != nil || amount == 0 {
			continue
		}
		contract := strings.ToLower(tx.ContractAddress)
		symbol, verified := known[contract]
		if !verified {
			symbol = sanitizeSymbol(tx.TokenSymbol)
		}
		from, to := strings.ToLower(tx.From), strings.ToLower(tx.To)
		dir := direction(owner, from, to)
		events = append(events, Event{
			TxHash:       tx.Hash,
			Key:          "erc20:" + contract + ":" + from + ":" + to + ":" + tx.Value,
			Direction:    dir,
			Counterparty: counterparty(dir, from, to),
			Amount:       amount,
			Symbol:       symbol,
			Token:        contract,
			Verified:     verified,
			Block:        block,
			Time:         at,
		})
	}
	return events, maxBlock
}

func counterparty(dir Direction, from, to string) string {
	switch dir {
	case In:
		return from
	case Out:
		return to
	default:
		return ""
	}
}

func sanitizeSymbol(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
		if b.Len() >= 12 {
			break
		}
	}
	if b.Len() == 0 {
		return "TOKEN"
	}
	return strings.ToUpper(b.String())
}
