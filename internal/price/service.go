package price

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

type entry struct {
	quote Quote
	at    time.Time
}

type Service struct {
	primary     Provider
	fallback    Provider
	top         TopProvider
	log         *slog.Logger
	mu          sync.Mutex
	cache       map[string]entry
	missing     map[string]time.Time
	ttl         time.Duration
	fallbackTTL time.Duration
	missingTTL  time.Duration
	now         func() time.Time
}

func NewService(primary, fallback Provider, top TopProvider, log *slog.Logger) *Service {
	return &Service{
		primary:     primary,
		fallback:    fallback,
		top:         top,
		log:         log,
		cache:       map[string]entry{},
		missing:     map[string]time.Time{},
		ttl:         20 * time.Second,
		fallbackTTL: 5 * time.Minute,
		missingTTL:  30 * time.Minute,
		now:         time.Now,
	}
}

func (s *Service) Quote(ctx context.Context, symbol string, withChange bool) (Quote, error) {
	sym := Normalize(symbol)
	quotes, err := s.Quotes(ctx, []string{sym}, withChange)
	if q, ok := quotes[sym]; ok {
		return q, nil
	}
	if err != nil {
		return Quote{}, err
	}
	return Quote{}, ErrNotFound
}

func (s *Service) Quotes(ctx context.Context, symbols []string, withChange bool) (map[string]Quote, error) {
	out := map[string]Quote{}
	var need []string
	seen := map[string]bool{}
	now := s.now()

	s.mu.Lock()
	for _, raw := range symbols {
		sym := Normalize(raw)
		if sym == "" || seen[sym] {
			continue
		}
		seen[sym] = true
		if e, ok := s.cache[sym]; ok && now.Sub(e.at) < s.ttlFor(e.quote) && (!withChange || e.quote.HasChange) {
			out[sym] = e.quote
			continue
		}
		if until, ok := s.missing[sym]; ok && now.Before(until) {
			continue
		}
		need = append(need, sym)
	}
	s.mu.Unlock()

	if len(need) == 0 {
		return out, nil
	}

	var errs []error
	rest := need
	for _, p := range []Provider{s.primary, s.fallback} {
		if p == nil || len(rest) == 0 {
			continue
		}
		got, err := p.Quotes(ctx, rest, withChange)
		if err != nil {
			errs = append(errs, err)
			s.log.Debug("price provider failed", "provider", p.Name(), "err", err)
		}
		s.store(got)
		var next []string
		for _, sym := range rest {
			if q, ok := got[sym]; ok {
				out[sym] = q
			} else {
				next = append(next, sym)
			}
		}
		rest = next
	}

	s.mu.Lock()
	for _, sym := range rest {
		if pegged[sym] {
			q := Quote{Symbol: sym, Price: 1, HasChange: true, Source: "peg"}
			out[sym] = q
			s.cache[sym] = entry{quote: q, at: s.now()}
			continue
		}
		if len(errs) == 0 {
			s.missing[sym] = s.now().Add(s.missingTTL)
		}
	}
	s.mu.Unlock()

	if len(out) == 0 && len(errs) > 0 {
		return out, errors.Join(errs...)
	}
	return out, nil
}

func (s *Service) History(ctx context.Context, symbol string, days int) ([]Point, error) {
	sym := Normalize(symbol)
	var lastErr error = ErrNotFound
	for _, p := range []Provider{s.primary, s.fallback} {
		if p == nil {
			continue
		}
		pts, err := p.History(ctx, sym, days)
		if err == nil {
			return pts, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (s *Service) Top(ctx context.Context, n int) ([]MarketCoin, error) {
	if s.top == nil {
		return nil, errors.New("top list provider not configured")
	}
	return s.top.Top(ctx, n)
}

func (s *Service) ttlFor(q Quote) time.Duration {
	if s.primary != nil && q.Source == s.primary.Name() {
		return s.ttl
	}
	return s.fallbackTTL
}

func (s *Service) store(quotes map[string]Quote) {
	if len(quotes) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for sym, q := range quotes {
		s.cache[sym] = entry{quote: q, at: now}
		delete(s.missing, sym)
	}
}
