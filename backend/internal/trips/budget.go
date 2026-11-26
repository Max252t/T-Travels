package trips

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

type Budget struct {
	Budget    int64  `json:"budget"`
	Spent     int64  `json:"spent"`
	Remaining int64  `json:"remaining"`
	Currency  string `json:"currency"`
}

func (s *Service) Budget(ctx context.Context, userID, tripID string, expected int64) (Revisioned[Budget], error) {
	return readAtRevision(ctx, s.pool, userID, tripID, expected, func(tx pgx.Tx) (Budget, error) {
		return readBudget(ctx, tx, userID, tripID)
	})
}

func readBudget(ctx context.Context, db queryRower, userID, tripID string) (Budget, error) {
	var budget Budget
	err := db.QueryRow(ctx, `SELECT t.budget, t.currency, COALESCE(sum(e.amount), 0)::bigint FROM trips t
		JOIN trip_members m ON m.trip_id = t.id
		LEFT JOIN expenses e ON e.trip_id = t.id AND e.deleted_at IS NULL
		WHERE t.id = $1::text::uuid AND m.user_id = $2::text::uuid AND m.left_at IS NULL GROUP BY t.id`, tripID, userID).
		Scan(&budget.Budget, &budget.Currency, &budget.Spent)
	if errors.Is(err, pgx.ErrNoRows) {
		return Budget{}, ErrNotFound
	}
	if err != nil {
		return Budget{}, err
	}
	budget.Remaining = budget.Budget - budget.Spent
	return budget, nil
}
