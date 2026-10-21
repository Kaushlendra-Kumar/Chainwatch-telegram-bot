package bot

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

var errBadNumber = errors.New("invalid number")

func ParseNumber(s string) (float64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "$")
	s = strings.NewReplacer(",", "", "_", "").Replace(s)
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "k"):
		mult, s = 1e3, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mult, s = 1e6, strings.TrimSuffix(s, "m")
	case strings.HasSuffix(s, "b"):
		mult, s = 1e9, strings.TrimSuffix(s, "b")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0, errBadNumber
	}
	return v * mult, nil
}

func ParseDirection(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "above", ">", ">=", "over", "up", "gt":
		return "above", true
	case "below", "<", "<=", "under", "down", "lt":
		return "below", true
	default:
		return "", false
	}
}

func ParseID(s string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(s), "#"), 10, 64)
	return id, err == nil && id > 0
}

func ParseDays(s string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "7d", "1w", "week":
		return 7, true
	case "1d", "24h", "day":
		return 1, true
	case "30d", "1m", "month":
		return 30, true
	case "90d", "3m":
		return 90, true
	case "1y", "365d", "year":
		return 365, true
	default:
		return 0, false
	}
}
