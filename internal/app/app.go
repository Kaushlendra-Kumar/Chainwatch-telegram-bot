package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"chainwatch/internal/bot"
	"chainwatch/internal/chain"
	"chainwatch/internal/config"
	"chainwatch/internal/price"
	"chainwatch/internal/server"
	"chainwatch/internal/store"
	"chainwatch/internal/worker"
)

func Run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	log := newLogger(cfg)
	slog.SetDefault(log)
	log.Info("starting chainwatch", "env", cfg.Env, "mode", cfg.BotMode)

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	gecko := price.NewCoinGecko(cfg.CoinGeckoURL, cfg.CoinGeckoKey)
	prices := price.NewService(price.NewBinance(cfg.BinanceURL), gecko, gecko, log.With("component", "prices"))
	if cfg.CoinGeckoKey == "" {
		log.Warn("COINGECKO_API_KEY not set: /top and fallback prices use the low public rate limit")
	}

	chains := buildChains(cfg, log)

	tg, err := bot.New(bot.Options{
		Token:         cfg.BotToken,
		APIURL:        cfg.TelegramAPIURL,
		Webhook:       cfg.BotMode == config.ModeWebhook,
		WebhookURL:    cfg.WebhookURL(),
		WebhookSecret: cfg.WebhookSecret,
		MaxAlerts:     cfg.MaxAlerts,
		MaxWallets:    cfg.MaxWallets,
	}, st, prices, chains, log)
	if err != nil {
		return err
	}
	log.Info("telegram bot ready", "username", tg.Username())

	alerts := worker.NewAlertWorker(st, prices, tg, cfg.PricePoll, log)
	wallets := worker.NewWalletWorker(st, chains, prices, tg, cfg.WalletPoll, cfg.NotifyBatchLimit, log)
	janitor := worker.NewJanitor(st, cfg.EventRetention, log)
	tg.SetRefreshers(alerts, wallets)

	srv := server.New(net.JoinHostPort("", cfg.Port), server.Deps{
		DB:            st,
		Prices:        prices,
		Updates:       tg,
		WebhookSecret: cfg.WebhookSecret,
		Webhook:       cfg.BotMode == config.ModeWebhook,
		Production:    cfg.IsProduction(),
		Log:           log.With("component", "http"),
	})

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	serverErr := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	if err := tg.Start(runCtx); err != nil {
		shutdownServer(srv, log)
		return err
	}

	var wg sync.WaitGroup
	for _, run := range []func(context.Context){alerts.Run, wallets.Run, janitor.Run} {
		wg.Add(1)
		go func(fn func(context.Context)) {
			defer wg.Done()
			fn(runCtx)
		}(run)
	}

	select {
	case <-ctx.Done():
		log.Info("shutdown requested")
	case err = <-serverErr:
		log.Error("http server failed", "err", err)
	}

	tg.Stop()
	shutdownServer(srv, log)
	cancel()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		log.Warn("workers did not stop in time")
	}
	log.Info("bye")
	return err
}

func buildChains(cfg *config.Config, log *slog.Logger) *chain.Registry {
	trackers := []chain.Tracker{
		chain.NewBitcoin(cfg.BitcoinAPIURL),
		chain.NewSolana(cfg.SolanaRPCURL, cfg.SolanaRPS),
	}
	if cfg.EtherscanKey == "" {
		log.Warn("ETHERSCAN_API_KEY not set: EVM wallet tracking disabled")
	} else {
		api := chain.NewEtherscan(cfg.EtherscanKey, cfg.EtherscanRPS, cfg.EtherscanDaily)
		for _, id := range cfg.EVMChains {
			net, ok := chain.EVMNetworks[id]
			if !ok {
				log.Warn("unknown EVM chain in EVM_CHAINS", "chain", id)
				continue
			}
			trackers = append(trackers, chain.NewEVM(net, api))
		}
	}
	return chain.NewRegistry(trackers...)
}

func shutdownServer(srv *http.Server, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Warn("http shutdown", "err", err)
	}
}

func newLogger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.IsProduction() {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}
