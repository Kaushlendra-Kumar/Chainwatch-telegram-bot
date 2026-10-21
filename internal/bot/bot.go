package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	tele "gopkg.in/telebot.v3"
	"gopkg.in/telebot.v3/middleware"

	"chainwatch/internal/chain"
	"chainwatch/internal/httpx"
	"chainwatch/internal/price"
	"chainwatch/internal/store"
	"chainwatch/internal/worker"
)

type Refresher interface {
	Refresh()
}

type Options struct {
	Token         string
	APIURL        string
	Webhook       bool
	WebhookURL    string
	WebhookSecret string
	MaxAlerts     int
	MaxWallets    int
}

type Bot struct {
	tb          *tele.Bot
	opts        Options
	store       *store.Store
	prices      *price.Service
	chains      *chain.Registry
	alerts      Refresher
	wallets     Refresher
	log         *slog.Logger
	baseCtx     context.Context
	users       *userCache
	throttle    *rateLimiter
	sendLimiter *httpx.Limiter
	started     atomic.Bool
}

type externalPoller struct{}

func (externalPoller) Poll(_ *tele.Bot, _ chan tele.Update, stop chan struct{}) {
	<-stop
}

type noopRefresher struct{}

func (noopRefresher) Refresh() {}

func New(opts Options, st *store.Store, prices *price.Service, chains *chain.Registry, log *slog.Logger) (*Bot, error) {
	b := &Bot{
		opts:        opts,
		store:       st,
		prices:      prices,
		chains:      chains,
		alerts:      noopRefresher{},
		wallets:     noopRefresher{},
		log:         log.With("component", "bot"),
		baseCtx:     context.Background(),
		users:       newUserCache(15 * time.Minute),
		throttle:    newRateLimiter(1.5, 6),
		sendLimiter: httpx.NewLimiter(25),
	}

	var poller tele.Poller = &tele.LongPoller{Timeout: 10 * time.Second, AllowedUpdates: []string{"message"}}
	if opts.Webhook {
		poller = externalPoller{}
	}
	tb, err := tele.NewBot(tele.Settings{
		URL:       opts.APIURL,
		Token:     opts.Token,
		Poller:    poller,
		ParseMode: tele.ModeHTML,
		Client:    &http.Client{Timeout: 60 * time.Second},
		OnError: func(err error, c tele.Context) {
			b.log.Error("telegram handler error", "err", err)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}
	b.tb = tb
	b.routes()
	return b, nil
}

func (b *Bot) SetRefreshers(alerts, wallets Refresher) {
	b.alerts, b.wallets = alerts, wallets
}

func (b *Bot) Username() string {
	return b.tb.Me.Username
}

func (b *Bot) routes() {
	b.tb.Use(middleware.Recover(func(err error, c tele.Context) {
		b.log.Error("handler panic", "err", err)
	}))
	b.tb.Use(b.privateOnly, b.rateLimit, b.identify)

	b.tb.Handle("/start", b.cmdStart)
	b.tb.Handle("/help", b.cmdHelp)
	b.tb.Handle("/price", b.cmdPrice)
	b.tb.Handle("/top", b.cmdTop)
	b.tb.Handle("/chart", b.cmdChart)
	b.tb.Handle("/alert", b.cmdAlert)
	b.tb.Handle("/alerts", b.cmdAlerts)
	b.tb.Handle("/delalert", b.cmdDelAlert)
	b.tb.Handle("/watch", b.cmdWatch)
	b.tb.Handle("/wallets", b.cmdWallets)
	b.tb.Handle("/unwatch", b.cmdUnwatch)
	b.tb.Handle("/minusd", b.cmdMinUSD)
	b.tb.Handle("/balance", b.cmdBalance)
	b.tb.Handle("/chains", b.cmdChains)
	b.tb.Handle("/add", b.cmdAdd)
	b.tb.Handle("/sell", b.cmdSell)
	b.tb.Handle("/remove", b.cmdRemove)
	b.tb.Handle("/portfolio", b.cmdPortfolio)
	b.tb.Handle(tele.OnText, b.onText)
}

func (b *Bot) Start(ctx context.Context) error {
	b.baseCtx = ctx
	if b.opts.Webhook {
		err := b.tb.SetWebhook(&tele.Webhook{
			Endpoint:       &tele.WebhookEndpoint{PublicURL: b.opts.WebhookURL},
			SecretToken:    b.opts.WebhookSecret,
			AllowedUpdates: []string{"message"},
			MaxConnections: 20,
		})
		if err != nil {
			return fmt.Errorf("set webhook: %w", err)
		}
	} else if err := b.tb.RemoveWebhook(false); err != nil {
		return fmt.Errorf("remove webhook: %w", err)
	}
	if err := b.tb.SetCommands(commandMenu()); err != nil {
		b.log.Warn("set command menu", "err", err)
	}
	b.started.Store(true)
	go b.tb.Start()
	return nil
}

func (b *Bot) Stop() {
	if b.started.CompareAndSwap(true, false) {
		b.tb.Stop()
	}
}

func (b *Bot) Ingest(ctx context.Context, u tele.Update) error {
	select {
	case b.tb.Updates <- u:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return errors.New("update queue full")
	}
}

func (b *Bot) Notify(ctx context.Context, chatID int64, html string) error {
	for attempt := 0; attempt < 2; attempt++ {
		if err := b.sendLimiter.Wait(ctx); err != nil {
			return err
		}
		_, err := b.tb.Send(tele.ChatID(chatID), html, tele.NoPreview)
		if err == nil {
			return nil
		}
		var flood tele.FloodError
		if errors.As(err, &flood) && attempt == 0 {
			wait := time.Duration(min(max(flood.RetryAfter, 1), 30)) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		if errors.Is(err, tele.ErrBlockedByUser) || errors.Is(err, tele.ErrUserIsDeactivated) ||
			errors.Is(err, tele.ErrNotStartedByUser) || errors.Is(err, tele.ErrChatNotFound) {
			return fmt.Errorf("%w: %v", worker.ErrRecipientGone, err)
		}
		return err
	}
	return errors.New("telegram flood limit")
}

func (b *Bot) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(b.baseCtx, 30*time.Second)
}

func (b *Bot) fail(c tele.Context, err error, msg string) error {
	if err != nil {
		b.log.Error("command failed", "cmd", c.Text(), "err", err)
	}
	return c.Send("⚠️ " + msg)
}

func commandMenu() []tele.Command {
	return []tele.Command{
		{Text: "price", Description: "Current price: /price btc eth"},
		{Text: "chart", Description: "Price chart: /chart btc 7d"},
		{Text: "top", Description: "Top 10 coins by market cap"},
		{Text: "alert", Description: "Price alert: /alert btc above 100000"},
		{Text: "alerts", Description: "Your active price alerts"},
		{Text: "watch", Description: "Watch a wallet: /watch eth 0x… label"},
		{Text: "wallets", Description: "Wallets you are watching"},
		{Text: "balance", Description: "Wallet balance: /balance sol <address>"},
		{Text: "portfolio", Description: "Your holdings and P&L"},
		{Text: "add", Description: "Record a buy: /add btc 0.5 60000"},
		{Text: "help", Description: "All commands"},
	}
}
