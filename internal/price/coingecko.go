package price

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"chainwatch/internal/httpx"
)

var coingeckoIDs = map[string]string{
	"BTC":    "bitcoin",
	"ETH":    "ethereum",
	"USDT":   "tether",
	"BNB":    "binancecoin",
	"SOL":    "solana",
	"XRP":    "ripple",
	"USDC":   "usd-coin",
	"DOGE":   "dogecoin",
	"ADA":    "cardano",
	"TRX":    "tron",
	"TON":    "the-open-network",
	"AVAX":   "avalanche-2",
	"SHIB":   "shiba-inu",
	"LINK":   "chainlink",
	"DOT":    "polkadot",
	"BCH":    "bitcoin-cash",
	"LTC":    "litecoin",
	"NEAR":   "near",
	"POL":    "polygon-ecosystem-token",
	"UNI":    "uniswap",
	"ATOM":   "cosmos",
	"XLM":    "stellar",
	"ETC":    "ethereum-classic",
	"APT":    "aptos",
	"ARB":    "arbitrum",
	"OP":     "optimism",
	"PEPE":   "pepe",
	"SUI":    "sui",
	"DAI":    "dai",
	"AAVE":   "aave",
	"FIL":    "filecoin",
	"HBAR":   "hedera-hashgraph",
	"ICP":    "internet-computer",
	"JUP":    "jupiter-exchange-solana",
	"BONK":   "bonk",
	"WIF":    "dogwifcoin",
	"INJ":    "injective-protocol",
	"RENDER": "render-token",
}

type CoinGecko struct {
	base    string
	http    *httpx.Client
	mu      sync.Mutex
	ids     map[string]string
	missing map[string]time.Time
	topMu   sync.Mutex
	top     []MarketCoin
	topAt   time.Time
}

func NewCoinGecko(base, apiKey string) *CoinGecko {
	headers := map[string]string{}
	if apiKey != "" {
		headers["x-cg-demo-api-key"] = apiKey
	}
	ids := make(map[string]string, len(coingeckoIDs))
	for k, v := range coingeckoIDs {
		ids[k] = v
	}
	return &CoinGecko{
		base:    strings.TrimRight(base, "/"),
		http:    httpx.New(15*time.Second, httpx.NewLimiter(0.5), headers),
		ids:     ids,
		missing: map[string]time.Time{},
	}
}

func (g *CoinGecko) Name() string { return "coingecko" }

func (g *CoinGecko) resolve(ctx context.Context, symbol string) (string, error) {
	g.mu.Lock()
	if id, ok := g.ids[symbol]; ok {
		g.mu.Unlock()
		return id, nil
	}
	if until, ok := g.missing[symbol]; ok && time.Now().Before(until) {
		g.mu.Unlock()
		return "", ErrNotFound
	}
	g.mu.Unlock()

	var res struct {
		Coins []struct {
			ID     string `json:"id"`
			Symbol string `json:"symbol"`
			Rank   *int   `json:"market_cap_rank"`
		} `json:"coins"`
	}
	if err := g.http.GetJSON(ctx, g.base+"/search?query="+url.QueryEscape(symbol), &res); err != nil {
		return "", fmt.Errorf("coingecko search: %w", err)
	}
	const unranked = 1 << 30
	best, bestRank := "", unranked
	for _, c := range res.Coins {
		if !strings.EqualFold(c.Symbol, symbol) {
			continue
		}
		rank := unranked
		if c.Rank != nil && *c.Rank > 0 {
			rank = *c.Rank
		}
		if best == "" || rank < bestRank {
			best, bestRank = c.ID, rank
		}
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if best == "" {
		g.missing[symbol] = time.Now().Add(time.Hour)
		return "", ErrNotFound
	}
	g.ids[symbol] = best
	return best, nil
}

func (g *CoinGecko) Quotes(ctx context.Context, symbols []string, withChange bool) (map[string]Quote, error) {
	bySymbol := map[string]string{}
	var ids []string
	var lastErr error
	for _, s := range symbols {
		id, err := g.resolve(ctx, s)
		if err != nil {
			if err != ErrNotFound {
				lastErr = err
			}
			continue
		}
		bySymbol[s] = id
		ids = append(ids, id)
	}
	out := map[string]Quote{}
	if len(ids) == 0 {
		return out, lastErr
	}
	var res map[string]map[string]float64
	u := fmt.Sprintf("%s/simple/price?ids=%s&vs_currencies=usd&include_24hr_change=true", g.base, url.QueryEscape(strings.Join(ids, ",")))
	if err := g.http.GetJSON(ctx, u, &res); err != nil {
		return out, fmt.Errorf("coingecko price: %w", err)
	}
	for s, id := range bySymbol {
		row, ok := res[id]
		if !ok || row["usd"] <= 0 {
			continue
		}
		change, hasChange := row["usd_24h_change"]
		out[s] = Quote{Symbol: s, Price: row["usd"], Change24h: change, HasChange: hasChange, Source: g.Name()}
	}
	return out, nil
}

func (g *CoinGecko) History(ctx context.Context, symbol string, days int) ([]Point, error) {
	id, err := g.resolve(ctx, symbol)
	if err != nil {
		return nil, err
	}
	var res struct {
		Prices [][2]float64 `json:"prices"`
	}
	u := fmt.Sprintf("%s/coins/%s/market_chart?vs_currency=usd&days=%d", g.base, url.PathEscape(id), days)
	if err := g.http.GetJSON(ctx, u, &res); err != nil {
		return nil, fmt.Errorf("coingecko chart: %w", err)
	}
	points := make([]Point, 0, len(res.Prices))
	for _, p := range res.Prices {
		points = append(points, Point{Time: time.UnixMilli(int64(p[0])), Price: p[1]})
	}
	if len(points) < 2 {
		return nil, ErrNotFound
	}
	return points, nil
}

func (g *CoinGecko) Top(ctx context.Context, n int) ([]MarketCoin, error) {
	g.topMu.Lock()
	defer g.topMu.Unlock()
	if len(g.top) >= n && time.Since(g.topAt) < 10*time.Minute {
		return g.top[:n], nil
	}
	var rows []struct {
		Symbol    string   `json:"symbol"`
		Name      string   `json:"name"`
		Price     float64  `json:"current_price"`
		MarketCap float64  `json:"market_cap"`
		Rank      int      `json:"market_cap_rank"`
		Change    *float64 `json:"price_change_percentage_24h"`
	}
	u := fmt.Sprintf("%s/coins/markets?vs_currency=usd&order=market_cap_desc&per_page=%d&page=1", g.base, n)
	if err := g.http.GetJSON(ctx, u, &rows); err != nil {
		return nil, fmt.Errorf("coingecko markets: %w", err)
	}
	coins := make([]MarketCoin, 0, len(rows))
	for _, r := range rows {
		c := MarketCoin{Rank: r.Rank, Symbol: strings.ToUpper(r.Symbol), Name: r.Name, Price: r.Price, MarketCap: r.MarketCap}
		if r.Change != nil {
			c.Change24h = *r.Change
		}
		coins = append(coins, c)
	}
	g.top, g.topAt = coins, time.Now()
	return coins[:min(n, len(coins))], nil
}
