package trips

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Max252t/T-Travels/backend/internal/idempotency"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"
)

const MaxBudget = int64(1_000_000_000_000)

var (
	ErrNotFound   = errors.New("trip not found")
	ErrForbidden  = errors.New("trip owner required")
	ErrVersion    = errors.New("trip version conflict")
	ErrInvalid    = errors.New("invalid trip request")
	ErrInvitation = errors.New("invitation is invalid or expired")
	ErrRateLimit  = errors.New("invitation lookup rate exceeded")
)

type Input struct {
	Name        string `json:"name"`
	Destination string `json:"destination"`
	StartDate   string `json:"start_date"`
	EndDate     string `json:"end_date"`
	Budget      int64  `json:"budget"`
}

type Patch struct {
	Name        *string `json:"name,omitempty"`
	Destination *string `json:"destination,omitempty"`
	StartDate   *string `json:"start_date,omitempty"`
	EndDate     *string `json:"end_date,omitempty"`
	Budget      *int64  `json:"budget,omitempty"`
	Version     int64   `json:"version"`
}

type Trip struct {
	ID string `json:"id"`
	Input
	Currency string `json:"currency"`
	Version  int64  `json:"version"`
	Role     string `json:"role"`
}

type Member struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
}

type Service struct {
	pool     *pgxpool.Pool
	joins    *rate.Limiter
	receipts ReceiptStore
}

func NewService(pool *pgxpool.Pool, receiptStore ReceiptStore) *Service {
	return &Service{pool: pool, joins: rate.NewLimiter(10, 20), receipts: receiptStore}
}

const tripFields = `t.id::text, t.name, t.destination, t.start_date, t.end_date,
	t.budget, t.currency, t.version, m.role`

func (s *Service) List(ctx context.Context, userID string, limit, offset int32) ([]Trip, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+tripFields+`
		FROM trips t JOIN trip_members m ON m.trip_id = t.id
		WHERE m.user_id = $1::text::uuid AND m.left_at IS NULL
		ORDER BY t.created_at DESC, t.id DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Trip, error) { return scanTrip(row) })
}

func (s *Service) Get(ctx context.Context, userID, tripID string, expected int64) (Revisioned[Trip], error) {
	return readAtRevision(ctx, s.pool, userID, tripID, expected, func(tx pgx.Tx) (Trip, error) {
		return getTrip(ctx, tx, userID, tripID, false)
	})
}

func (s *Service) Create(ctx context.Context, userID, key string, input Input) (Trip, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Destination = strings.TrimSpace(input.Destination)
	if err := validate(input); err != nil {
		return Trip{}, err
	}
	return inTransaction(ctx, s.pool, func(tx pgx.Tx) (Trip, error) {
		return idempotency.Do(ctx, tx, userID, key, "create-trip", input, func() (Trip, error) {
			var tripID string
			err := tx.QueryRow(ctx, `INSERT INTO trips (name, destination, start_date, end_date, budget)
				VALUES ($1, $2, $3::text::date, $4::text::date, $5) RETURNING id::text`,
				input.Name, input.Destination, input.StartDate, input.EndDate, input.Budget).Scan(&tripID)
			if err != nil {
				return Trip{}, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO trip_members (trip_id, user_id, role)
				VALUES ($1::text::uuid, $2::text::uuid, 'owner')`, tripID, userID); err != nil {
				return Trip{}, err
			}
			return getTrip(ctx, tx, userID, tripID, false)
		})
	})
}

func (s *Service) Update(ctx context.Context, userID, tripID, key string, patch Patch) (Trip, error) {
	if patch.Version <= 0 || (patch.Name == nil && patch.Destination == nil && patch.StartDate == nil && patch.EndDate == nil && patch.Budget == nil) {
		return Trip{}, ErrInvalid
	}
	if patch.Name != nil {
		patch.Name = new(strings.TrimSpace(*patch.Name))
	}
	if patch.Destination != nil {
		patch.Destination = new(strings.TrimSpace(*patch.Destination))
	}
	return inTransaction(ctx, s.pool, func(tx pgx.Tx) (Trip, error) {
		if err := requireOwner(ctx, tx, userID, tripID); err != nil {
			return Trip{}, err
		}
		return idempotency.Do(ctx, tx, userID, key, "update-trip/"+tripID, patch, func() (Trip, error) {
			trip, err := getTrip(ctx, tx, userID, tripID, true)
			if err != nil {
				return Trip{}, err
			}
			if trip.Version != patch.Version {
				return Trip{}, ErrVersion
			}
			input := trip.Input
			apply(&input.Name, patch.Name)
			apply(&input.Destination, patch.Destination)
			apply(&input.StartDate, patch.StartDate)
			apply(&input.EndDate, patch.EndDate)
			apply(&input.Budget, patch.Budget)
			if err := validate(input); err != nil {
				return Trip{}, err
			}
			_, err = tx.Exec(ctx, `UPDATE trips SET name = $2, destination = $3,
				start_date = $4::text::date, end_date = $5::text::date, budget = $6, version = version + 1, snapshot_revision = snapshot_revision + 1
				WHERE id = $1::text::uuid`, tripID, input.Name, input.Destination, input.StartDate, input.EndDate, input.Budget)
			if err != nil {
				return Trip{}, err
			}
			trip.Input = input
			trip.Version++
			return trip, nil
		})
	})
}

func (s *Service) Members(ctx context.Context, userID, tripID string, limit, offset int32, expected int64) (Revisioned[[]Member], error) {
	return readAtRevision(ctx, s.pool, userID, tripID, expected, func(tx pgx.Tx) ([]Member, error) {
		return readMembers(ctx, tx, tripID, limit, offset)
	})
}

func readMembers(ctx context.Context, tx pgx.Tx, tripID string, limit, offset int32) ([]Member, error) {
	rows, err := tx.Query(ctx, `SELECT m.user_id::text, u.name, m.role
		FROM trip_members m JOIN users u ON u.id = m.user_id
		WHERE m.trip_id = $1::text::uuid AND m.left_at IS NULL
		ORDER BY m.role DESC, m.joined_at, m.user_id LIMIT $2 OFFSET $3`, tripID, limit, offset)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Member, error) {
		var member Member
		err := row.Scan(&member.UserID, &member.Name, &member.Role)
		return member, err
	})
}

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func getTrip(ctx context.Context, db queryRower, userID, tripID string, lock bool) (Trip, error) {
	if lock {
		if err := lockTrip(ctx, db, tripID); err != nil {
			return Trip{}, err
		}
	}
	query := `SELECT ` + tripFields + ` FROM trips t JOIN trip_members m ON m.trip_id = t.id
		WHERE t.id = $1::text::uuid AND m.user_id = $2::text::uuid AND m.left_at IS NULL`
	trip, err := scanTrip(db.QueryRow(ctx, query, tripID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Trip{}, ErrNotFound
	}
	return trip, err
}

func lockTrip(ctx context.Context, db queryRower, tripID string) error {
	var id string
	err := db.QueryRow(ctx, `SELECT id::text FROM trips WHERE id = $1::text::uuid FOR UPDATE`, tripID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func scanTrip(row pgx.Row) (Trip, error) {
	var trip Trip
	var start, end time.Time
	err := row.Scan(&trip.ID, &trip.Name, &trip.Destination, &start, &end, &trip.Budget, &trip.Currency, &trip.Version, &trip.Role)
	if err == nil {
		trip.StartDate, trip.EndDate = start.Format(time.DateOnly), end.Format(time.DateOnly)
	}
	return trip, err
}

func requireOwner(ctx context.Context, tx pgx.Tx, userID, tripID string) error {
	var role string
	err := tx.QueryRow(ctx, `SELECT role FROM trip_members
		WHERE trip_id = $1::text::uuid AND user_id = $2::text::uuid AND left_at IS NULL`, tripID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err == nil && role != "owner" {
		return ErrForbidden
	}
	return err
}

func validate(input Input) error {
	start, startErr := time.Parse(time.DateOnly, input.StartDate)
	end, endErr := time.Parse(time.DateOnly, input.EndDate)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 100 || strings.ContainsRune(input.Name, 0) ||
		input.Destination == "" || utf8.RuneCountInString(input.Destination) > 150 || strings.ContainsRune(input.Destination, 0) ||
		startErr != nil || endErr != nil || start.Year() < 1 || end.Before(start) || input.Budget < 0 || input.Budget > MaxBudget {
		return ErrInvalid
	}
	return nil
}

func apply[T any](field, patch *T) {
	if patch != nil {
		*field = *patch
	}
}

func inTransaction[T any](ctx context.Context, pool *pgxpool.Pool, work func(pgx.Tx) (T, error)) (T, error) {
	return runTransaction(ctx, pool, pgx.TxOptions{}, work)
}

func inReadTransaction[T any](ctx context.Context, pool *pgxpool.Pool, work func(pgx.Tx) (T, error)) (T, error) {
	return runTransaction(ctx, pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, work)
}

func runTransaction[T any](ctx context.Context, pool *pgxpool.Pool, options pgx.TxOptions, work func(pgx.Tx) (T, error)) (T, error) {
	var result T
	err := pgx.BeginTxFunc(ctx, pool, options, func(tx pgx.Tx) error {
		var err error
		result, err = work(tx)
		return err
	})
	return result, err
}
