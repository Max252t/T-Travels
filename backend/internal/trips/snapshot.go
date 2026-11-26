package trips

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const snapshotLimit = 1000

var ErrSnapshotLimit = errors.New("trip exceeds the snapshot size limit")
var ErrSnapshotRevision = errors.New("trip snapshot revision changed")

type Revisioned[T any] struct {
	Value    T
	Revision int64
}

func readAtRevision[T any](ctx context.Context, pool *pgxpool.Pool, userID, tripID string, expected int64, read func(pgx.Tx) (T, error)) (Revisioned[T], error) {
	return inReadTransaction(ctx, pool, func(tx pgx.Tx) (Revisioned[T], error) {
		var result Revisioned[T]
		err := tx.QueryRow(ctx, `SELECT t.snapshot_revision FROM trips t JOIN trip_members m ON m.trip_id = t.id
			WHERE t.id = $1::text::uuid AND m.user_id = $2::text::uuid AND m.left_at IS NULL`, tripID, userID).Scan(&result.Revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return result, ErrNotFound
		}
		if err != nil {
			return result, err
		}
		if expected != 0 && result.Revision != expected {
			return result, ErrSnapshotRevision
		}
		result.Value, err = read(tx)
		return result, err
	})
}

type Snapshot struct {
	Revision    int64        `json:"revision"`
	Trip        Trip         `json:"trip"`
	Members     []Member     `json:"members"`
	Expenses    []Expense    `json:"expenses"`
	Budget      Budget       `json:"budget"`
	Balances    []Balance    `json:"balances"`
	Settlements []Settlement `json:"settlements"`
}

func (s *Service) Snapshot(ctx context.Context, userID, tripID string) (Snapshot, error) {
	return inReadTransaction(ctx, s.pool, func(tx pgx.Tx) (Snapshot, error) {
		trip, err := getTrip(ctx, tx, userID, tripID, false)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot := Snapshot{Trip: trip}
		var members int64
		err = tx.QueryRow(ctx, `SELECT snapshot_revision, (SELECT count(*) FROM trip_members WHERE trip_id = t.id)
			FROM trips t WHERE t.id = $1::text::uuid`, tripID).Scan(&snapshot.Revision, &members)
		if err != nil {
			return Snapshot{}, err
		}
		if members > snapshotLimit {
			return Snapshot{Revision: snapshot.Revision}, ErrSnapshotLimit
		}
		snapshot.Members, err = readMembers(ctx, tx, tripID, snapshotLimit+1, 0)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot.Expenses, err = readExpenses(ctx, tx, tripID, snapshotLimit+1, 0)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot.Settlements, err = readSettlements(ctx, tx, tripID, snapshotLimit+1, 0)
		if err != nil {
			return Snapshot{}, err
		}
		if len(snapshot.Members) > snapshotLimit || len(snapshot.Expenses) > snapshotLimit || len(snapshot.Settlements) > snapshotLimit {
			return Snapshot{Revision: snapshot.Revision}, ErrSnapshotLimit
		}
		snapshot.Budget, err = readBudget(ctx, tx, userID, tripID)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot.Balances, err = readBalances(ctx, tx, tripID)
		if err != nil {
			return Snapshot{}, err
		}
		if len(snapshot.Balances) > snapshotLimit {
			return Snapshot{Revision: snapshot.Revision}, ErrSnapshotLimit
		}
		return snapshot, nil
	})
}

func bumpSnapshotRevision(ctx context.Context, tx pgx.Tx, tripID string) error {
	_, err := tx.Exec(ctx, `UPDATE trips SET snapshot_revision = snapshot_revision + 1 WHERE id = $1::text::uuid`, tripID)
	return err
}
