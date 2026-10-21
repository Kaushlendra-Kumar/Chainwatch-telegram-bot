package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"chainwatch/internal/chain"
	"chainwatch/internal/format"
	"chainwatch/internal/price"
	"chainwatch/internal/store"
)

type WalletStore interface {
	ActiveSubscriptions(ctx context.Context) ([]store.Subscription, error)
	SaveEvents(ctx context.Context, walletID int64, events []chain.Event, cursor string) (int, error)
	ClaimPendingEvents(ctx context.Context, since time.Time, limit int) ([]store.StoredEvent, error)
	DeactivateChat(ctx context.Context, chatID int64) error
}

type ChainLookup interface {
	Get(id string) (chain.Tracker, bool)
}

type EventView struct {
	chain.Event
	USD   float64
	Known bool
}

type trackedWallet struct {
	id      int64
	chain   string
	address string
	cursor  string
	subs    []store.Subscription
}

type WalletWorker struct {
	store       WalletStore
	chains      ChainLookup
	prices      PriceSource
	notifier    Notifier
	interval    time.Duration
	batchLimit  int
	concurrency int
	log         *slog.Logger
	refresh     chan struct{}
	wallets     map[int64]*trackedWallet
	stale       bool
	pending     bool
	now         func() time.Time
}

func NewWalletWorker(st WalletStore, chains ChainLookup, prices PriceSource, notifier Notifier, interval time.Duration, batchLimit int, log *slog.Logger) *WalletWorker {
	return &WalletWorker{
		store:       st,
		chains:      chains,
		prices:      prices,
		notifier:    notifier,
		interval:    interval,
		batchLimit:  batchLimit,
		concurrency: 4,
		log:         log.With("worker", "wallets"),
		refresh:     make(chan struct{}, 1),
		wallets:     map[int64]*trackedWallet{},
		stale:       true,
		pending:     true,
		now:         time.Now,
	}
}

func (w *WalletWorker) Refresh() {
	select {
	case w.refresh <- struct{}{}:
	default:
	}
}

func (w *WalletWorker) Run(ctx context.Context) {
	w.reload(ctx)
	w.dispatch(ctx)
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
			w.poll(ctx)
			if w.pending {
				w.dispatch(ctx)
			}
		}
	}
}

func (w *WalletWorker) reload(ctx context.Context) {
	if !w.stale {
		return
	}
	loadCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	subs, err := w.store.ActiveSubscriptions(loadCtx)
	if err != nil {
		w.log.Error("load subscriptions", "err", err)
		return
	}
	next := map[int64]*trackedWallet{}
	for _, s := range subs {
		tw, ok := next[s.WalletID]
		if !ok {
			tw = &trackedWallet{id: s.WalletID, chain: s.Chain, address: s.Address, cursor: s.Cursor}
			if old, exists := w.wallets[s.WalletID]; exists && old.cursor != "" {
				tw.cursor = old.cursor
			}
			next[s.WalletID] = tw
		}
		tw.subs = append(tw.subs, s)
	}
	w.wallets, w.stale = next, false
	w.log.Debug("wallets loaded", "wallets", len(next), "subscriptions", len(subs))
}

func (w *WalletWorker) poll(ctx context.Context) {
	if len(w.wallets) == 0 {
		return
	}
	sem := make(chan struct{}, w.concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	inserted := 0

	for _, tw := range w.wallets {
		tracker, ok := w.chains.Get(tw.chain)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(tw *trackedWallet, tracker chain.Tracker) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			pollCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			events, cursor, err := tracker.Poll(pollCtx, tw.address, tw.cursor)
			if err != nil {
				level := slog.LevelWarn
				if errors.Is(err, chain.ErrBudgetExhausted) || errors.Is(err, context.Canceled) {
					level = slog.LevelDebug
				}
				w.log.Log(ctx, level, "poll wallet", "chain", tw.chain, "wallet", tw.id, "err", err)
				return
			}
			if len(events) == 0 && cursor == tw.cursor {
				return
			}
			n, err := w.store.SaveEvents(pollCtx, tw.id, events, cursor)
			if err != nil {
				w.log.Error("save events", "wallet", tw.id, "err", err)
				return
			}
			tw.cursor = cursor
			if n > 0 {
				mu.Lock()
				inserted += n
				mu.Unlock()
			}
		}(tw, tracker)
	}
	wg.Wait()
	if inserted > 0 {
		w.log.Info("new wallet events", "count", inserted)
		w.pending = true
	}
}

func (w *WalletWorker) dispatch(ctx context.Context) {
	const pageSize = 200
	for round := 0; round < 5; round++ {
		claimCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		events, err := w.store.ClaimPendingEvents(claimCtx, w.now().Add(-6*time.Hour), pageSize)
		cancel()
		if err != nil {
			w.log.Error("claim pending events", "err", err)
			return
		}
		if len(events) > 0 {
			w.deliver(ctx, events)
		}
		if len(events) < pageSize {
			w.pending = false
			return
		}
	}
}

func (w *WalletWorker) deliver(ctx context.Context, events []store.StoredEvent) {
	var symbols []string
	for _, e := range events {
		if e.Verified {
			symbols = append(symbols, e.Symbol)
		}
	}
	quotes := map[string]price.Quote{}
	if len(symbols) > 0 {
		qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		if q, err := w.prices.Quotes(qctx, symbols, false); err == nil {
			quotes = q
		}
		cancel()
	}

	byWallet := map[int64][]EventView{}
	var order []int64
	for _, e := range events {
		if _, ok := byWallet[e.WalletID]; !ok {
			order = append(order, e.WalletID)
		}
		byWallet[e.WalletID] = append(byWallet[e.WalletID], Value(e.Event, quotes))
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	for _, walletID := range order {
		tw, ok := w.wallets[walletID]
		if !ok {
			continue
		}
		tracker, ok := w.chains.Get(tw.chain)
		if !ok {
			continue
		}
		for _, sub := range tw.subs {
			var items []EventView
			for _, v := range byWallet[walletID] {
				if Passes(sub.MinUSD, v) {
					items = append(items, v)
				}
			}
			if len(items) == 0 {
				continue
			}
			sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := w.notifier.Notify(sendCtx, sub.ChatID, WalletMessage(tracker, sub, items, w.batchLimit))
			cancel()
			if errors.Is(err, ErrRecipientGone) {
				_ = w.store.DeactivateChat(ctx, sub.ChatID)
				w.stale = true
			} else if err != nil {
				w.log.Warn("send wallet notification", "subscription", sub.ID, "err", err)
			}
		}
	}
}

func Value(e chain.Event, quotes map[string]price.Quote) EventView {
	v := EventView{Event: e}
	if !e.Verified {
		return v
	}
	if q, ok := quotes[price.Normalize(e.Symbol)]; ok && q.Price > 0 {
		v.USD, v.Known = e.Amount*q.Price, true
	}
	return v
}

func Passes(minUSD float64, v EventView) bool {
	if minUSD <= 0 {
		return true
	}
	return v.Known && v.USD >= minUSD
}

func WalletMessage(tracker chain.Tracker, sub store.Subscription, items []EventView, limit int) string {
	var b strings.Builder
	title := sub.Label
	if title == "" {
		title = format.ShortAddr(sub.Address)
	}
	fmt.Fprintf(&b, "🔔 <b>Wallet activity · %s</b>\n%s · <a href=\"%s\">%s</a>\n",
		format.Escape(title), tracker.Name(), tracker.AddressURL(sub.Address), format.Escape(format.ShortAddr(sub.Address)))

	shown := items
	if limit > 0 && len(items) > limit {
		shown = items[len(items)-limit:]
	}
	for _, v := range shown {
		b.WriteString("\n")
		b.WriteString(eventLine(tracker, v))
	}
	if hidden := len(items) - len(shown); hidden > 0 {
		fmt.Fprintf(&b, "\n…and %d earlier transfer(s). Use /minusd %d &lt;amount&gt; to filter small ones.", hidden, sub.ID)
	}
	return b.String()
}

func eventLine(tracker chain.Tracker, v EventView) string {
	icon, verb, prep := "📥", "Received", "from"
	switch v.Direction {
	case chain.Out:
		icon, verb, prep = "📤", "Sent", "to"
	case chain.Self:
		icon, verb, prep = "🔁", "Self-transfer", ""
	}
	line := fmt.Sprintf("%s <b>%s %s %s</b>", icon, verb, format.Amount(v.Amount), format.Escape(v.Symbol))
	if v.Known {
		line += " (≈ " + format.USD(v.USD) + ")"
	}
	if !v.Verified {
		line += " ⚠️ <i>unverified token</i>"
	}
	line += "\n"
	if prep != "" && v.Counterparty != "" {
		line += prep + " <code>" + format.Escape(format.ShortAddr(v.Counterparty)) + "</code> · "
	}
	line += fmt.Sprintf("<a href=\"%s\">view tx</a>", tracker.TxURL(v.TxHash))
	return line
}
