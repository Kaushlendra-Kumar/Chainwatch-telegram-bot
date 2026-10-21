package price

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"chainwatch/internal/httpx"
)

type Binance struct {
	base  string
	http  *httpx.Client
	ttl   time.Duration
	mu    sync.Mutex
	all   map[string]float64
	allAt time.Time
}

func NewBinance(base string) *Binance {
	return &Binance{
		base: strings.TrimRight(base, "/"),
		http: httpx.New(10*time.Second, httpx.NewLimiter(10), nil),
		ttl:  15 * time.Second,
	}
}

func (b *Binance) Name() string { return "binance" }

func (b *Binance) allPrices(ctx context.Context) (map[string]float64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.all != nil && time.Since(b.allAt) < b.ttl {
		return b.all, nil
	}
	var rows []struct {
		Symbol string `json:"symbol"`
		Price  string `json:"price"`
	}
	if err := b.http.GetJSON(ctx, b.base+"/api/v3/ticker/price", &rows); err != nil {
		return nil, fmt.Errorf("binance prices: %w", err)
	}
	all := make(map[string]float64, len(rows))
	for _, r := range rows {
		base, ok := strings.CutSuffix(r.Symbol, "USDT")
		if !ok || base == "" {
			continue
		}
		p, err := strconv.ParseFloat(r.Price, 64)
		if err != nil || p <= 0 {
			continue
		}
		all[base] = p
	}
	b.all, b.allAt = all, time.Now()
	return all, nil
}

func (b *Binance) Quotes(ctx context.Context, symbols []string, withChange bool) (map[string]Quote, error) {
	all, err := b.allPrices(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Quote, len(symbols))
	var found []string
	for _, s := range symbols {
		if s == "USDT" {
			out[s] = Quote{Symbol: s, Price: 1, HasChange: true, Source: b.Name()}
			continue
		}
		if p, ok := all[s]; ok {
			out[s] = Quote{Symbol: s, Price: p, Source: b.Name()}
			found = append(found, s)
		}
	}
	if !withChange || len(found) == 0 {
		return out, nil
	}
	for start := 0; start < len(found); start += 100 {
		end := min(start+100, len(found))
		pairs := make([]string, 0, end-start)
		for _, s := range found[start:end] {
			pairs = append(pairs, s+"USDT")
		}
		raw, _ := json.Marshal(pairs)
		var rows []struct {
			Symbol             string `json:"symbol"`
			LastPrice          string `json:"lastPrice"`
			PriceChangePercent string `json:"priceChangePercent"`
		}
		u := b.base + "/api/v3/ticker/24hr?symbols=" + url.QueryEscape(string(raw))
		if err := b.http.GetJSON(ctx, u, &rows); err != nil {
			return out, nil
		}
		for _, r := range rows {
			s := strings.TrimSuffix(r.Symbol, "USDT")
			q, ok := out[s]
			if !ok {
				continue
			}
			if p, err := strconv.ParseFloat(r.LastPrice, 64); err == nil && p > 0 {
				q.Price = p
			}
			if c, err := strconv.ParseFloat(r.PriceChangePercent, 64); err == nil {
				q.Change24h, q.HasChange = c, true
			}
			out[s] = q
		}
	}
	return out, nil
}

func (b *Binance) History(ctx context.Context, symbol string, days int) ([]Point, error) {
	all, err := b.allPrices(ctx)
	if err != nil {
		return nil, err
	}
	if _, ok := all[symbol]; !ok {
		return nil, ErrNotFound
	}
	interval, limit := ChartWindow(days)
	u := fmt.Sprintf("%s/api/v3/klines?symbol=%sUSDT&interval=%s&limit=%d", b.base, symbol, interval, limit)
	var rows [][]json.RawMessage
	if err := b.http.GetJSON(ctx, u, &rows); err != nil {
		return nil, fmt.Errorf("binance klines: %w", err)
	}
	points := make([]Point, 0, len(rows))
	for _, r := range rows {
		if len(r) < 5 {
			continue
		}
		var openMs int64
		var closeStr string
		if json.Unmarshal(r[0], &openMs) != nil || json.Unmarshal(r[4], &closeStr) != nil {
			continue
		}
		p, err := strconv.ParseFloat(closeStr, 64)
		if err != nil {
			continue
		}
		points = append(points, Point{Time: time.UnixMilli(openMs), Price: p})
	}
	if len(points) < 2 {
		return nil, ErrNotFound
	}
	return points, nil
}
