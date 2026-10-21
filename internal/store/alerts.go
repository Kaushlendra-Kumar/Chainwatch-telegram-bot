package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type User struct {
	ID         int64
	TelegramID int64
	ChatID     int64
	Username   string
	FirstName  string
}

type Alert struct {
	ID        int64
	UserID    int64
	ChatID    int64
	Symbol    string
	Direction string
	Target    float64
	CreatedAt time.Time
}

func (s *Store) UpsertUser(ctx context.Context, telegramID, chatID int64, username, firstName string) (User, error) {
	u := User{TelegramID: telegramID, ChatID: chatID, Username: username, FirstName: firstName}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (telegram_id, chat_id, username, first_name)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (telegram_id) DO UPDATE
		SET chat_id = EXCLUDED.chat_id,
		    username = EXCLUDED.username,
		    first_name = EXCLUDED.first_name,
		    active = TRUE,
		    updated_at = now()
		RETURNING id`, telegramID, chatID, username, firstName).Scan(&u.ID)
	return u, err
}

func (s *Store) DeactivateChat(ctx context.Context, chatID int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET active = FALSE, updated_at = now() WHERE chat_id = $1`, chatID)
	return err
}

func (s *Store) CountActiveAlerts(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM price_alerts WHERE user_id = $1 AND triggered_at IS NULL`, userID).Scan(&n)
	return n, err
}

func (s *Store) CreateAlert(ctx context.Context, userID int64, symbol, direction string, target float64) (Alert, error) {
	a := Alert{UserID: userID, Symbol: symbol, Direction: direction, Target: target}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO price_alerts (user_id, symbol, direction, target)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at`, userID, symbol, direction, target).Scan(&a.ID, &a.CreatedAt)
	return a, err
}

func (s *Store) UserAlerts(ctx context.Context, userID int64) ([]Alert, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, symbol, direction, target, created_at
		FROM price_alerts
		WHERE user_id = $1 AND triggered_at IS NULL
		ORDER BY symbol, target`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Alert, error) {
		var a Alert
		err := r.Scan(&a.ID, &a.UserID, &a.Symbol, &a.Direction, &a.Target, &a.CreatedAt)
		return a, err
	})
}

func (s *Store) DeleteAlert(ctx context.Context, userID, alertID int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM price_alerts WHERE id = $1 AND user_id = $2 AND triggered_at IS NULL`, alertID, userID)
	return tag.RowsAffected() > 0, err
}

func (s *Store) ActiveAlerts(ctx context.Context) ([]Alert, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.user_id, u.chat_id, a.symbol, a.direction, a.target, a.created_at
		FROM price_alerts a
		JOIN users u ON u.id = a.user_id
		WHERE a.triggered_at IS NULL AND u.active`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Alert, error) {
		var a Alert
		err := r.Scan(&a.ID, &a.UserID, &a.ChatID, &a.Symbol, &a.Direction, &a.Target, &a.CreatedAt)
		return a, err
	})
}

func (s *Store) ClaimAlert(ctx context.Context, alertID int64, price float64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE price_alerts
		SET triggered_at = now(), triggered_price = $2
		WHERE id = $1 AND triggered_at IS NULL`, alertID, price)
	return tag.RowsAffected() > 0, err
}

func (s *Store) ReleaseAlert(ctx context.Context, alertID int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE price_alerts SET triggered_at = NULL, triggered_price = NULL WHERE id = $1`, alertID)
	return err
}
