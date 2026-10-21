package store

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"chainwatch/internal/chain"
)

type Subscription struct {
	ID        int64
	UserID    int64
	ChatID    int64
	WalletID  int64
	Chain     string
	Address   string
	Cursor    string
	Label     string
	MinUSD    float64
	CreatedAt time.Time
}

type StoredEvent struct {
	ID       int64
	WalletID int64
	chain.Event
}

func (s *Store) FindWallet(ctx context.Context, chainID, address string) (int64, bool, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT id FROM wallets WHERE chain = $1 AND address = $2`, chainID, address).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

func (s *Store) CreateWallet(ctx context.Context, chainID, address, cursor string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO wallets (chain, address, poll_cursor)
		VALUES ($1, $2, $3)
		ON CONFLICT (chain, address) DO UPDATE SET chain = EXCLUDED.chain
		RETURNING id`, chainID, address, cursor).Scan(&id)
	return id, err
}

func (s *Store) CountSubscriptions(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM wallet_subscriptions WHERE user_id = $1`, userID).Scan(&n)
	return n, err
}

func (s *Store) Subscribe(ctx context.Context, userID, walletID int64, label string) (int64, bool, error) {
	var id int64
	var created bool
	err := s.pool.QueryRow(ctx, `
		INSERT INTO wallet_subscriptions (user_id, wallet_id, label)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, wallet_id) DO UPDATE
		SET label = CASE WHEN EXCLUDED.label <> '' THEN EXCLUDED.label ELSE wallet_subscriptions.label END
		RETURNING id, (xmax = 0)`, userID, walletID, label).Scan(&id, &created)
	return id, created, err
}

const subscriptionColumns = `
	s.id, s.user_id, u.chat_id, s.wallet_id, w.chain, w.address, w.poll_cursor, s.label, s.min_usd, s.created_at`

func scanSubscription(r pgx.CollectableRow) (Subscription, error) {
	var sub Subscription
	err := r.Scan(&sub.ID, &sub.UserID, &sub.ChatID, &sub.WalletID, &sub.Chain, &sub.Address, &sub.Cursor, &sub.Label, &sub.MinUSD, &sub.CreatedAt)
	return sub, err
}

func (s *Store) UserSubscriptions(ctx context.Context, userID int64) ([]Subscription, error) {
	rows, err := s.pool.Query(ctx, `SELECT`+subscriptionColumns+`
		FROM wallet_subscriptions s
		JOIN wallets w ON w.id = s.wallet_id
		JOIN users u ON u.id = s.user_id
		WHERE s.user_id = $1
		ORDER BY s.id`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanSubscription)
}

func (s *Store) ActiveSubscriptions(ctx context.Context) ([]Subscription, error) {
	rows, err := s.pool.Query(ctx, `SELECT`+subscriptionColumns+`
		FROM wallet_subscriptions s
		JOIN wallets w ON w.id = s.wallet_id
		JOIN users u ON u.id = s.user_id
		WHERE u.active
		ORDER BY s.wallet_id, s.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanSubscription)
}

func (s *Store) Unsubscribe(ctx context.Context, userID, subID int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM wallet_subscriptions WHERE id = $1 AND user_id = $2`, subID, userID)
	return tag.RowsAffected() > 0, err
}

func (s *Store) SetMinUSD(ctx context.Context, userID, subID int64, minUSD float64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE wallet_subscriptions SET min_usd = $3 WHERE id = $1 AND user_id = $2`, subID, userID, minUSD)
	return tag.RowsAffected() > 0, err
}

func (s *Store) SaveEvents(ctx context.Context, walletID int64, events []chain.Event, cursor string) (int, error) {
	inserted := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if len(events) > 0 {
			batch := &pgx.Batch{}
			for _, e := range events {
				occurred := e.Time
				if occurred.IsZero() {
					occurred = time.Now().UTC()
				}
				batch.Queue(`
					INSERT INTO wallet_events
						(wallet_id, tx_hash, event_key, direction, counterparty, amount, symbol, token, verified, block_number, occurred_at)
					VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
					ON CONFLICT (wallet_id, tx_hash, event_key) DO NOTHING`,
					walletID, e.TxHash, e.Key, string(e.Direction), e.Counterparty, e.Amount, e.Symbol, e.Token, e.Verified, e.Block, occurred)
			}
			results := tx.SendBatch(ctx, batch)
			for range events {
				tag, err := results.Exec()
				if err != nil {
					results.Close()
					return err
				}
				inserted += int(tag.RowsAffected())
			}
			if err := results.Close(); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE wallets SET poll_cursor = $2, last_polled_at = now() WHERE id = $1`, walletID, cursor)
		return err
	})
	return inserted, err
}

func (s *Store) ClaimPendingEvents(ctx context.Context, since time.Time, limit int) ([]StoredEvent, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE wallet_events
		SET notified_at = now()
		WHERE id IN (
			SELECT id FROM wallet_events
			WHERE notified_at IS NULL AND created_at >= $1
			ORDER BY id
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, wallet_id, tx_hash, event_key, direction, counterparty, amount, symbol, token, verified, block_number, occurred_at`,
		since, limit)
	if err != nil {
		return nil, err
	}
	events, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (StoredEvent, error) {
		var e StoredEvent
		var dir string
		err := r.Scan(&e.ID, &e.WalletID, &e.TxHash, &e.Key, &dir, &e.Counterparty, &e.Amount, &e.Symbol, &e.Token, &e.Verified, &e.Block, &e.Time)
		e.Direction = chain.Direction(dir)
		return e, err
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(events, func(a, b StoredEvent) int { return cmp.Compare(a.ID, b.ID) })
	return events, nil
}
