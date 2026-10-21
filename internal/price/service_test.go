package price

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

type fakeProvider struct {
	name   string
	prices map[string]float64
	err    error
	calls  int
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Quotes(_ context.Context, symbols []string, _ bool) (map[string]Quote, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]Quote{}
	for _, s := range symbols {
		if p, ok := f.prices[s]; ok {
			out[s] = Quote{Symbol: s, Price: p, HasChange: true, Source: f.name}
		}
	}
	return out, nil
}

func (f *fakeProvider) History(context.Context, string, int) ([]Point, error) {
	return nil, ErrNotFound
}

func newTestService(primary, fallback Provider) *Service {
	return NewService(primary, fallback, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestQuotesUsesFallbackForMissing(t *testing.T) {
	p := &fakeProvider{name: "p", prices: map[string]float64{"BTC": 100}}
	f := &fakeProvider{name: "f", prices: map[string]float64{"XYZ": 2}}
	s := newTestService(p, f)

	got, err := s.Quotes(context.Background(), []string{"btc", "xyz"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got["BTC"].Price != 100 || got["XYZ"].Price != 2 {
		t.Fatalf("unexpected quotes: %+v", got)
	}
	if got["XYZ"].Source != "f" {
		t.Fatalf("expected fallback source, got %q", got["XYZ"].Source)
	}
}

func TestQuotesCachesAndNormalizesAliases(t *testing.T) {
	p := &fakeProvider{name: "p", prices: map[string]float64{"ETH": 3000}}
	s := newTestService(p, nil)

	for i := 0; i < 3; i++ {
		got, err := s.Quotes(context.Background(), []string{"WETH"}, false)
		if err != nil || got["ETH"].Price != 3000 {
			t.Fatalf("got %+v err %v", got, err)
		}
	}
	if p.calls != 1 {
		t.Fatalf("expected 1 provider call, got %d", p.calls)
	}
}

func TestQuotesNegativeCacheAndPeg(t *testing.T) {
	p := &fakeProvider{name: "p", prices: map[string]float64{}}
	s := newTestService(p, nil)

	got, _ := s.Quotes(context.Background(), []string{"NOPE", "USDC"}, false)
	if _, ok := got["NOPE"]; ok {
		t.Fatal("NOPE should be missing")
	}
	if got["USDC"].Price != 1 {
		t.Fatalf("USDC should fall back to peg, got %+v", got["USDC"])
	}
	_, _ = s.Quotes(context.Background(), []string{"NOPE"}, false)
	if p.calls != 1 {
		t.Fatalf("missing symbol should be negatively cached, calls=%d", p.calls)
	}
}

func TestQuotesErrorWhenAllProvidersFail(t *testing.T) {
	boom := errors.New("down")
	s := newTestService(&fakeProvider{name: "p", err: boom}, &fakeProvider{name: "f", err: boom})
	if _, err := s.Quote(context.Background(), "BTC", false); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("expected provider error, got %v", err)
	}
	if _, err := s.Quotes(context.Background(), []string{"BTC"}, false); err == nil {
		t.Fatal("expected error")
	}
}
