package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/service/draft"
)

// DraftRepo implements draft.Store using PostgreSQL.
type DraftRepo struct {
	pool *pgxpool.Pool
}

// NewDraftRepo creates a new DraftRepo.
func NewDraftRepo(pool *pgxpool.Pool) draft.Store {
	return &DraftRepo{pool: pool}
}

// Upsert writes the draft, replacing any existing one for the same
// (user, channel, thread root). Branches on the thread root because the unique
// constraint is split into two partial indexes (channel-level vs thread-level).
func (r *DraftRepo) Upsert(ctx context.Context, d *draft.Draft) error {
	if d.ParentMessageID == nil {
		query := `
			INSERT INTO message_drafts (workspace_id, channel_id, user_id, parent_message_id, content, updated_at)
			VALUES ($1, $2, $3, NULL, $4, $5)
			ON CONFLICT (user_id, channel_id) WHERE parent_message_id IS NULL
			DO UPDATE SET content = EXCLUDED.content, updated_at = EXCLUDED.updated_at`
		if _, err := r.pool.Exec(ctx, query, d.WorkspaceID, d.ChannelID, d.UserID, []byte(d.Content), d.UpdatedAt); err != nil {
			return fmt.Errorf("postgres: upsert channel draft: %w", err)
		}
		return nil
	}

	query := `
		INSERT INTO message_drafts (workspace_id, channel_id, user_id, parent_message_id, content, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id, channel_id, parent_message_id) WHERE parent_message_id IS NOT NULL
		DO UPDATE SET content = EXCLUDED.content, updated_at = EXCLUDED.updated_at`
	if _, err := r.pool.Exec(ctx, query, d.WorkspaceID, d.ChannelID, d.UserID, *d.ParentMessageID, []byte(d.Content), d.UpdatedAt); err != nil {
		return fmt.Errorf("postgres: upsert thread draft: %w", err)
	}
	return nil
}

// ListByWorkspaceUser returns all of a user's drafts in a workspace.
func (r *DraftRepo) ListByWorkspaceUser(ctx context.Context, workspaceID, userID uuid.UUID) ([]draft.Draft, error) {
	query := `
		SELECT workspace_id, channel_id, user_id, parent_message_id, content, updated_at
		FROM message_drafts
		WHERE workspace_id = $1 AND user_id = $2
		ORDER BY updated_at DESC`

	rows, err := r.pool.Query(ctx, query, workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list drafts: %w", err)
	}
	defer rows.Close()

	drafts := make([]draft.Draft, 0)
	for rows.Next() {
		var d draft.Draft
		var content []byte
		if err := rows.Scan(
			&d.WorkspaceID,
			&d.ChannelID,
			&d.UserID,
			&d.ParentMessageID,
			&content,
			&d.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list drafts scan: %w", err)
		}
		d.Content = json.RawMessage(content)
		drafts = append(drafts, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list drafts rows: %w", err)
	}
	return drafts, nil
}

// Delete removes a single draft (matching the thread root, NULL-safe).
func (r *DraftRepo) Delete(ctx context.Context, userID, channelID uuid.UUID, parentMessageID *uuid.UUID) error {
	query := `
		DELETE FROM message_drafts
		WHERE user_id = $1 AND channel_id = $2 AND parent_message_id IS NOT DISTINCT FROM $3`
	if _, err := r.pool.Exec(ctx, query, userID, channelID, parentMessageID); err != nil {
		return fmt.Errorf("postgres: delete draft: %w", err)
	}
	return nil
}
