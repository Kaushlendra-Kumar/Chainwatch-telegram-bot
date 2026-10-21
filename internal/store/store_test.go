package store

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"chainwatch/internal/chain"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	_, err = s.pool.Exec(ctx, `DROP TABLE IF EXISTS holdings, wallet_events, wallet_subscriptions, wallets, price_alerts, users, schema_migrations CASCADE`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("migrate run %d: %v", i, err)
		}
	}
	return s
}

func TestStoreIntegration(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	u, err := s.UpsertUser(ctx, 1001, 1001, "alice", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	u2, err := s.UpsertUser(ctx, 1001, 1001, "alice2", "Alice")
	if err != nil || u2.ID != u.ID {
		t.Fatalf("upsert should keep id: %v %v", u2, err)
	}

	t.Run("alerts", func(t *testing.T) {
		a, err := s.CreateAlert(ctx, u.ID, "BTC", "above", 100000)
		if err != nil {
			t.Fatal(err)
		}
		active, err := s.ActiveAlerts(ctx)
		if err != nil || len(active) != 1 || active[0].ChatID != 1001 {
			t.Fatalf("active alerts: %+v %v", active, err)
		}
		if ok, _ := s.ClaimAlert(ctx, a.ID, 100500); !ok {
			t.Fatal("first claim should win")
		}
		if ok, _ := s.ClaimAlert(ctx, a.ID, 100500); ok {
			t.Fatal("second claim must lose")
		}
		if err := s.ReleaseAlert(ctx, a.ID); err != nil {
			t.Fatal(err)
		}
		if n, _ := s.CountActiveAlerts(ctx, u.ID); n != 1 {
			t.Fatalf("released alert should be active again, count=%d", n)
		}
		if ok, _ := s.DeleteAlert(ctx, u.ID, a.ID); !ok {
			t.Fatal("delete should succeed")
		}
		if err := s.DeactivateChat(ctx, 1001); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateAlert(ctx, u.ID, "ETH", "below", 1000); err != nil {
			t.Fatal(err)
		}
		if active, _ := s.ActiveAlerts(ctx); len(active) != 0 {
			t.Fatal("inactive users must not have active alerts loaded")
		}
		_, _ = s.UpsertUser(ctx, 1001, 1001, "alice", "Alice")
	})

	t.Run("wallets", func(t *testing.T) {
		wid, err := s.CreateWallet(ctx, "eth", "0xabc", "100")
		if err != nil {
			t.Fatal(err)
		}
		again, err := s.CreateWallet(ctx, "eth", "0xabc", "999")
		if err != nil || again != wid {
			t.Fatalf("create wallet should be idempotent: %d %v", again, err)
		}
		if id, found, err := s.FindWallet(ctx, "eth", "0xabc"); err != nil || !found || id != wid {
			t.Fatalf("find wallet: %d %v %v", id, found, err)
		}
		if _, found, err := s.FindWallet(ctx, "eth", "0xnope"); err != nil || found {
			t.Fatalf("missing wallet: %v %v", found, err)
		}

		sub, created, err := s.Subscribe(ctx, u.ID, wid, "Whale")
		if err != nil || !created {
			t.Fatalf("subscribe: %v %v", created, err)
		}
		sub2, created, err := s.Subscribe(ctx, u.ID, wid, "")
		if err != nil || created || sub2 != sub {
			t.Fatalf("resubscribe: %d %v %v", sub2, created, err)
		}
		subs, err := s.UserSubscriptions(ctx, u.ID)
		if err != nil || len(subs) != 1 || subs[0].Label != "Whale" || subs[0].Cursor != "100" {
			t.Fatalf("subscriptions: %+v %v", subs, err)
		}
		if ok, _ := s.SetMinUSD(ctx, u.ID, sub, 2500); !ok {
			t.Fatal("set min usd")
		}

		events := []chain.Event{
			{TxHash: "0x1", Key: "native", Direction: chain.In, Amount: 1.5, Symbol: "ETH", Verified: true, Block: 101, Time: time.Now()},
			{TxHash: "0x2", Key: "erc20:x", Direction: chain.Out, Amount: 50, Symbol: "USDC", Token: "0xa0b8", Verified: true, Block: 102},
		}
		n, err := s.SaveEvents(ctx, wid, events, "102")
		if err != nil || n != 2 {
			t.Fatalf("save events: %d %v", n, err)
		}
		n, err = s.SaveEvents(ctx, wid, events, "102")
		if err != nil || n != 0 {
			t.Fatalf("duplicate events must be ignored: %d %v", n, err)
		}
		active, err := s.ActiveSubscriptions(ctx)
		if err != nil || len(active) != 1 || active[0].Cursor != "102" || active[0].MinUSD != 2500 {
			t.Fatalf("active subscriptions: %+v %v", active, err)
		}

		claimed, err := s.ClaimPendingEvents(ctx, time.Now().Add(-time.Hour), 100)
		if err != nil || len(claimed) != 2 || claimed[0].TxHash != "0x1" || claimed[1].Direction != chain.Out {
			t.Fatalf("claim pending: %+v %v", claimed, err)
		}
		claimed, err = s.ClaimPendingEvents(ctx, time.Now().Add(-time.Hour), 100)
		if err != nil || len(claimed) != 0 {
			t.Fatalf("events must be claimed only once: %+v %v", claimed, err)
		}

		if ok, _ := s.Unsubscribe(ctx, u.ID, sub); !ok {
			t.Fatal("unsubscribe")
		}
		_, err = s.pool.Exec(ctx, `UPDATE wallets SET created_at = now() - interval '2 hours'`)
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.Cleanup(ctx, 24*time.Hour)
		if err != nil || res.Wallets != 1 {
			t.Fatalf("orphan wallet should be removed: %+v %v", res, err)
		}
	})

	t.Run("holdings", func(t *testing.T) {
		if _, err := s.AddHolding(ctx, u.ID, "BTC", 1, 60000); err != nil {
			t.Fatal(err)
		}
		h, err := s.AddHolding(ctx, u.ID, "BTC", 1, 40000)
		if err != nil || h.Quantity != 2 || h.AvgCost != 50000 {
			t.Fatalf("average cost: %+v %v", h, err)
		}
		res, err := s.SellHolding(ctx, u.ID, "BTC", 0.5)
		if err != nil || res.Sold != 0.5 || math.Abs(res.Remaining-1.5) > 1e-12 || res.AvgCost != 50000 {
			t.Fatalf("partial sell: %+v %v", res, err)
		}
		res, err = s.SellHolding(ctx, u.ID, "BTC", 10)
		if err != nil || res.Sold != 1.5 || res.Remaining != 0 {
			t.Fatalf("full sell: %+v %v", res, err)
		}
		if _, err := s.SellHolding(ctx, u.ID, "BTC", 1); err != ErrNotFound {
			t.Fatalf("selling a closed position: %v", err)
		}
		_, _ = s.AddHolding(ctx, u.ID, "ETH", 3, 2000)
		list, err := s.Holdings(ctx, u.ID)
		if err != nil || len(list) != 1 || list[0].Symbol != "ETH" {
			t.Fatalf("holdings: %+v %v", list, err)
		}
		if ok, _ := s.RemoveHolding(ctx, u.ID, "ETH"); !ok {
			t.Fatal("remove")
		}
	})
}
