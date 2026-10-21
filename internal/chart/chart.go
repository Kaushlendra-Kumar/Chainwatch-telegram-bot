package chart

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	gochart "github.com/wcharczuk/go-chart/v2"
	"github.com/wcharczuk/go-chart/v2/drawing"

	"chainwatch/internal/format"
	"chainwatch/internal/price"
)

var (
	green = drawing.ColorFromHex("16a34a")
	red   = drawing.ColorFromHex("dc2626")
	grid  = drawing.ColorFromHex("e5e7eb")
)

func Line(title string, points []price.Point, days int) ([]byte, error) {
	if len(points) < 2 {
		return nil, errors.New("not enough data points")
	}
	xs := make([]time.Time, len(points))
	ys := make([]float64, len(points))
	lo, hi := points[0].Price, points[0].Price
	for i, p := range points {
		xs[i], ys[i] = p.Time, p.Price
		lo, hi = min(lo, p.Price), max(hi, p.Price)
	}
	color := green
	if ys[len(ys)-1] < ys[0] {
		color = red
	}
	pad := (hi - lo) * 0.08
	if pad == 0 {
		pad = hi * 0.01
	}

	layout := "Jan 02"
	if days <= 1 {
		layout = "15:04"
	}

	graph := gochart.Chart{
		Title:  title,
		Width:  1100,
		Height: 560,
		TitleStyle: gochart.Style{
			FontSize: 16,
		},
		Background: gochart.Style{
			Padding: gochart.Box{Top: 60, Left: 20, Right: 30, Bottom: 20},
		},
		XAxis: gochart.XAxis{
			ValueFormatter: gochart.TimeValueFormatterWithFormat(layout),
			GridMajorStyle: gochart.Style{StrokeColor: grid, StrokeWidth: 1},
		},
		YAxis: gochart.YAxis{
			Range: &gochart.ContinuousRange{Min: lo - pad, Max: hi + pad},
			ValueFormatter: func(v interface{}) string {
				if f, ok := v.(float64); ok {
					return format.USD(f)
				}
				return ""
			},
			GridMajorStyle: gochart.Style{StrokeColor: grid, StrokeWidth: 1},
		},
		Series: []gochart.Series{
			gochart.TimeSeries{
				Name:    title,
				XValues: xs,
				YValues: ys,
				Style: gochart.Style{
					StrokeColor: color,
					StrokeWidth: 2.5,
					FillColor:   color.WithAlpha(40),
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := graph.Render(gochart.PNG, &buf); err != nil {
		return nil, fmt.Errorf("render chart: %w", err)
	}
	return buf.Bytes(), nil
}
