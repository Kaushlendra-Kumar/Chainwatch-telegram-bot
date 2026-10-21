package chart

import (
	"bytes"
	"testing"
	"time"

	"chainwatch/internal/price"
)

func TestLineRendersPNG(t *testing.T) {
	start := time.Now().Add(-7 * 24 * time.Hour)
	var pts []price.Point
	for i := 0; i < 168; i++ {
		pts = append(pts, price.Point{Time: start.Add(time.Duration(i) * time.Hour), Price: 60000 + float64(i%24)*150})
	}
	png, err := Line("BTC / USD · 7d", pts, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(png, []byte("\x89PNG")) || len(png) < 5000 {
		t.Fatalf("unexpected png output, %d bytes", len(png))
	}
	if _, err := Line("x", pts[:1], 7); err == nil {
		t.Fatal("expected error for a single point")
	}
}
