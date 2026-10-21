package format

import (
	"testing"
	"time"
)

func TestUSD(t *testing.T) {
	cases := map[float64]string{
		0:              "$0.00",
		1:              "$1.00",
		63421.123:      "$63,421.12",
		1234567.5:      "$1,234,567.50",
		0.5:            "$0.5000",
		0.00001234:     "$0.00001234",
		-1500:          "-$1,500.00",
		999.999:        "$1,000.00",
		0.000000123456: "$0.0000001235",
	}
	for in, want := range cases {
		if got := USD(in); got != want {
			t.Errorf("USD(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestAmount(t *testing.T) {
	cases := map[float64]string{
		0:          "0",
		1.5:        "1.5",
		2000:       "2,000",
		12345.678:  "12,345.68",
		0.00012345: "0.00012345",
		3.14159265: "3.1416",
	}
	for in, want := range cases {
		if got := Amount(in); got != want {
			t.Errorf("Amount(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestPercentAndCompact(t *testing.T) {
	if got := Percent(2.345); got != "+2.35%" && got != "+2.34%" {
		t.Errorf("Percent = %q", got)
	}
	if got := Percent(-1); got != "-1.00%" {
		t.Errorf("Percent = %q", got)
	}
	if got := CompactUSD(1.23e12); got != "$1.23T" {
		t.Errorf("CompactUSD = %q", got)
	}
	if got := CompactUSD(456.7e6); got != "$456.70M" {
		t.Errorf("CompactUSD = %q", got)
	}
}

func TestShortAddrAndAgo(t *testing.T) {
	if got := ShortAddr("0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045"); got != "0xd8dA…6045" {
		t.Errorf("ShortAddr = %q", got)
	}
	if got := ShortAddr("short"); got != "short" {
		t.Errorf("ShortAddr = %q", got)
	}
	now := time.Now()
	if got := Ago(now.Add(-3*time.Hour), now); got != "3h ago" {
		t.Errorf("Ago = %q", got)
	}
	if got := Ago(now.Add(-72*time.Hour), now); got != "3d ago" {
		t.Errorf("Ago = %q", got)
	}
}
