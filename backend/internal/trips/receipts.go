package trips

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"log/slog"
	"time"

	"github.com/Max252t/T-Travels/backend/internal/idempotency"
	"github.com/jackc/pgx/v5"
)

const MaxReceiptSize = 5 << 20

var (
	ErrReceiptNotFound = errors.New("receipt not found")
	ErrReceiptStorage  = errors.New("receipt storage is unavailable")
)

type Receipt struct {
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (s *Service) GetReceipt(ctx context.Context, userID, tripID, expenseID string) (Receipt, []byte, error) {
	var key string
	var receipt Receipt
	err := s.pool.QueryRow(ctx, `SELECT r.object_key, r.content_type, r.size_bytes, r.updated_at
		FROM receipts r JOIN expenses e ON e.id = r.expense_id JOIN trip_members m ON m.trip_id = r.trip_id
		WHERE r.trip_id = $1::text::uuid AND r.expense_id = $2::text::uuid
			AND m.user_id = $3::text::uuid AND m.left_at IS NULL AND e.deleted_at IS NULL`, tripID, expenseID, userID).
		Scan(&key, &receipt.ContentType, &receipt.SizeBytes, &receipt.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, nil, ErrReceiptNotFound
	}
	if err != nil {
		return Receipt{}, nil, err
	}
	if s.receipts == nil {
		return Receipt{}, nil, ErrReceiptStorage
	}
	data, err := s.receipts.Get(ctx, key, receipt.SizeBytes)
	if err != nil {
		return Receipt{}, nil, ErrReceiptStorage
	}
	receipt.UpdatedAt = receipt.UpdatedAt.UTC()
	return receipt, data, nil
}

func (s *Service) PutReceipt(ctx context.Context, userID, tripID, expenseID, key, contentType string, data []byte) (Receipt, error) {
	if s.receipts == nil {
		return Receipt{}, ErrReceiptStorage
	}
	if err := requireExpenseEditor(ctx, s.pool, userID, tripID, expenseID); err != nil {
		return Receipt{}, err
	}
	objectKey := "receipts/" + rand.Text()
	_, err := s.pool.Exec(ctx, `INSERT INTO receipt_cleanup (object_key, delete_after)
		VALUES ($1, clock_timestamp() + interval '1 hour')`, objectKey)
	if err != nil {
		return Receipt{}, err
	}
	attemptedUpload := false
	defer func() {
		if !attemptedUpload {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			if _, err := s.pool.Exec(cleanupCtx, `DELETE FROM receipt_cleanup WHERE object_key = $1`, objectKey); err != nil {
				slog.Warn("unused receipt staging record will be cleaned up later")
			}
		}
	}()
	fingerprint := struct {
		ContentType string
		Digest      [sha256.Size]byte
	}{contentType, sha256.Sum256(data)}
	return inTransaction(ctx, s.pool, func(tx pgx.Tx) (Receipt, error) {
		if err := requireExpenseEditor(ctx, tx, userID, tripID, expenseID); err != nil {
			return Receipt{}, err
		}
		return idempotency.Do(ctx, tx, userID, key, "put-receipt/"+tripID+"/"+expenseID, fingerprint, func() (Receipt, error) {
			if err := lockReceiptExpense(ctx, tx, userID, tripID, expenseID); err != nil {
				return Receipt{}, err
			}
			var staged string
			if err := tx.QueryRow(ctx, `SELECT object_key FROM receipt_cleanup WHERE object_key = $1 FOR UPDATE`, objectKey).Scan(&staged); err != nil {
				return Receipt{}, err
			}
			attemptedUpload = true
			if err := s.receipts.Put(ctx, objectKey, contentType, data); err != nil {
				return Receipt{}, ErrReceiptStorage
			}
			if _, err := detachReceipt(ctx, tx, expenseID); err != nil {
				return Receipt{}, err
			}
			receipt := Receipt{ContentType: contentType, SizeBytes: int64(len(data))}
			err := tx.QueryRow(ctx, `INSERT INTO receipts (expense_id, trip_id, object_key, content_type, size_bytes)
				VALUES ($1::text::uuid, $2::text::uuid, $3, $4, $5) RETURNING updated_at`, expenseID, tripID, objectKey, contentType, receipt.SizeBytes).
				Scan(&receipt.UpdatedAt)
			if err != nil {
				return Receipt{}, err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM receipt_cleanup WHERE object_key = $1`, objectKey); err != nil {
				return Receipt{}, err
			}
			if err := bumpSnapshotRevision(ctx, tx, tripID); err != nil {
				return Receipt{}, err
			}
			receipt.UpdatedAt = receipt.UpdatedAt.UTC()
			return receipt, nil
		})
	})
}

func (s *Service) DeleteReceipt(ctx context.Context, userID, tripID, expenseID, key string) error {
	_, err := inTransaction(ctx, s.pool, func(tx pgx.Tx) (bool, error) {
		if err := requireExpenseEditor(ctx, tx, userID, tripID, expenseID); err != nil {
			return false, err
		}
		return idempotency.Do(ctx, tx, userID, key, "delete-receipt/"+tripID+"/"+expenseID, nil, func() (bool, error) {
			if err := lockReceiptExpense(ctx, tx, userID, tripID, expenseID); err != nil {
				return false, err
			}
			removed, err := detachReceipt(ctx, tx, expenseID)
			if err != nil {
				return false, err
			}
			if removed {
				if err := bumpSnapshotRevision(ctx, tx, tripID); err != nil {
					return false, err
				}
			}
			return true, nil
		})
	})
	return err
}

func lockReceiptExpense(ctx context.Context, tx pgx.Tx, userID, tripID, expenseID string) error {
	if _, err := getTrip(ctx, tx, userID, tripID, true); err != nil {
		return err
	}
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM expenses
		WHERE id = $1::text::uuid AND trip_id = $2::text::uuid AND deleted_at IS NULL)`, expenseID, tripID).Scan(&exists)
	if err == nil && !exists {
		return ErrExpenseNotFound
	}
	return err
}

func detachReceipt(ctx context.Context, tx pgx.Tx, expenseID string) (bool, error) {
	var objectKey string
	err := tx.QueryRow(ctx, `DELETE FROM receipts WHERE expense_id = $1::text::uuid RETURNING object_key`, expenseID).Scan(&objectKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO receipt_cleanup (object_key, delete_after)
		VALUES ($1, clock_timestamp() + interval '1 minute')`, objectKey)
	return err == nil, err
}

func (s *Service) RunReceiptCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := s.CleanupReceipts(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("receipt cleanup will be retried")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) CleanupReceipts(ctx context.Context) error {
	for range 16 {
		attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
		deleted, err := inTransaction(attempt, s.pool, func(tx pgx.Tx) (bool, error) {
			var key string
			err := tx.QueryRow(attempt, `SELECT object_key FROM receipt_cleanup WHERE delete_after <= clock_timestamp()
				ORDER BY delete_after, object_key LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&key)
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			objectCtx, stop := context.WithTimeout(attempt, 5*time.Second)
			err = s.receipts.Delete(objectCtx, key)
			stop()
			if err != nil {
				slog.Warn("receipt object deletion deferred")
				_, err := tx.Exec(attempt, `UPDATE receipt_cleanup SET delete_after = clock_timestamp() + interval '5 minutes' WHERE object_key = $1`, key)
				return true, err
			}
			_, err = tx.Exec(attempt, `DELETE FROM receipt_cleanup WHERE object_key = $1`, key)
			return true, err
		})
		cancel()
		if err != nil || !deleted {
			return err
		}
	}
	return nil
}
