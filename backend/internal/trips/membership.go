package trips

import (
	"context"
	"errors"

	"github.com/Max252t/T-Travels/backend/internal/idempotency"
	"github.com/jackc/pgx/v5"
)

var (
	ErrMemberNotFound  = errors.New("trip member not found")
	ErrOwnerDeparture  = errors.New("the trip owner cannot leave or be removed")
	ErrMemberBalance   = errors.New("member has a nonzero balance")
	ErrMemberDisputes  = errors.New("member has open disputes")
	ErrInactiveBalance = errors.New("change would affect the balance of a former member")
)

func (s *Service) Leave(ctx context.Context, userID, tripID, key string) error {
	return s.depart(ctx, userID, tripID, userID, key, true)
}

func (s *Service) RemoveMember(ctx context.Context, userID, tripID, targetID, key string) error {
	return s.depart(ctx, userID, tripID, targetID, key, false)
}

func (s *Service) depart(ctx context.Context, userID, tripID, targetID, key string, self bool) error {
	operation := "remove-member/" + tripID + "/" + targetID
	if self {
		operation = "leave-trip/" + tripID
	}
	_, err := inTransaction(ctx, s.pool, func(tx pgx.Tx) (bool, error) {
		if self {
			if _, _, err := memberStatus(ctx, tx, tripID, userID); err != nil {
				return false, err
			}
		} else if err := requireOwner(ctx, tx, userID, tripID); err != nil {
			return false, err
		}
		return idempotency.Do(ctx, tx, userID, key, operation, nil, func() (bool, error) {
			if err := lockTrip(ctx, tx, tripID); err != nil {
				return false, err
			}
			role, active, err := memberStatus(ctx, tx, tripID, targetID)
			if err != nil {
				return false, err
			}
			if role == "owner" {
				return false, ErrOwnerDeparture
			}
			if !active {
				return true, nil
			}
			balances, err := readBalances(ctx, tx, tripID)
			if err != nil {
				return false, err
			}
			for _, balance := range balances {
				if balance.UserID == targetID && balance.Balance != 0 {
					return false, ErrMemberBalance
				}
			}
			var open bool
			err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM expense_disputes
				WHERE trip_id = $1::text::uuid AND created_by = $2::text::uuid AND resolved_at IS NULL)`, tripID, targetID).Scan(&open)
			if err != nil {
				return false, err
			}
			if open {
				return false, ErrMemberDisputes
			}
			_, err = tx.Exec(ctx, `UPDATE trip_members SET left_at = clock_timestamp()
				WHERE trip_id = $1::text::uuid AND user_id = $2::text::uuid AND left_at IS NULL`, tripID, targetID)
			if err != nil {
				return false, err
			}
			if err := bumpFinancialRevision(ctx, tx, tripID); err != nil {
				return false, err
			}
			err = notifyDeparture(ctx, tx, tripID, userID, targetID)
			return err == nil, err
		})
	})
	return err
}

func memberStatus(ctx context.Context, db queryRower, tripID, userID string) (string, bool, error) {
	var role string
	var active bool
	err := db.QueryRow(ctx, `SELECT role, left_at IS NULL FROM trip_members
		WHERE trip_id = $1::text::uuid AND user_id = $2::text::uuid`, tripID, userID).Scan(&role, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrMemberNotFound
	}
	return role, active, err
}

func checkInactiveBalances(ctx context.Context, db queryRower, tripID string, before Expense, after *Expense) error {
	deltas := make(map[string]int64)
	deltas[before.PayerID] -= before.Amount
	for _, share := range before.Participants {
		deltas[share.UserID] += share.Amount
	}
	if after != nil {
		deltas[after.PayerID] += after.Amount
		for _, share := range after.Participants {
			deltas[share.UserID] -= share.Amount
		}
	}
	var affected []string
	for id, delta := range deltas {
		if delta != 0 {
			affected = append(affected, id)
		}
	}
	if len(affected) == 0 {
		return nil
	}
	var blocked bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM trip_members
		WHERE trip_id = $1::text::uuid AND user_id = ANY($2::text[]::uuid[]) AND left_at IS NOT NULL)`, tripID, affected).Scan(&blocked)
	if err == nil && blocked {
		return ErrInactiveBalance
	}
	return err
}
