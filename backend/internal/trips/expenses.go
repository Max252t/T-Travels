package trips

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Max252t/T-Travels/backend/internal/idempotency"
	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidExpense   = errors.New("invalid expense")
	ErrExpenseNotFound  = errors.New("expense not found")
	ErrExpenseForbidden = errors.New("expense editor required")
	ErrExpenseVersion   = errors.New("expense version conflict")
	ErrExpenseLimit     = errors.New("total expenses exceed the trip limit")
	categoryPattern     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

type ExpenseData struct {
	Title       string  `json:"title"`
	Amount      int64   `json:"amount"`
	Category    string  `json:"category"`
	PayerID     string  `json:"payer_id"`
	SplitType   string  `json:"split_type"`
	Date        string  `json:"date"`
	Description *string `json:"description"`
}

type ExpenseInput struct {
	ExpenseData
	Participants []ParticipantInput `json:"participants"`
}

type Expense struct {
	ID        string `json:"id"`
	TripID    string `json:"trip_id"`
	CreatedBy string `json:"created_by"`
	Version   int64  `json:"version"`
	ExpenseData
	Participants []Share         `json:"participants"`
	Dispute      *ExpenseDispute `json:"dispute,omitempty"`
}

type ExpensePatch struct {
	Title          *string
	Amount         *int64
	Category       *string
	PayerID        *string
	SplitType      *string
	Date           *string
	Description    *string
	SetDescription bool
	Participants   []ParticipantInput
	Version        int64
}

const expenseFields = `e.id::text, e.trip_id::text, e.created_by::text, e.version, e.title, e.amount,
	e.category, e.payer_id::text, e.split_type, e.expense_date, e.description,
	(SELECT jsonb_agg(jsonb_build_object('user_id', p.user_id, 'amount', p.amount, 'basis_points', p.basis_points)
		ORDER BY p.user_id) FROM expense_splits p WHERE p.expense_id = e.id),
	(SELECT jsonb_build_object('id', d.id, 'expense_id', d.expense_id, 'created_by', d.created_by,
		'reason', d.reason, 'created_at', d.created_at, 'resolved_at', d.resolved_at)
		FROM expense_disputes d WHERE d.expense_id = e.id AND d.resolved_at IS NULL)`

func (s *Service) ListExpenses(ctx context.Context, userID, tripID string, limit, offset int32) ([]Expense, error) {
	return inReadTransaction(ctx, s.pool, func(tx pgx.Tx) ([]Expense, error) {
		if _, err := getTrip(ctx, tx, userID, tripID, false); err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `SELECT `+expenseFields+` FROM expenses e
			WHERE e.trip_id = $1::text::uuid AND e.deleted_at IS NULL
			ORDER BY e.expense_date DESC, e.id DESC LIMIT $2 OFFSET $3`, tripID, limit, offset)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Expense, error) { return scanExpense(row) })
	})
}

func (s *Service) GetExpense(ctx context.Context, userID, tripID, expenseID string) (Expense, error) {
	return inReadTransaction(ctx, s.pool, func(tx pgx.Tx) (Expense, error) {
		if _, err := getTrip(ctx, tx, userID, tripID, false); err != nil {
			return Expense{}, err
		}
		return getExpense(ctx, tx, tripID, expenseID)
	})
}

func (s *Service) CreateExpense(ctx context.Context, userID, tripID, key string, input ExpenseInput) (Expense, error) {
	input.Title = strings.TrimSpace(input.Title)
	payer, ok := expenseUUID(input.PayerID)
	if !ok || !validExpenseData(input.ExpenseData) {
		return Expense{}, ErrInvalidExpense
	}
	input.PayerID = payer
	var err error
	input.Participants, err = canonicalParticipants(input.Participants)
	if err != nil {
		return Expense{}, err
	}
	shares, err := calculateShares(input.Amount, input.SplitType, input.Participants)
	if err != nil {
		return Expense{}, err
	}

	return inTransaction(ctx, s.pool, func(tx pgx.Tx) (Expense, error) {
		if _, err := getTrip(ctx, tx, userID, tripID, false); err != nil {
			return Expense{}, err
		}
		return idempotency.Do(ctx, tx, userID, key, "create-expense/"+tripID, input, func() (Expense, error) {
			if _, err := getTrip(ctx, tx, userID, tripID, true); err != nil {
				return Expense{}, err
			}
			if err := validateExpenseMembers(ctx, tx, tripID, payer, shares); err != nil {
				return Expense{}, err
			}
			if err := validateExpenseTotal(ctx, tx, tripID, "", input.Amount); err != nil {
				return Expense{}, err
			}
			var id string
			err := tx.QueryRow(ctx, `INSERT INTO expenses
				(trip_id, created_by, title, amount, category, payer_id, split_type, expense_date, description)
				VALUES ($1::text::uuid, $2::text::uuid, $3, $4, $5, $6::text::uuid, $7, $8::text::date, $9)
				RETURNING id::text`, tripID, userID, input.Title, input.Amount, input.Category, payer,
				input.SplitType, input.Date, input.Description).Scan(&id)
			if err != nil {
				return Expense{}, err
			}
			if err := insertShares(ctx, tx, tripID, id, shares); err != nil {
				return Expense{}, err
			}
			if err := bumpFinancialRevision(ctx, tx, tripID); err != nil {
				return Expense{}, err
			}
			if err := notifyExpense(ctx, tx, tripID, userID, id, "expense_created"); err != nil {
				return Expense{}, err
			}
			return Expense{ID: id, TripID: tripID, CreatedBy: userID, Version: 1, ExpenseData: input.ExpenseData, Participants: shares}, nil
		})
	})
}

func (s *Service) UpdateExpense(ctx context.Context, userID, tripID, expenseID, key string, patch ExpensePatch) (Expense, error) {
	if patch.Version <= 0 || !patch.hasChanges() {
		return Expense{}, ErrInvalidExpense
	}
	if patch.Title != nil {
		patch.Title = new(strings.TrimSpace(*patch.Title))
	}
	if patch.PayerID != nil {
		id, ok := expenseUUID(*patch.PayerID)
		if !ok {
			return Expense{}, ErrInvalidExpense
		}
		patch.PayerID = new(id)
	}
	if patch.Participants != nil {
		participants, err := canonicalParticipants(patch.Participants)
		if err != nil {
			return Expense{}, err
		}
		patch.Participants = participants
	}
	return inTransaction(ctx, s.pool, func(tx pgx.Tx) (Expense, error) {
		if err := requireExpenseEditor(ctx, tx, userID, tripID, expenseID); err != nil {
			return Expense{}, err
		}
		return idempotency.Do(ctx, tx, userID, key, "update-expense/"+tripID+"/"+expenseID, patch, func() (Expense, error) {
			if _, err := getTrip(ctx, tx, userID, tripID, true); err != nil {
				return Expense{}, err
			}
			expense, err := getExpense(ctx, tx, tripID, expenseID)
			if err != nil {
				return Expense{}, err
			}
			if expense.Version != patch.Version {
				return Expense{}, ErrExpenseVersion
			}
			newData := patch.apply(expense.ExpenseData)
			if !validExpenseData(newData) {
				return Expense{}, ErrInvalidExpense
			}
			shares := expense.Participants
			recalculate := patch.Participants != nil || newData.Amount != expense.Amount || newData.SplitType != expense.SplitType
			if recalculate {
				participants := patch.Participants
				if participants == nil {
					if newData.SplitType != expense.SplitType {
						return Expense{}, ErrInvalidExpense
					}
					participants = savedSplitInput(expense)
				}
				shares, err = calculateShares(newData.Amount, newData.SplitType, participants)
				if err != nil {
					return Expense{}, err
				}
			}
			if expense.PayerID != newData.PayerID || patch.Participants != nil {
				if err := validateExpenseMembers(ctx, tx, tripID, newData.PayerID, shares); err != nil {
					return Expense{}, err
				}
			}
			if recalculate || newData.PayerID != expense.PayerID {
				changed := &Expense{ExpenseData: newData, Participants: shares}
				if err := checkInactiveBalances(ctx, tx, tripID, expense, changed); err != nil {
					return Expense{}, err
				}
			}
			if newData.Amount != expense.Amount {
				if err := validateExpenseTotal(ctx, tx, tripID, expenseID, newData.Amount); err != nil {
					return Expense{}, err
				}
			}
			_, err = tx.Exec(ctx, `UPDATE expenses SET title = $2, amount = $3, category = $4, payer_id = $5::text::uuid,
				split_type = $6, expense_date = $7::text::date, description = $8, version = version + 1
				WHERE id = $1::text::uuid`, expenseID, newData.Title, newData.Amount, newData.Category, newData.PayerID,
				newData.SplitType, newData.Date, newData.Description)
			if err != nil {
				return Expense{}, err
			}
			if recalculate {
				if _, err := tx.Exec(ctx, `DELETE FROM expense_splits WHERE expense_id = $1::text::uuid`, expenseID); err != nil {
					return Expense{}, err
				}
				if err := insertShares(ctx, tx, tripID, expenseID, shares); err != nil {
					return Expense{}, err
				}
			}
			if err := bumpFinancialRevision(ctx, tx, tripID); err != nil {
				return Expense{}, err
			}
			if err := notifyExpense(ctx, tx, tripID, userID, expenseID, "expense_updated"); err != nil {
				return Expense{}, err
			}
			expense.ExpenseData, expense.Participants = newData, shares
			expense.Version++
			return expense, nil
		})
	})
}

func (s *Service) DeleteExpense(ctx context.Context, userID, tripID, expenseID, key string, version int64) error {
	_, err := inTransaction(ctx, s.pool, func(tx pgx.Tx) (bool, error) {
		if err := requireExpenseEditor(ctx, tx, userID, tripID, expenseID); err != nil {
			return false, err
		}
		return idempotency.Do(ctx, tx, userID, key, "delete-expense/"+tripID+"/"+expenseID, version, func() (bool, error) {
			if _, err := getTrip(ctx, tx, userID, tripID, true); err != nil {
				return false, err
			}
			var storedVersion int64
			var deleted bool
			err := tx.QueryRow(ctx, `SELECT version, deleted_at IS NOT NULL FROM expenses
				WHERE id = $1::text::uuid AND trip_id = $2::text::uuid`, expenseID, tripID).Scan(&storedVersion, &deleted)
			if err != nil {
				return false, err
			}
			if deleted {
				return true, nil
			}
			if storedVersion != version {
				return false, ErrExpenseVersion
			}
			expense, err := getExpense(ctx, tx, tripID, expenseID)
			if err != nil {
				return false, err
			}
			if expense.Dispute != nil {
				return false, ErrOpenDispute
			}
			if err := checkInactiveBalances(ctx, tx, tripID, expense, nil); err != nil {
				return false, err
			}
			if _, err := tx.Exec(ctx, `UPDATE expenses SET deleted_at = now(), version = version + 1 WHERE id = $1::text::uuid`, expenseID); err != nil {
				return false, err
			}
			if err := bumpFinancialRevision(ctx, tx, tripID); err != nil {
				return false, err
			}
			err = notifyExpense(ctx, tx, tripID, userID, expenseID, "expense_deleted")
			return err == nil, err
		})
	})
	return err
}

func (p ExpensePatch) hasChanges() bool {
	return p.Title != nil || p.Amount != nil || p.Category != nil || p.PayerID != nil || p.SplitType != nil ||
		p.Date != nil || p.SetDescription || p.Participants != nil
}

func (p ExpensePatch) apply(data ExpenseData) ExpenseData {
	apply(&data.Title, p.Title)
	apply(&data.Amount, p.Amount)
	apply(&data.Category, p.Category)
	apply(&data.PayerID, p.PayerID)
	apply(&data.SplitType, p.SplitType)
	apply(&data.Date, p.Date)
	if p.SetDescription {
		data.Description = p.Description
	}
	return data
}

func validExpenseData(data ExpenseData) bool {
	date, err := time.Parse(time.DateOnly, data.Date)
	return err == nil && date.Year() >= 1 && date.Format(time.DateOnly) == data.Date &&
		data.Amount >= 1 && data.Amount <= MaxBudget && categoryPattern.MatchString(data.Category) &&
		data.Title != "" && utf8.RuneCountInString(data.Title) <= 120 && !strings.ContainsRune(data.Title, 0) &&
		(data.Description == nil || (utf8.RuneCountInString(*data.Description) <= 1000 && !strings.ContainsRune(*data.Description, 0)))
}

func scanExpense(row pgx.Row) (Expense, error) {
	var expense Expense
	var date time.Time
	err := row.Scan(&expense.ID, &expense.TripID, &expense.CreatedBy, &expense.Version, &expense.Title, &expense.Amount,
		&expense.Category, &expense.PayerID, &expense.SplitType, &date, &expense.Description, &expense.Participants, &expense.Dispute)
	if err == nil {
		expense.Date = date.Format(time.DateOnly)
		if expense.Dispute != nil {
			expense.Dispute.CreatedAt = expense.Dispute.CreatedAt.UTC()
		}
	}
	return expense, err
}

func getExpense(ctx context.Context, db queryRower, tripID, id string) (Expense, error) {
	expense, err := scanExpense(db.QueryRow(ctx, `SELECT `+expenseFields+` FROM expenses e
		WHERE e.trip_id = $1::text::uuid AND e.id = $2::text::uuid AND e.deleted_at IS NULL`, tripID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Expense{}, ErrExpenseNotFound
	}
	return expense, err
}

func requireExpenseEditor(ctx context.Context, tx pgx.Tx, userID, tripID, expenseID string) error {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT m.role = 'owner' OR e.created_by = m.user_id
		FROM expenses e JOIN trip_members m ON m.trip_id = e.trip_id
		WHERE m.user_id = $1::text::uuid AND m.left_at IS NULL AND e.trip_id = $2::text::uuid AND e.id = $3::text::uuid`,
		userID, tripID, expenseID).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrExpenseNotFound
	}
	if err == nil && !allowed {
		return ErrExpenseForbidden
	}
	return err
}

func validateExpenseMembers(ctx context.Context, tx pgx.Tx, tripID, payer string, shares []Share) error {
	ids := []string{payer}
	for _, share := range shares {
		if share.UserID != payer {
			ids = append(ids, share.UserID)
		}
	}
	var count int64
	err := tx.QueryRow(ctx, `SELECT count(*) FROM trip_members
		WHERE trip_id = $1::text::uuid AND user_id = ANY($2::text[]::uuid[]) AND left_at IS NULL`, tripID, ids).Scan(&count)
	if err == nil && count != int64(len(ids)) {
		return ErrInvalidExpense
	}
	return err
}

func validateExpenseTotal(ctx context.Context, db queryRower, tripID, excludedID string, addition int64) error {
	var total int64
	err := db.QueryRow(ctx, `SELECT COALESCE(sum(amount), 0)::bigint FROM expenses
		WHERE trip_id = $1::text::uuid AND deleted_at IS NULL AND id IS DISTINCT FROM NULLIF($2::text, '')::uuid`, tripID, excludedID).Scan(&total)
	if err == nil && total > MaxBudget-addition {
		return ErrExpenseLimit
	}
	return err
}

func insertShares(ctx context.Context, tx pgx.Tx, tripID, expenseID string, shares []Share) error {
	ids, amounts, points := make([]string, len(shares)), make([]int64, len(shares)), make([]*int64, len(shares))
	for i, share := range shares {
		ids[i], amounts[i], points[i] = share.UserID, share.Amount, share.BasisPoints
	}
	_, err := tx.Exec(ctx, `INSERT INTO expense_splits (trip_id, expense_id, user_id, amount, basis_points)
		SELECT $1::text::uuid, $2::text::uuid, split.user_id::uuid, split.amount, split.points
		FROM unnest($3::text[], $4::bigint[], $5::bigint[]) AS split(user_id, amount, points)`, tripID, expenseID, ids, amounts, points)
	return err
}
