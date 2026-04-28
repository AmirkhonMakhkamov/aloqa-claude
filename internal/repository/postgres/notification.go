package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/service/notification"
)

// NotificationRepo implements notification.Store using PostgreSQL.
type NotificationRepo struct {
	pool *pgxpool.Pool
}

// NewNotificationRepo creates a new NotificationRepo.
func NewNotificationRepo(pool *pgxpool.Pool) notification.Store {
	return &NotificationRepo{pool: pool}
}

func (r *NotificationRepo) Save(ctx context.Context, n *notification.Notification) error {
	query := `
		INSERT INTO notifications (id, user_id, workspace_id, type, title, body, resource_id, read, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	_, err := r.pool.Exec(ctx, query,
		n.ID, n.UserID, n.WorkspaceID, n.Type,
		n.Title, n.Body, n.ResourceID, n.Read, n.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: save notification: %w", err)
	}
	return nil
}

func (r *NotificationRepo) ListByUser(ctx context.Context, userID, workspaceID uuid.UUID, limit int) ([]notification.Notification, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT id, user_id, workspace_id, type, title, body, resource_id, read, created_at
		FROM notifications
		WHERE user_id = $1 AND workspace_id = $2
		ORDER BY created_at DESC
		LIMIT $3`

	rows, err := r.pool.Query(ctx, query, userID, workspaceID, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list notifications: %w", err)
	}
	defer rows.Close()

	var notifications []notification.Notification
	for rows.Next() {
		var n notification.Notification
		if err := rows.Scan(
			&n.ID, &n.UserID, &n.WorkspaceID, &n.Type,
			&n.Title, &n.Body, &n.ResourceID, &n.Read, &n.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan notification: %w", err)
		}
		notifications = append(notifications, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate notifications: %w", err)
	}
	return notifications, nil
}

func (r *NotificationRepo) MarkRead(ctx context.Context, notificationID, userID uuid.UUID) error {
	query := `UPDATE notifications SET read = TRUE WHERE id = $1 AND user_id = $2`
	tag, err := r.pool.Exec(ctx, query, notificationID, userID)
	if err != nil {
		return fmt.Errorf("postgres: mark notification read: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("notification not found")
	}
	return nil
}

func (r *NotificationRepo) MarkAllRead(ctx context.Context, userID, workspaceID uuid.UUID) error {
	query := `UPDATE notifications SET read = TRUE WHERE user_id = $1 AND workspace_id = $2 AND read = FALSE`
	_, err := r.pool.Exec(ctx, query, userID, workspaceID)
	if err != nil {
		return fmt.Errorf("postgres: mark all notifications read: %w", err)
	}
	return nil
}

func (r *NotificationRepo) CountUnread(ctx context.Context, userID, workspaceID uuid.UUID) (int, error) {
	query := `SELECT COUNT(*) FROM notifications WHERE user_id = $1 AND workspace_id = $2 AND read = FALSE`
	var count int
	if err := r.pool.QueryRow(ctx, query, userID, workspaceID).Scan(&count); err != nil {
		return 0, fmt.Errorf("postgres: count unread notifications: %w", err)
	}
	return count, nil
}
