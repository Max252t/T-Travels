package trips

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"strings"
	"time"

	"github.com/Max252t/T-Travels/backend/internal/idempotency"
	"github.com/jackc/pgx/v5"
)

type Invitation struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Service) Invite(ctx context.Context, userID, tripID, key string) (Invitation, error) {
	return inTransaction(ctx, s.pool, func(tx pgx.Tx) (Invitation, error) {
		if err := requireOwner(ctx, tx, userID, tripID); err != nil {
			return Invitation{}, err
		}
		return idempotency.Do(ctx, tx, userID, key, "invite/"+tripID, nil, func() (Invitation, error) {
			invitation := Invitation{Code: rand.Text()}
			hash := sha256.Sum256([]byte(invitation.Code))
			err := tx.QueryRow(ctx, `INSERT INTO invitations (trip_id, created_by, code_hash, expires_at)
				VALUES ($1::text::uuid, $2::text::uuid, $3, now() + interval '168 hours')
				RETURNING id::text, expires_at`, tripID, userID, hash[:]).Scan(&invitation.ID, &invitation.ExpiresAt)
			invitation.ExpiresAt = invitation.ExpiresAt.UTC()
			return invitation, err
		})
	})
}

func (s *Service) Join(ctx context.Context, userID, key, code string) (Trip, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if !validCode(code) {
		return Trip{}, ErrInvitation
	}
	if !s.joins.Allow() {
		return Trip{}, ErrRateLimit
	}
	hash := sha256.Sum256([]byte(code))
	return inTransaction(ctx, s.pool, func(tx pgx.Tx) (Trip, error) {
		result, err := idempotency.Do(ctx, tx, userID, key, "join-trip", hash, func() (Trip, error) {
			var tripID string
			err := tx.QueryRow(ctx, `SELECT t.id::text FROM invitations i JOIN trips t ON t.id = i.trip_id
				WHERE i.code_hash = $1 AND i.expires_at > now() FOR UPDATE OF t`, hash[:]).Scan(&tripID)
			if errors.Is(err, pgx.ErrNoRows) {
				return Trip{}, ErrInvitation
			}
			if err != nil {
				return Trip{}, err
			}
			change, err := tx.Exec(ctx, `INSERT INTO trip_members (trip_id, user_id, role)
				VALUES ($1::text::uuid, $2::text::uuid, 'member')
				ON CONFLICT (trip_id, user_id) DO UPDATE SET left_at = NULL, joined_at = now()
				WHERE trip_members.left_at IS NOT NULL`, tripID, userID)
			if err != nil {
				return Trip{}, err
			}
			if change.RowsAffected() == 1 {
				if err := notifyMemberJoined(ctx, tx, tripID, userID); err != nil {
					return Trip{}, err
				}
			}
			return getTrip(ctx, tx, userID, tripID, false)
		})
		if err != nil {
			return Trip{}, err
		}
		if _, err := getTrip(ctx, tx, userID, result.ID, false); err != nil {
			return Trip{}, err
		}
		return result, nil
	})
}

func validCode(code string) bool {
	if len(code) != 26 {
		return false
	}
	for _, char := range code {
		if (char < 'A' || char > 'Z') && (char < '2' || char > '7') {
			return false
		}
	}
	return true
}
