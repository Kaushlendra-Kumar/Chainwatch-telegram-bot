package config

import (
	"strings"
	"testing"
)

func TestLoadDefaultsAndValidation(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost/db")
	t.Setenv("BOT_MODE", "polling")
	t.Setenv("RENDER_EXTERNAL_URL", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.PricePoll.Seconds() != 30 || len(c.EVMChains) != 3 || c.BinanceURL != "https://data-api.binance.vision" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if len(c.WebhookSecret) != 64 || strings.ContainsAny(c.WebhookSecret, "+/=") {
		t.Fatalf("webhook secret must be telegram-safe hex: %q", c.WebhookSecret)
	}

	t.Setenv("BOT_MODE", "webhook")
	t.Setenv("PUBLIC_URL", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PUBLIC_URL") {
		t.Fatalf("webhook mode without url should fail, got %v", err)
	}

	t.Setenv("RENDER_EXTERNAL_URL", "https://chainwatch.onrender.com/")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.WebhookURL() != "https://chainwatch.onrender.com/telegram/webhook" {
		t.Fatalf("webhook url = %s", c.WebhookURL())
	}

	t.Setenv("PRICE_POLL_INTERVAL", "2s")
	if _, err := Load(); err == nil {
		t.Fatal("too-fast poll interval should fail")
	}
}
