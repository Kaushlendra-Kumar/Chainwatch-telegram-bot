package price

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
)

var ErrNotFound = errors.New("symbol not found")

type Quote struct {
	Symbol    string
	Price     float64
	Change24h float64
	HasChange bool
	Source    string
}

type Point struct {
	Time  time.Time
	Price float64
}

type MarketCoin struct {
	Rank      int
	Symbol    string
	Name      string
	Price     float64
	Change24h float64
	MarketCap float64
}

type Provider interface {
	Name() string
	Quotes(ctx context.Context, symbols []string, withChange bool) (map[string]Quote, error)
	History(ctx context.Context, symbol string, days int) ([]Point, error)
}

type TopProvider interface {
	Top(ctx context.Context, n int) ([]MarketCoin, error)
}

var symbolRe = regexp.MustCompile(`^[A-Z0-9]{1,15}$`)

var aliases = map[string]string{
	"XBT":    "BTC",
	"WBTC":   "BTC",
	"WETH":   "ETH",
	"WSOL":   "SOL",
	"WPOL":   "POL",
	"WMATIC": "POL",
	"MATIC":  "POL",
	"USDC.E": "USDC",
	"WBNB":   "BNB",
}

var pegged = map[string]bool{
	"USDT":  true,
	"USDC":  true,
	"DAI":   true,
	"FDUSD": true,
	"TUSD":  true,
	"USDE":  true,
	"PYUSD": true,
	"USDS":  true,
}

func Normalize(symbol string) string {
	s := strings.ToUpper(strings.TrimSpace(symbol))
	s = strings.TrimPrefix(s, "$")
	if a, ok := aliases[s]; ok {
		return a
	}
	return s
}

func ValidSymbol(symbol string) bool {
	return symbolRe.MatchString(Normalize(symbol))
}

func IsPegged(symbol string) bool {
	return pegged[Normalize(symbol)]
}

func ChartWindow(days int) (interval string, limit int) {
	switch {
	case days <= 1:
		return "15m", 96
	case days <= 7:
		return "1h", 168
	case days <= 30:
		return "4h", 180
	case days <= 90:
		return "1d", 90
	default:
		return "1d", 365
	}
}
