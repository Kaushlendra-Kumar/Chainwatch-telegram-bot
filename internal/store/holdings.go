package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

type Holding struct {
	Symbol   string
	Quantity float64
	AvgCost  float64
}

type SellResult struct {
	Sold      float64
	AvgCost   float64
	Remaining float64
}

func (s *Store) AddHolding(ctx context.Context, userID int64, symbol string, qty, price float64) (Holding, error) {
	h := Holding{Symbol: symbol}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO holdings (user_id, symbol, quantity, avg_cost)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, symbol) DO UPDATE
		SET avg_cost = (holdings.quantity * holdings.avg_cost + EXCLUDED.quantity * EXCLUDED.avg_cost)
		               / (holdings.quantity + EXCLUDED.quantity),
		    quantity = holdings.quantity + EXCLUDED.quantity,
		    updated_at = now()
		RETURNING quantity, avg_cost`, userID, symbol, qty, price).Scan(&h.Quantity, &h.AvgCost)
	return h, err
}

func (s *Store) SellHolding(ctx context.Context, userID int64, symbol string, qty float64) (SellResult, error) {
	var res SellResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var current float64
		err := tx.QueryRow(ctx, `
			SELECT quantity, avg_cost FROM holdings
			WHERE user_id = $1 AND symbol = $2
			FOR UPDATE`, userID, symbol).Scan(&current, &res.AvgCost)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if qty >= current*(1-1e-9) {
			res.Sold, res.Remaining = current, 0
			_, err = tx.Exec(ctx, `DELETE FROM holdings WHERE user_id = $1 AND symbol = $2`, userID, symbol)
			return err
		}
		res.Sold, res.Remaining = qty, current-qty
		_, err = tx.Exec(ctx, `
			UPDATE holdings SET quantity = $3, updated_at = now()
			WHERE user_id = $1 AND symbol = $2`, userID, symbol, res.Remaining)
		return err
	})
	return res, err
}

func (s *Store) RemoveHolding(ctx context.Context, userID int64, symbol string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM holdings WHERE user_id = $1 AND symbol = $2`, userID, symbol)
	return tag.RowsAffected() > 0, err
}

func (s *Store) Holdings(ctx context.Context, userID int64) ([]Holding, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT symbol, quantity, avg_cost FROM holdings
		WHERE user_id = $1
		ORDER BY quantity * avg_cost DESC`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Holding, error) {
		var h Holding
		err := r.Scan(&h.Symbol, &h.Quantity, &h.AvgCost)
		return h, err
	})
}
