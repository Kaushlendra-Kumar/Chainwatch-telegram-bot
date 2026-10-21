package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"chainwatch/internal/format"
	"chainwatch/internal/price"
	"chainwatch/internal/store"
)

var ErrRecipientGone = errors.New("recipient unavailable")

type Notifier interface {
	Notify(ctx context.Context, chatID int64, html string) error
}

type AlertStore interface {
	ActiveAlerts(ctx context.Context) ([]store.Alert, error)
	ClaimAlert(ctx context.Context, alertID int64, price float64) (bool, error)
	ReleaseAlert(ctx context.Context, alertID int64) error
	DeactivateChat(ctx context.Context, chatID int64) error
}

type PriceSource interface {
	Quotes(ctx context.Context, symbols []string, withChange bool) (map[string]price.Quote, error)
}

type AlertWorker struct {
	store    AlertStore
	prices   PriceSource
	notifier Notifier
	interval time.Duration
	log      *slog.Logger
	refresh  chan struct{}
	alerts   []store.Alert
	stale    bool
	now      func() time.Time
}

func NewAlertWorker(st AlertStore, prices PriceSource, notifier Notifier, interval time.Duration, log *slog.Logger) *AlertWorker {
	return &AlertWorker{
		store:    st,
		prices:   prices,
		notifier: notifier,
		interval: interval,
		log:      log.With("worker", "alerts"),
		refresh:  make(chan struct{}, 1),
		stale:    true,
		now:      time.Now,
	}
}

func (w *AlertWorker) Refresh() {
	select {
	case w.refresh <- struct{}{}:
	default:
	}
}

func (w *AlertWorker) Run(ctx context.Context) {
	w.reload(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.refresh:
			w.stale = true
			w.reload(ctx)
		case <-ticker.C:
			if w.stale {
				w.reload(ctx)
			}
			w.check(ctx)
		}
	}
}

func (w *AlertWorker) reload(ctx context.Context) {
	if !w.stale {
		return
	}
	loadCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	alerts, err := w.store.ActiveAlerts(loadCtx)
	if err != nil {
		w.log.Error("load alerts", "err", err)
		return
	}
	w.alerts, w.stale = alerts, false
	w.log.Debug("alerts loaded", "count", len(alerts))
}

func (w *AlertWorker) check(ctx context.Context) {
	if len(w.alerts) == 0 {
		return
	}
	symbols := make([]string, 0, len(w.alerts))
	for _, a := range w.alerts {
		symbols = append(symbols, a.Symbol)
	}
	checkCtx, cancel := context.WithTimeout(ctx, w.interval)
	defer cancel()
	quotes, err := w.prices.Quotes(checkCtx, symbols, false)
	if err != nil {
		w.log.Warn("fetch prices", "err", err)
		return
	}

	keep := w.alerts[:0:0]
	for _, a := range w.alerts {
		q, ok := quotes[a.Symbol]
		if !ok || !Triggered(a.Direction, a.Target, q.Price) {
			keep = append(keep, a)
			continue
		}
		if w.fire(checkCtx, a, q.Price) {
			continue
		}
		keep = append(keep, a)
	}
	w.alerts = keep
}

func (w *AlertWorker) fire(ctx context.Context, a store.Alert, current float64) bool {
	claimed, err := w.store.ClaimAlert(ctx, a.ID, current)
	if err != nil {
		w.log.Error("claim alert", "alert", a.ID, "err", err)
		return false
	}
	if !claimed {
		return true
	}
	err = w.notifier.Notify(ctx, a.ChatID, AlertMessage(a, current, w.now()))
	switch {
	case err == nil:
		w.log.Info("alert fired", "alert", a.ID, "symbol", a.Symbol, "price", current)
		return true
	case errors.Is(err, ErrRecipientGone):
		_ = w.store.DeactivateChat(ctx, a.ChatID)
		w.stale = true
		return true
	default:
		w.log.Warn("send alert failed, will retry", "alert", a.ID, "err", err)
		if rerr := w.store.ReleaseAlert(ctx, a.ID); rerr != nil {
			w.log.Error("release alert", "alert", a.ID, "err", rerr)
			return true
		}
		return false
	}
}

func Triggered(direction string, target, current float64) bool {
	if current <= 0 {
		return false
	}
	switch direction {
	case "above":
		return current >= target
	case "below":
		return current <= target
	default:
		return false
	}
}

func AlertMessage(a store.Alert, current float64, now time.Time) string {
	arrow := "📈"
	if a.Direction == "below" {
		arrow = "📉"
	}
	return fmt.Sprintf(
		"🚨 <b>Price alert · %s</b>\n\n%s %s is now <b>%s</b> %s\n\nCurrent price: <b>%s</b>\nAlert #%d · set %s\n\n<i>This alert is now complete. Create a new one with /alert.</i>",
		format.Escape(a.Symbol), arrow, format.Escape(a.Symbol), a.Direction, format.USD(a.Target),
		format.USD(current), a.ID, format.Ago(a.CreatedAt, now),
	)
}
