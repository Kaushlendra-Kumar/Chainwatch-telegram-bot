package httpx

import (
	"context"
	"sync"
	"time"
)

type Limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func NewLimiter(perSecond float64) *Limiter {
	if perSecond <= 0 {
		return nil
	}
	return &Limiter{interval: time.Duration(float64(time.Second) / perSecond)}
}

func (l *Limiter) Wait(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	wait := l.next.Sub(now)
	l.next = l.next.Add(l.interval)
	l.mu.Unlock()

	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type Budget struct {
	mu    sync.Mutex
	limit int
	day   string
	used  int
	now   func() time.Time
}

func NewBudget(perDay int) *Budget {
	return &Budget{limit: perDay, now: time.Now}
}

func (b *Budget) Take() bool {
	if b == nil || b.limit <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	day := b.now().UTC().Format("2006-01-02")
	if day != b.day {
		b.day = day
		b.used = 0
	}
	if b.used >= b.limit {
		return false
	}
	b.used++
	return true
}

func (b *Budget) Used() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}
