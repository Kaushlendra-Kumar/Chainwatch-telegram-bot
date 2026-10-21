package worker

import (
	"context"
	"log/slog"
	"time"

	"chainwatch/internal/store"
)

type Cleaner interface {
	Cleanup(ctx context.Context, retention time.Duration) (store.CleanupResult, error)
}

type Janitor struct {
	store     Cleaner
	retention time.Duration
	every     time.Duration
	log       *slog.Logger
}

func NewJanitor(st Cleaner, retention time.Duration, log *slog.Logger) *Janitor {
	return &Janitor{store: st, retention: retention, every: 24 * time.Hour, log: log.With("worker", "janitor")}
}

func (j *Janitor) Run(ctx context.Context) {
	first := time.NewTimer(2 * time.Minute)
	defer first.Stop()
	select {
	case <-ctx.Done():
		return
	case <-first.C:
	}
	j.once(ctx)
	ticker := time.NewTicker(j.every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j.once(ctx)
		}
	}
}

func (j *Janitor) once(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	res, err := j.store.Cleanup(cctx, j.retention)
	if err != nil {
		j.log.Error("cleanup", "err", err)
		return
	}
	j.log.Info("cleanup done", "events", res.Events, "alerts", res.Alerts, "wallets", res.Wallets)
}
