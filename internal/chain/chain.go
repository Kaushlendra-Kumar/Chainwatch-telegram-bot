package chain

import (
	"context"
	"errors"
	"math/big"
	"sort"
	"strings"
	"time"
)

var ErrInvalidAddress = errors.New("invalid address")

type Direction string

const (
	In   Direction = "in"
	Out  Direction = "out"
	Self Direction = "self"
)

type Event struct {
	TxHash       string
	Key          string
	Direction    Direction
	Counterparty string
	Amount       float64
	Symbol       string
	Token        string
	Verified     bool
	Block        int64
	Time         time.Time
}

type Balance struct {
	Amount float64
	Symbol string
}

type Tracker interface {
	ID() string
	Name() string
	NativeSymbol() string
	Normalize(address string) (string, error)
	Bootstrap(ctx context.Context, address string) (string, error)
	Poll(ctx context.Context, address, cursor string) ([]Event, string, error)
	Balance(ctx context.Context, address string) (Balance, error)
	TxURL(hash string) string
	AddressURL(address string) string
}

var chainAliases = map[string]string{
	"ethereum": "eth",
	"ether":    "eth",
	"matic":    "polygon",
	"pol":      "polygon",
	"arb":      "arbitrum",
	"bitcoin":  "btc",
	"solana":   "sol",
}

type Registry struct {
	trackers map[string]Tracker
}

func NewRegistry(trackers ...Tracker) *Registry {
	r := &Registry{trackers: map[string]Tracker{}}
	for _, t := range trackers {
		if t != nil {
			r.trackers[t.ID()] = t
		}
	}
	return r
}

func (r *Registry) Get(id string) (Tracker, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if a, ok := chainAliases[id]; ok {
		id = a
	}
	t, ok := r.trackers[id]
	return t, ok
}

func (r *Registry) All() []Tracker {
	out := make([]Tracker, 0, len(r.trackers))
	for _, t := range r.trackers {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

func FromBaseUnits(raw string, decimals int) (float64, error) {
	n, ok := new(big.Int).SetString(strings.TrimSpace(raw), 10)
	if !ok {
		return 0, errors.New("invalid integer amount")
	}
	return BigToFloat(n, decimals), nil
}

func BigToFloat(n *big.Int, decimals int) float64 {
	if decimals < 0 {
		decimals = 0
	}
	div := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(n), new(big.Float).SetInt(div)).Float64()
	return f
}

func direction(owner, from, to string) Direction {
	switch {
	case from == owner && to == owner:
		return Self
	case to == owner:
		return In
	default:
		return Out
	}
}
