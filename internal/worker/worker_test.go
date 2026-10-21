package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"chainwatch/internal/chain"
	"chainwatch/internal/price"
	"chainwatch/internal/store"
)

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeAlertStore struct {
	alerts      []store.Alert
	claimed     map[int64]bool
	released    []int64
	deactivated []int64
}

func (f *fakeAlertStore) ActiveAlerts(context.Context) ([]store.Alert, error) { return f.alerts, nil }

func (f *fakeAlertStore) ClaimAlert(_ context.Context, id int64, _ float64) (bool, error) {
	if f.claimed[id] {
		return false, nil
	}
	f.claimed[id] = true
	return true, nil
}

func (f *fakeAlertStore) ReleaseAlert(_ context.Context, id int64) error {
	f.released = append(f.released, id)
	delete(f.claimed, id)
	return nil
}

func (f *fakeAlertStore) DeactivateChat(_ context.Context, chatID int64) error {
	f.deactivated = append(f.deactivated, chatID)
	return nil
}

type fakePrices map[string]float64

func (f fakePrices) Quotes(_ context.Context, symbols []string, _ bool) (map[string]price.Quote, error) {
	out := map[string]price.Quote{}
	for _, s := range symbols {
		if p, ok := f[s]; ok {
			out[s] = price.Quote{Symbol: s, Price: p}
		}
	}
	return out, nil
}

type fakeNotifier struct {
	sent []string
	errs map[int64]error
}

func (f *fakeNotifier) Notify(_ context.Context, chatID int64, html string) error {
	if err := f.errs[chatID]; err != nil {
		return err
	}
	f.sent = append(f.sent, html)
	return nil
}

func TestTriggered(t *testing.T) {
	cases := []struct {
		dir     string
		target  float64
		current float64
		want    bool
	}{
		{"above", 100, 100, true},
		{"above", 100, 99.99, false},
		{"below", 100, 99, true},
		{"below", 100, 101, false},
		{"below", 100, 0, false},
		{"sideways", 1, 1, false},
	}
	for _, c := range cases {
		if got := Triggered(c.dir, c.target, c.current); got != c.want {
			t.Errorf("Triggered(%s,%v,%v)=%v", c.dir, c.target, c.current, got)
		}
	}
}

func TestAlertWorkerFiresAndRetries(t *testing.T) {
	st := &fakeAlertStore{
		claimed: map[int64]bool{},
		alerts: []store.Alert{
			{ID: 1, ChatID: 10, Symbol: "BTC", Direction: "above", Target: 100000, CreatedAt: time.Now()},
			{ID: 2, ChatID: 20, Symbol: "BTC", Direction: "below", Target: 50000, CreatedAt: time.Now()},
			{ID: 3, ChatID: 30, Symbol: "ETH", Direction: "above", Target: 1000, CreatedAt: time.Now()},
			{ID: 4, ChatID: 40, Symbol: "ETH", Direction: "above", Target: 1000, CreatedAt: time.Now()},
		},
	}
	n := &fakeNotifier{errs: map[int64]error{
		30: errors.New("telegram timeout"),
		40: ErrRecipientGone,
	}}
	w := NewAlertWorker(st, fakePrices{"BTC": 100500, "ETH": 3000}, n, time.Second, quietLog)
	w.reload(context.Background())
	w.check(context.Background())

	if len(n.sent) != 1 || !strings.Contains(n.sent[0], "BTC") {
		t.Fatalf("expected one BTC notification, got %v", n.sent)
	}
	if len(st.released) != 1 || st.released[0] != 3 {
		t.Fatalf("alert 3 should be released for retry, got %v", st.released)
	}
	if len(st.deactivated) != 1 || st.deactivated[0] != 40 {
		t.Fatalf("chat 40 should be deactivated, got %v", st.deactivated)
	}
	ids := map[int64]bool{}
	for _, a := range w.alerts {
		ids[a.ID] = true
	}
	if !ids[2] || !ids[3] || ids[1] || ids[4] || len(ids) != 2 {
		t.Fatalf("remaining alerts wrong: %v", ids)
	}
}

type fakeTracker struct{}

func (fakeTracker) ID() string                                        { return "eth" }
func (fakeTracker) Name() string                                      { return "Ethereum" }
func (fakeTracker) NativeSymbol() string                              { return "ETH" }
func (fakeTracker) Normalize(a string) (string, error)                { return a, nil }
func (fakeTracker) Bootstrap(context.Context, string) (string, error) { return "1", nil }
func (fakeTracker) Poll(context.Context, string, string) ([]chain.Event, string, error) {
	return nil, "", nil
}
func (fakeTracker) Balance(context.Context, string) (chain.Balance, error) {
	return chain.Balance{}, nil
}
func (fakeTracker) TxURL(h string) string      { return "https://x/tx/" + h }
func (fakeTracker) AddressURL(a string) string { return "https://x/a/" + a }

func TestValuePassesAndMessage(t *testing.T) {
	quotes := map[string]price.Quote{"ETH": {Price: 2000}, "USDC": {Price: 1}}
	eth := Value(chain.Event{TxHash: "0x1", Direction: chain.In, Amount: 1.5, Symbol: "WETH", Verified: true, Counterparty: "0xabcdef0123456789abcdef0123456789abcdef01"}, quotes)
	if !eth.Known || eth.USD != 3000 {
		t.Fatalf("eth valuation = %+v", eth)
	}
	spam := Value(chain.Event{TxHash: "0x2", Direction: chain.In, Amount: 1e6, Symbol: "USDC", Verified: false}, quotes)
	if spam.Known {
		t.Fatal("unverified token must not be valued")
	}
	if !Passes(0, spam) || Passes(100, spam) || !Passes(1000, eth) || Passes(5000, eth) {
		t.Fatal("threshold filtering is wrong")
	}

	sub := store.Subscription{ID: 7, Address: "0xd8da6bf26964af9d7eed9e03e53415d37aa96045", Label: "<Vitalik>"}
	msg := WalletMessage(fakeTracker{}, sub, []EventView{spam, eth, eth}, 2)
	for _, want := range []string{"&lt;Vitalik&gt;", "Received 1.5 WETH", "≈ $3,000.00", "and 1 earlier", "/minusd 7"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "unverified") {
		t.Fatalf("oldest item should have been collapsed:\n%s", msg)
	}
}
