package trips

import (
	"context"
	"errors"
	"time"

	"github.com/Max252t/T-Travels/backend/internal/idempotency"
	"github.com/jackc/pgx/v5"
)

const MaxTransfer = 2 * MaxBudget

var (
	ErrInvalidSettlement     = errors.New("invalid simulated transfer")
	ErrSettlementRevision    = errors.New("settlement suggestions have changed")
	ErrSettlementUnavailable = errors.New("transfer is not currently suggested")
	errUnbalancedLedger      = errors.New("trip balances do not sum to zero")
)

type Balance struct {
	UserID  string `json:"user_id"`
	Name    string `json:"name"`
	Balance int64  `json:"balance"`
	Active  bool   `json:"active"`
}

type Suggestions struct {
	Revision        int64      `json:"revision"`
	Optimal         bool       `json:"optimal"`
	Transfers       []Transfer `json:"transfers"`
	HasOpenDisputes bool       `json:"has_open_disputes"`
}

type Settlement struct {
	ID     string `json:"id"`
	TripID string `json:"trip_id"`
	Transfer
	CreatedAt time.Time `json:"created_at"`
}

type SettlementInput struct {
	ToUserID            string `json:"to_user_id"`
	Amount              int64  `json:"amount"`
	Revision            int64  `json:"revision"`
	AcknowledgeDisputes bool   `json:"acknowledge_disputes,omitempty"`
}

func (s *Service) Balances(ctx context.Context, userID, tripID string) ([]Balance, error) {
	return inReadTransaction(ctx, s.pool, func(tx pgx.Tx) ([]Balance, error) {
		if _, err := financialRevision(ctx, tx, userID, tripID); err != nil {
			return nil, err
		}
		return readBalances(ctx, tx, tripID)
	})
}

func (s *Service) SettlementSuggestions(ctx context.Context, userID, tripID string) (Suggestions, error) {
	return inReadTransaction(ctx, s.pool, func(tx pgx.Tx) (Suggestions, error) {
		return readSuggestions(ctx, tx, userID, tripID)
	})
}

func (s *Service) ListSettlements(ctx context.Context, userID, tripID string, limit, offset int32) ([]Settlement, error) {
	return inReadTransaction(ctx, s.pool, func(tx pgx.Tx) ([]Settlement, error) {
		if _, err := financialRevision(ctx, tx, userID, tripID); err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `SELECT id::text, trip_id::text, from_user_id::text, to_user_id::text, amount, created_at
			FROM settlements WHERE trip_id = $1::text::uuid ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`, tripID, limit, offset)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Settlement, error) { return scanSettlement(row) })
	})
}

func (s *Service) CreateSettlement(ctx context.Context, userID, tripID, key string, input SettlementInput) (Settlement, error) {
	to, ok := expenseUUID(input.ToUserID)
	if !ok || to == userID || input.Amount < 1 || input.Amount > MaxTransfer || input.Revision < 1 {
		return Settlement{}, ErrInvalidSettlement
	}
	input.ToUserID = to
	return inTransaction(ctx, s.pool, func(tx pgx.Tx) (Settlement, error) {
		if _, err := financialRevision(ctx, tx, userID, tripID); err != nil {
			return Settlement{}, err
		}
		return idempotency.Do(ctx, tx, userID, key, "create-settlement/"+tripID, input, func() (Settlement, error) {
			if _, err := getTrip(ctx, tx, userID, tripID, true); err != nil {
				return Settlement{}, err
			}
			suggestions, err := readSuggestions(ctx, tx, userID, tripID)
			if err != nil {
				return Settlement{}, err
			}
			if suggestions.Revision != input.Revision {
				return Settlement{}, ErrSettlementRevision
			}
			if suggestions.HasOpenDisputes && !input.AcknowledgeDisputes {
				return Settlement{}, ErrDisputeAcknowledgement
			}
			if !permitsTransfer(suggestions.Transfers, userID, to, input.Amount) {
				return Settlement{}, ErrSettlementUnavailable
			}
			var recipientActive bool
			err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM trip_members
				WHERE trip_id = $1::text::uuid AND user_id = $2::text::uuid AND left_at IS NULL)`, tripID, to).Scan(&recipientActive)
			if err != nil {
				return Settlement{}, err
			}
			if !recipientActive {
				return Settlement{}, ErrSettlementUnavailable
			}
			settlement, err := scanSettlement(tx.QueryRow(ctx, `INSERT INTO settlements (trip_id, from_user_id, to_user_id, amount)
				VALUES ($1::text::uuid, $2::text::uuid, $3::text::uuid, $4)
				RETURNING id::text, trip_id::text, from_user_id::text, to_user_id::text, amount, created_at`, tripID, userID, to, input.Amount))
			if err != nil {
				return Settlement{}, err
			}
			if err := bumpFinancialRevision(ctx, tx, tripID); err != nil {
				return Settlement{}, err
			}
			if err := notifySettlement(ctx, tx, tripID, userID, settlement.ID); err != nil {
				return Settlement{}, err
			}
			return settlement, nil
		})
	})
}

func permitsTransfer(transfers []Transfer, from, to string, amount int64) bool {
	for _, transfer := range transfers {
		if transfer.FromUserID == from && transfer.ToUserID == to && amount <= transfer.Amount {
			return true
		}
	}
	return false
}

func readSuggestions(ctx context.Context, tx pgx.Tx, userID, tripID string) (Suggestions, error) {
	revision, err := financialRevision(ctx, tx, userID, tripID)
	if err != nil {
		return Suggestions{}, err
	}
	balances, err := readBalances(ctx, tx, tripID)
	if err != nil {
		return Suggestions{}, err
	}
	transfers, optimal := suggestTransfers(balances)
	if transfers == nil {
		return Suggestions{}, errUnbalancedLedger
	}
	var open bool
	err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM expense_disputes
		WHERE trip_id = $1::text::uuid AND resolved_at IS NULL)`, tripID).Scan(&open)
	return Suggestions{Revision: revision, Optimal: optimal, Transfers: transfers, HasOpenDisputes: open}, err
}

func readBalances(ctx context.Context, tx pgx.Tx, tripID string) ([]Balance, error) {
	rows, err := tx.Query(ctx, `WITH active_expenses AS (
		SELECT id, payer_id, amount FROM expenses WHERE trip_id = $1::text::uuid AND deleted_at IS NULL
	), entries AS (
		SELECT payer_id AS user_id, amount::numeric AS delta FROM active_expenses
		UNION ALL SELECT p.user_id, -p.amount::numeric FROM expense_splits p JOIN active_expenses e ON e.id = p.expense_id
		UNION ALL SELECT from_user_id, amount::numeric FROM settlements WHERE trip_id = $1::text::uuid
		UNION ALL SELECT to_user_id, -amount::numeric FROM settlements WHERE trip_id = $1::text::uuid
	), totals AS (
		SELECT user_id, sum(delta) AS balance FROM entries GROUP BY user_id
	)
	SELECT m.user_id::text, u.name, COALESCE(t.balance, 0)::bigint, m.left_at IS NULL
	FROM trip_members m JOIN users u ON u.id = m.user_id LEFT JOIN totals t ON t.user_id = m.user_id
	WHERE m.trip_id = $1::text::uuid ORDER BY m.user_id`, tripID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Balance, error) {
		var balance Balance
		err := row.Scan(&balance.UserID, &balance.Name, &balance.Balance, &balance.Active)
		return balance, err
	})
}

func financialRevision(ctx context.Context, db queryRower, userID, tripID string) (int64, error) {
	var revision int64
	err := db.QueryRow(ctx, `SELECT t.financial_revision FROM trips t JOIN trip_members m ON m.trip_id = t.id
		WHERE t.id = $1::text::uuid AND m.user_id = $2::text::uuid AND m.left_at IS NULL`, tripID, userID).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return revision, err
}

func bumpFinancialRevision(ctx context.Context, tx pgx.Tx, tripID string) error {
	_, err := tx.Exec(ctx, `UPDATE trips SET financial_revision = financial_revision + 1 WHERE id = $1::text::uuid`, tripID)
	return err
}

func scanSettlement(row pgx.Row) (Settlement, error) {
	var settlement Settlement
	err := row.Scan(&settlement.ID, &settlement.TripID, &settlement.FromUserID, &settlement.ToUserID, &settlement.Amount, &settlement.CreatedAt)
	settlement.CreatedAt = settlement.CreatedAt.UTC()
	return settlement, err
}
