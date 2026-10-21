package bot

import (
	"math"
	"testing"
	"time"

	"chainwatch/internal/price"
	"chainwatch/internal/store"
)

func TestParseNumber(t *testing.T) {
	ok := map[string]float64{
		"100000":    100000,
		"100k":      100000,
		"1.5M":      1500000,
		"$2,500":    2500,
		"0.0001":    0.0001,
		"1_000":     1000,
		" 2b ":      2e9,
		"$64,250.5": 64250.5,
	}
	for in, want := range ok {
		got, err := ParseNumber(in)
		if err != nil || math.Abs(got-want) > 1e-9 {
			t.Errorf("ParseNumber(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "-5", "0", "NaN", "1e999", "k"} {
		if _, err := ParseNumber(in); err == nil {
			t.Errorf("ParseNumber(%q) should fail", in)
		}
	}
}

func TestParseDirectionIDDays(t *testing.T) {
	if d, ok := ParseDirection(">"); !ok || d != "above" {
		t.Fatal("> should be above")
	}
	if d, ok := ParseDirection("Below"); !ok || d != "below" {
		t.Fatal("Below should be below")
	}
	if _, ok := ParseDirection("sideways"); ok {
		t.Fatal("sideways is not a direction")
	}
	if id, ok := ParseID("#12"); !ok || id != 12 {
		t.Fatal("#12 should parse")
	}
	if _, ok := ParseID("-3"); ok {
		t.Fatal("negative id must fail")
	}
	if d, ok := ParseDays(""); !ok || d != 7 {
		t.Fatal("default should be 7 days")
	}
	if d, ok := ParseDays("1y"); !ok || d != 365 {
		t.Fatal("1y should be 365")
	}
	if _, ok := ParseDays("2w"); ok {
		t.Fatal("2w unsupported")
	}
}

func TestSummarize(t *testing.T) {
	holdings := []store.Holding{
		{Symbol: "BTC", Quantity: 0.5, AvgCost: 60000},
		{Symbol: "ETH", Quantity: 2, AvgCost: 3000},
		{Symbol: "XYZ", Quantity: 10, AvgCost: 1},
	}
	quotes := map[string]price.Quote{"BTC": {Price: 70000}, "ETH": {Price: 2500}}
	s := Summarize(holdings, quotes)
	if s.Value != 40000 || s.Cost != 36000 || s.PnL != 4000 {
		t.Fatalf("totals wrong: %+v", s)
	}
	if s.Lines[0].PnL != 5000 || math.Abs(s.Lines[0].PnLPct-16.6666667) > 1e-6 {
		t.Fatalf("btc line wrong: %+v", s.Lines[0])
	}
	if s.Lines[1].PnL != -1000 || s.Lines[1].Weight != 12.5 {
		t.Fatalf("eth line wrong: %+v", s.Lines[1])
	}
	if s.Lines[2].Known {
		t.Fatal("unpriced holding must be marked unknown")
	}
}

func TestRateLimiter(t *testing.T) {
	r := newRateLimiter(1, 2)
	now := time.Now()
	if !r.allow(1, now) || !r.allow(1, now) {
		t.Fatal("burst of 2 should pass")
	}
	if r.allow(1, now) {
		t.Fatal("third immediate request should be throttled")
	}
	if !r.allow(2, now) {
		t.Fatal("other users are independent")
	}
	if !r.allow(1, now.Add(1100*time.Millisecond)) {
		t.Fatal("token should refill after 1s")
	}
}
