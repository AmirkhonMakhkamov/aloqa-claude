package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
)

type SavedRepo struct {
	pool *pgxpool.Pool
	db   queryable
}

func NewSavedRepo(pool *pgxpool.Pool) *SavedRepo {
	return &SavedRepo{pool: pool, db: pool}
}

type SavedMessageItem struct {
	SavedMsgID        uuid.UUID  `json:"saved_msg_id"`
	OriginalMessageID uuid.UUID  `json:"original_message_id"`
	WorkspaceID       *uuid.UUID `json:"workspace_id"`
	CreatedAt         time.Time  `json:"-"`
}

type SavedMessagesPage struct {
	Items      []SavedMessageItem `json:"items"`
	NextCursor *string            `json:"next_cursor"`
}

type savedCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        uuid.UUID `json:"id"`
}

func (r *SavedRepo) ResolveChannel(ctx context.Context, userID uuid.UUID, workspaceID *uuid.UUID, typ entity.ChannelType) (*entity.Channel, error) {
	ch, err := r.lookupChannel(ctx, userID, workspaceID, typ)
	if err == nil {
		return ch, nil
	}
	if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
		return nil, err
	}

	now := time.Now().UTC()
	channelID := uuid.New()
	_, err = r.db.Exec(ctx, `
		INSERT INTO channels (id, workspace_id, name, topic, type, created_by, owner_user_id, archived, created_at, updated_at)
		VALUES ($1, $2, 'Saved Messages', NULL, $3, $4, $4, false, $5, $5)
		ON CONFLICT DO NOTHING`,
		channelID, workspaceID, typ, userID, now)
	if err != nil {
		return nil, fmt.Errorf("postgres: create saved channel: %w", err)
	}

	ch, err = r.lookupChannel(ctx, userID, workspaceID, typ)
	if err != nil {
		return nil, err
	}
	_, err = r.db.Exec(ctx, `
		INSERT INTO channel_members (channel_id, user_id, role, last_read_at, joined_at)
		VALUES ($1, $2, $3, $4, $4)
		ON CONFLICT (channel_id, user_id) DO NOTHING`,
		ch.ID, userID, entity.ChannelRoleOwner, now)
	if err != nil {
		return nil, fmt.Errorf("postgres: ensure saved channel member: %w", err)
	}
	return ch, nil
}

func (r *SavedRepo) lookupChannel(ctx context.Context, userID uuid.UUID, workspaceID *uuid.UUID, typ entity.ChannelType) (*entity.Channel, error) {
	query := `
		SELECT id, workspace_id, name, topic, type, created_by, owner_user_id, archived, created_at, updated_at
		FROM channels
		WHERE owner_user_id = $1
		  AND type = $2
		  AND COALESCE(workspace_id::text, '') = COALESCE($3::text, '')
		LIMIT 1`
	ch := &entity.Channel{}
	err := r.db.QueryRow(ctx, query, userID, typ, workspaceID).Scan(
		&ch.ID,
		&ch.WorkspaceID,
		&ch.Name,
		&ch.Topic,
		&ch.Type,
		&ch.CreatedBy,
		&ch.OwnerUserID,
		&ch.Archived,
		&ch.CreatedAt,
		&ch.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("saved channel not found")
		}
		return nil, fmt.Errorf("postgres: lookup saved channel: %w", err)
	}
	return ch, nil
}

func (r *SavedRepo) FindSavedCopy(ctx context.Context, channelID, originalMessageID uuid.UUID) (*entity.Message, error) {
	msg := &entity.Message{}
	err := r.db.QueryRow(ctx, `
		SELECT id, channel_id, user_id, parent_id, content, type, edited, edited_at, pinned, pinned_by, pinned_at,
		       forwarded_from, saved_from, quoted_message_id, quoted_snapshot, profile_share, created_at, updated_at, deleted_at
		FROM messages
		WHERE channel_id = $1
		  AND saved_from->>'message_id' = $2
		  AND deleted_at IS NULL
		LIMIT 1`,
		channelID, originalMessageID.String()).Scan(
		&msg.ID, &msg.ChannelID, &msg.UserID, &msg.ParentID, &msg.Content, &msg.Type,
		&msg.Edited, &msg.EditedAt, &msg.Pinned, &msg.PinnedBy, &msg.PinnedAt,
		&msg.ForwardedFrom, &msg.SavedFrom, &msg.QuotedMessageID, &msg.QuotedSnapshot, &msg.ProfileShare,
		&msg.CreatedAt, &msg.UpdatedAt, &msg.DeletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("saved copy not found")
		}
		return nil, fmt.Errorf("postgres: find saved copy: %w", err)
	}
	return msg, nil
}

func (r *SavedRepo) ListMessages(ctx context.Context, userID uuid.UUID, mode entity.SavedMessagesMode, workspaceID *uuid.UUID, cursorText string, limit int) (*SavedMessagesPage, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	channelType := entity.ChannelTypeSaved
	if mode == entity.SavedMessagesModeGlobal {
		channelType = entity.ChannelTypeSavedGlobal
	}

	var cursor savedCursor
	hasCursor := false
	if cursorText != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursorText)
		if err != nil {
			return nil, cerrors.InvalidInput("invalid cursor")
		}
		if err := json.Unmarshal(raw, &cursor); err != nil {
			return nil, cerrors.InvalidInput("invalid cursor")
		}
		hasCursor = true
	}

	rows, err := r.db.Query(ctx, `
		SELECT m.id,
		       (m.saved_from->>'message_id')::uuid,
		       NULLIF(m.saved_from->>'workspace_id', '')::uuid,
		       m.created_at
		FROM messages m
		INNER JOIN channels c ON c.id = m.channel_id
		WHERE c.owner_user_id = $1
		  AND c.type = $2
		  AND ($3::uuid IS NULL OR c.workspace_id = $3)
		  AND m.saved_from IS NOT NULL
		  AND m.deleted_at IS NULL
		  AND ($4::boolean = false OR (m.created_at, m.id) < ($5::timestamptz, $6::uuid))
		ORDER BY m.created_at DESC, m.id DESC
		LIMIT $7`,
		userID, channelType, workspaceID, hasCursor, cursor.CreatedAt, cursor.ID, limit+1)
	if err != nil {
		return nil, fmt.Errorf("postgres: list saved messages: %w", err)
	}
	defer rows.Close()

	items := make([]SavedMessageItem, 0, limit)
	for rows.Next() {
		var item SavedMessageItem
		if err := rows.Scan(&item.SavedMsgID, &item.OriginalMessageID, &item.WorkspaceID, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: list saved messages scan: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list saved messages rows: %w", err)
	}

	var next *string
	if len(items) > limit {
		last := items[limit-1]
		items = items[:limit]
		raw, err := json.Marshal(savedCursor{CreatedAt: last.CreatedAt, ID: last.SavedMsgID})
		if err != nil {
			return nil, fmt.Errorf("postgres: encode saved cursor: %w", err)
		}
		encoded := base64.RawURLEncoding.EncodeToString(raw)
		next = &encoded
	}

	return &SavedMessagesPage{Items: items, NextCursor: next}, nil
}
