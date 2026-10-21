package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tele "gopkg.in/telebot.v3"
)

type okDB struct{}

func (okDB) Ping(context.Context) error { return nil }

type sink struct{ got []tele.Update }

func (s *sink) Ingest(_ context.Context, u tele.Update) error {
	s.got = append(s.got, u)
	return nil
}

func TestWebhookRequiresSecret(t *testing.T) {
	s := &sink{}
	srv := New(":0", Deps{DB: okDB{}, Updates: s, WebhookSecret: "secret", Webhook: true, Production: true,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})

	body := `{"update_id": 42, "message": {"message_id": 1, "text": "/price btc", "chat": {"id": 7, "type": "private"}}}`
	do := func(secret string) int {
		req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if secret != "" {
			req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
		}
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := do(""); code != http.StatusUnauthorized {
		t.Fatalf("missing secret: %d", code)
	}
	if code := do("wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong secret: %d", code)
	}
	if code := do("secret"); code != http.StatusOK {
		t.Fatalf("valid secret: %d", code)
	}
	if len(s.got) != 1 || s.got[0].ID != 42 || s.got[0].Message.Text != "/price btc" {
		t.Fatalf("update not ingested: %+v", s.got)
	}

	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz: %d", rec.Code)
	}
}
