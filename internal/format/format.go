package format

import (
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
	"time"
)

func Escape(s string) string {
	return html.EscapeString(s)
}

func USD(v float64) string {
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	switch {
	case v == 0:
		return "$0.00"
	case v >= 1:
		return sign + "$" + withCommas(strconv.FormatFloat(v, 'f', 2, 64))
	case v >= 0.01:
		return sign + "$" + strconv.FormatFloat(v, 'f', 4, 64)
	default:
		return sign + "$" + trimZeros(strconv.FormatFloat(v, 'f', 10, 64))
	}
}

func SignedUSD(v float64) string {
	if v > 0 {
		return "+" + USD(v)
	}
	return USD(v)
}

func Amount(v float64) string {
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	switch {
	case v == 0:
		return "0"
	case v >= 1000:
		return sign + withCommas(trimZeros(strconv.FormatFloat(v, 'f', 2, 64)))
	case v >= 1:
		return sign + trimZeros(strconv.FormatFloat(v, 'f', 4, 64))
	default:
		return sign + trimZeros(strconv.FormatFloat(v, 'f', 8, 64))
	}
}

func Percent(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "n/a"
	}
	s := strconv.FormatFloat(v, 'f', 2, 64)
	if v > 0 {
		s = "+" + s
	}
	return s + "%"
}

func Trend(v float64) string {
	switch {
	case v > 0:
		return "🟢"
	case v < 0:
		return "🔴"
	default:
		return "⚪"
	}
}

func CompactUSD(v float64) string {
	abs := math.Abs(v)
	switch {
	case abs >= 1e12:
		return fmt.Sprintf("$%.2fT", v/1e12)
	case abs >= 1e9:
		return fmt.Sprintf("$%.2fB", v/1e9)
	case abs >= 1e6:
		return fmt.Sprintf("$%.2fM", v/1e6)
	case abs >= 1e3:
		return fmt.Sprintf("$%.2fK", v/1e3)
	default:
		return USD(v)
	}
}

func ShortAddr(a string) string {
	r := []rune(a)
	if len(r) <= 14 {
		return a
	}
	return string(r[:6]) + "…" + string(r[len(r)-4:])
}

func Ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func trimZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

func withCommas(s string) string {
	intPart, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, frac = s[:i], s[i:]
	}
	if len(intPart) <= 3 {
		return intPart + frac
	}
	var b strings.Builder
	lead := len(intPart) % 3
	if lead > 0 {
		b.WriteString(intPart[:lead])
	}
	for i := lead; i < len(intPart); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(intPart[i : i+3])
	}
	return b.String() + frac
}
