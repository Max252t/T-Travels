package trips

import (
	"context"
	"errors"
	"time"

	"github.com/Max252t/T-Travels/backend/internal/idempotency"
	"github.com/jackc/pgx/v5"
)

var ErrNotificationNotFound = errors.New("notification not found")

type Notification struct {
	ID        string     `json:"id"`
	TripID    string     `json:"trip_id"`
	ActorID   string     `json:"actor_id"`
	EventType string     `json:"event_type"`
	CreatedAt time.Time  `json:"created_at"`
	ReadAt    *time.Time `json:"read_at"`
}

func notifyMemberJoined(ctx context.Context, tx pgx.Tx, tripID, actorID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO notifications (recipient_id, trip_id, actor_id, event_type)
		SELECT user_id, trip_id, $2::text::uuid, 'member_joined' FROM trip_members
		WHERE trip_id = $1::text::uuid AND user_id <> $2::text::uuid AND left_at IS NULL`, tripID, actorID)
	return err
}

func (s *Service) Notifications(ctx context.Context, userID string, limit, offset int32) ([]Notification, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text, trip_id::text, actor_id::text, event_type, created_at, read_at
		FROM notifications WHERE recipient_id = $1::text::uuid
		ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Notification, error) {
		var notification Notification
		err := row.Scan(&notification.ID, &notification.TripID, &notification.ActorID, &notification.EventType, &notification.CreatedAt, &notification.ReadAt)
		notification.CreatedAt = notification.CreatedAt.UTC()
		if notification.ReadAt != nil {
			notification.ReadAt = new(notification.ReadAt.UTC())
		}
		return notification, err
	})
}

func (s *Service) ReadNotification(ctx context.Context, userID, id string) error {
	result, err := s.pool.Exec(ctx, `UPDATE notifications SET read_at = COALESCE(read_at, now())
		WHERE recipient_id = $1::text::uuid AND id = $2::text::uuid`, userID, id)
	if err == nil && result.RowsAffected() == 0 {
		return ErrNotificationNotFound
	}
	return err
}

func (s *Service) ReadAllNotifications(ctx context.Context, userID, key string) error {
	_, err := inTransaction(ctx, s.pool, func(tx pgx.Tx) (bool, error) {
		return idempotency.Do(ctx, tx, userID, key, "read-all-notifications", nil, func() (bool, error) {
			_, err := tx.Exec(ctx, `UPDATE notifications SET read_at = now()
				WHERE recipient_id = $1::text::uuid AND read_at IS NULL`, userID)
			return err == nil, err
		})
	})
	return err
}
