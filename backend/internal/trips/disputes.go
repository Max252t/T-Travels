package trips

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Max252t/T-Travels/backend/internal/idempotency"
	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidDispute         = errors.New("invalid dispute reason")
	ErrOpenDispute            = errors.New("expense has an open dispute")
	ErrNoDispute              = errors.New("expense has no open dispute")
	ErrDisputeAuthor          = errors.New("only the author can resolve this dispute")
	ErrDisputeAcknowledgement = errors.New("settlement requires open dispute acknowledgement")
)

type ExpenseDispute struct {
	ID         string     `json:"id"`
	ExpenseID  string     `json:"expense_id"`
	CreatedBy  string     `json:"created_by"`
	Reason     string     `json:"reason"`
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
}

const disputeFields = `id::text, expense_id::text, created_by::text, reason, created_at, resolved_at`

func (s *Service) OpenDispute(ctx context.Context, userID, tripID, expenseID, key, reason string) (ExpenseDispute, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > 1000 || strings.ContainsRune(reason, 0) {
		return ExpenseDispute{}, ErrInvalidDispute
	}
	return s.changeDispute(ctx, userID, tripID, expenseID, key, "open-dispute", reason, "expense_disputed", func(tx pgx.Tx) (ExpenseDispute, error) {
		result, err := scanDispute(tx.QueryRow(ctx, `INSERT INTO expense_disputes (trip_id, expense_id, created_by, reason)
			VALUES ($1::text::uuid, $2::text::uuid, $3::text::uuid, $4)
			ON CONFLICT (expense_id) WHERE resolved_at IS NULL DO NOTHING RETURNING `+disputeFields, tripID, expenseID, userID, reason))
		if errors.Is(err, pgx.ErrNoRows) {
			return ExpenseDispute{}, ErrOpenDispute
		}
		return result, err
	})
}

func (s *Service) ResolveDispute(ctx context.Context, userID, tripID, expenseID, key string) (ExpenseDispute, error) {
	return s.changeDispute(ctx, userID, tripID, expenseID, key, "resolve-dispute", nil, "dispute_resolved", func(tx pgx.Tx) (ExpenseDispute, error) {
		var authorID string
		err := tx.QueryRow(ctx, `SELECT created_by::text FROM expense_disputes WHERE expense_id = $1::text::uuid AND resolved_at IS NULL`, expenseID).Scan(&authorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ExpenseDispute{}, ErrNoDispute
		}
		if err != nil {
			return ExpenseDispute{}, err
		}
		if authorID != userID {
			return ExpenseDispute{}, ErrDisputeAuthor
		}
		return scanDispute(tx.QueryRow(ctx, `UPDATE expense_disputes SET resolved_at = clock_timestamp()
			WHERE expense_id = $1::text::uuid AND resolved_at IS NULL RETURNING `+disputeFields, expenseID))
	})
}

func (s *Service) changeDispute(ctx context.Context, userID, tripID, expenseID, key, operation string, input any, event string, change func(pgx.Tx) (ExpenseDispute, error)) (ExpenseDispute, error) {
	return inTransaction(ctx, s.pool, func(tx pgx.Tx) (ExpenseDispute, error) {
		if _, err := getTrip(ctx, tx, userID, tripID, false); err != nil {
			return ExpenseDispute{}, err
		}
		return idempotency.Do(ctx, tx, userID, key, operation+"/"+tripID+"/"+expenseID, input, func() (ExpenseDispute, error) {
			if _, err := getTrip(ctx, tx, userID, tripID, true); err != nil {
				return ExpenseDispute{}, err
			}
			var exists bool
			err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM expenses
				WHERE id = $1::text::uuid AND trip_id = $2::text::uuid AND deleted_at IS NULL)`, expenseID, tripID).Scan(&exists)
			if err != nil {
				return ExpenseDispute{}, err
			}
			if !exists {
				return ExpenseDispute{}, ErrExpenseNotFound
			}
			dispute, err := change(tx)
			if err != nil {
				return ExpenseDispute{}, err
			}
			if err := bumpFinancialRevision(ctx, tx, tripID); err != nil {
				return ExpenseDispute{}, err
			}
			if err := notifyExpense(ctx, tx, tripID, userID, expenseID, event); err != nil {
				return ExpenseDispute{}, err
			}
			return dispute, nil
		})
	})
}

func scanDispute(row pgx.Row) (ExpenseDispute, error) {
	var dispute ExpenseDispute
	err := row.Scan(&dispute.ID, &dispute.ExpenseID, &dispute.CreatedBy, &dispute.Reason, &dispute.CreatedAt, &dispute.ResolvedAt)
	dispute.CreatedAt = dispute.CreatedAt.UTC()
	if dispute.ResolvedAt != nil {
		dispute.ResolvedAt = new(dispute.ResolvedAt.UTC())
	}
	return dispute, err
}
