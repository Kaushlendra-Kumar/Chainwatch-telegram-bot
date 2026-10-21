package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const (
	ModePolling = "polling"
	ModeWebhook = "webhook"
	WebhookPath = "/telegram/webhook"
)

type Config struct {
	Env              string
	LogLevel         slog.Level
	Port             string
	BotToken         string
	TelegramAPIURL   string
	BotMode          string
	PublicURL        string
	WebhookSecret    string
	DatabaseURL      string
	EtherscanKey     string
	EtherscanRPS     float64
	EtherscanDaily   int
	EVMChains        []string
	CoinGeckoKey     string
	BinanceURL       string
	CoinGeckoURL     string
	BitcoinAPIURL    string
	SolanaRPCURL     string
	SolanaRPS        float64
	PricePoll        time.Duration
	WalletPoll       time.Duration
	MaxAlerts        int
	MaxWallets       int
	EventRetention   time.Duration
	NotifyBatchLimit int
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	c := &Config{
		Env:            str("APP_ENV", "development"),
		Port:           str("PORT", "8080"),
		BotToken:       str("TELEGRAM_BOT_TOKEN", ""),
		TelegramAPIURL: strings.TrimRight(str("TELEGRAM_API_URL", ""), "/"),
		BotMode:        strings.ToLower(str("BOT_MODE", ModePolling)),
		PublicURL:      strings.TrimRight(str("PUBLIC_URL", os.Getenv("RENDER_EXTERNAL_URL")), "/"),
		DatabaseURL:    str("DATABASE_URL", ""),
		EtherscanKey:   str("ETHERSCAN_API_KEY", ""),
		CoinGeckoKey:   str("COINGECKO_API_KEY", ""),
		BinanceURL:     strings.TrimRight(str("BINANCE_BASE_URL", "https://data-api.binance.vision"), "/"),
		CoinGeckoURL:   strings.TrimRight(str("COINGECKO_BASE_URL", "https://api.coingecko.com/api/v3"), "/"),
		BitcoinAPIURL:  strings.TrimRight(str("BITCOIN_API_URL", "https://mempool.space/api"), "/"),
		SolanaRPCURL:   str("SOLANA_RPC_URL", "https://api.mainnet-beta.solana.com"),
		EVMChains:      list("EVM_CHAINS", "eth,polygon,arbitrum"),
	}

	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	var err error
	c.LogLevel, err = level(str("LOG_LEVEL", "info"))
	collect(err)
	c.EtherscanRPS, err = float("ETHERSCAN_RPS", 2.5)
	collect(err)
	c.EtherscanDaily, err = integer("ETHERSCAN_DAILY_BUDGET", 90000)
	collect(err)
	c.SolanaRPS, err = float("SOLANA_RPS", 4)
	collect(err)
	c.PricePoll, err = duration("PRICE_POLL_INTERVAL", 30*time.Second)
	collect(err)
	c.WalletPoll, err = duration("WALLET_POLL_INTERVAL", 60*time.Second)
	collect(err)
	c.MaxAlerts, err = integer("MAX_ALERTS_PER_USER", 20)
	collect(err)
	c.MaxWallets, err = integer("MAX_WALLETS_PER_USER", 10)
	collect(err)
	c.EventRetention, err = duration("EVENT_RETENTION", 14*24*time.Hour)
	collect(err)
	c.NotifyBatchLimit, err = integer("NOTIFY_BATCH_LIMIT", 5)
	collect(err)

	if c.BotToken == "" {
		errs = append(errs, errors.New("TELEGRAM_BOT_TOKEN is required"))
	}
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	switch c.BotMode {
	case ModePolling:
	case ModeWebhook:
		if c.PublicURL == "" {
			errs = append(errs, errors.New("PUBLIC_URL (or RENDER_EXTERNAL_URL) is required in webhook mode"))
		} else if !strings.HasPrefix(c.PublicURL, "https://") {
			errs = append(errs, errors.New("PUBLIC_URL must start with https://"))
		}
	default:
		errs = append(errs, fmt.Errorf("BOT_MODE must be %q or %q", ModePolling, ModeWebhook))
	}
	if c.PricePoll < 10*time.Second {
		errs = append(errs, errors.New("PRICE_POLL_INTERVAL must be at least 10s"))
	}
	if c.WalletPoll < 15*time.Second {
		errs = append(errs, errors.New("WALLET_POLL_INTERVAL must be at least 15s"))
	}
	if c.NotifyBatchLimit < 1 {
		c.NotifyBatchLimit = 1
	}

	seed := str("WEBHOOK_SECRET", "")
	if seed == "" {
		seed = c.BotToken
	}
	sum := sha256.Sum256([]byte("chainwatch:" + seed))
	c.WebhookSecret = hex.EncodeToString(sum[:])

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}

func (c *Config) IsProduction() bool {
	return c.Env == "production"
}

func (c *Config) WebhookURL() string {
	return c.PublicURL + WebhookPath
}

func str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func list(key, def string) []string {
	var out []string
	for _, p := range strings.Split(str(key, def), ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func integer(key string, def int) (int, error) {
	v := str(key, "")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", key)
	}
	return n, nil
}

func float(key string, def float64) (float64, error) {
	v := str(key, "")
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("%s must be a positive number", key)
	}
	return f, nil
}

func duration(key string, def time.Duration) (time.Duration, error) {
	v := str(key, "")
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration like 30s or 5m", key)
	}
	return d, nil
}

func level(v string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(v)); err != nil {
		return slog.LevelInfo, fmt.Errorf("LOG_LEVEL %q is invalid", v)
	}
	return l, nil
}
