package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrKeyReused = errors.New("idempotency key already used for a different request")

func Do[T any](ctx context.Context, tx pgx.Tx, userID, key, operation string, input any, write func() (T, error)) (T, error) {
	var result T
	hash, err := requestHash(operation, input)
	if err != nil {
		return result, err
	}
	claim, err := tx.Exec(ctx, `
		INSERT INTO idempotency_keys (user_id, key, request_hash)
		VALUES ($1::text::uuid, $2::text::uuid, $3)
		ON CONFLICT (user_id, key) DO NOTHING`, userID, key, hash[:])
	if err != nil {
		return result, err
	}
	if claim.RowsAffected() == 0 {
		result, _, err := loadReplay[T](ctx, tx, userID, key, hash)
		return result, err
	}
	result, err = write()
	if err != nil {
		return result, err
	}
	response, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	_, err = tx.Exec(ctx, `UPDATE idempotency_keys SET response_body = $3
		WHERE user_id = $1::text::uuid AND key = $2::text::uuid`, userID, key, response)
	return result, err
}

func Replay[T any](ctx context.Context, tx pgx.Tx, userID, key, operation string, input any) (T, bool, error) {
	hash, err := requestHash(operation, input)
	if err != nil {
		var zero T
		return zero, false, err
	}
	return loadReplay[T](ctx, tx, userID, key, hash)
}

func requestHash(operation string, input any) ([sha256.Size]byte, error) {
	request, err := json.Marshal([]any{operation, input})
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(request), nil
}

func loadReplay[T any](ctx context.Context, tx pgx.Tx, userID, key string, hash [sha256.Size]byte) (T, bool, error) {
	var result T
	var storedHash, response []byte
	err := tx.QueryRow(ctx, `SELECT request_hash, response_body FROM idempotency_keys
		WHERE user_id = $1::text::uuid AND key = $2::text::uuid`, userID, key).Scan(&storedHash, &response)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	if !bytes.Equal(storedHash, hash[:]) {
		return result, false, ErrKeyReused
	}
	err = json.Unmarshal(response, &result)
	return result, err == nil, err
}
