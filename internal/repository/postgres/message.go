package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
)

// MessageRepo implements repository.MessageRepository using PostgreSQL.
type MessageRepo struct {
	pool *pgxpool.Pool
	db   queryable
}

type nullableUserScan struct {
	id                *uuid.UUID
	email             *string
	displayName       *string
	avatarURL         *string
	position          *string
	department        *string
	passwordHash      *string
	status            *entity.UserStatus
	deactivatedAt     *time.Time
	savedMessagesMode *entity.SavedMessagesMode
	locale            *string
	createdAt         *time.Time
	updatedAt         *time.Time
}

func (u nullableUserScan) user() *entity.User {
	if u.id == nil {
		return nil
	}

	user := &entity.User{
		ID:           *u.id,
		Email:        derefStr(u.email),
		DisplayName:  derefStr(u.displayName),
		AvatarURL:    derefStr(u.avatarURL),
		Position:     u.position,
		Department:   u.department,
		PasswordHash: derefStr(u.passwordHash),
		Locale:       derefStr(u.locale),
	}
	if u.status != nil {
		user.Status = *u.status
	}
	if u.deactivatedAt != nil {
		user.DeactivatedAt = u.deactivatedAt
	}
	if u.savedMessagesMode != nil {
		user.SavedMessagesMode = *u.savedMessagesMode
	}
	if user.SavedMessagesMode == "" {
		user.SavedMessagesMode = entity.SavedMessagesModePerWorkspace
	}
	if u.createdAt != nil {
		user.CreatedAt = *u.createdAt
	}
	if u.updatedAt != nil {
		user.UpdatedAt = *u.updatedAt
	}
	return user
}

// NewMessageRepo creates a new MessageRepo.
func NewMessageRepo(pool *pgxpool.Pool) *MessageRepo {
	return &MessageRepo{pool: pool, db: pool}
}

func (r *MessageRepo) withTx(tx pgx.Tx) *MessageRepo {
	if r == nil {
		return nil
	}
	return &MessageRepo{pool: r.pool, db: tx}
}

func (r *MessageRepo) Create(ctx context.Context, msg *entity.Message) error {
	query := `
		INSERT INTO messages (id, channel_id, user_id, parent_id, content, type, edited, edited_at, pinned, pinned_by, pinned_at, forwarded_from, saved_from, quoted_message_id, quoted_snapshot, created_at, updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`

	_, err := r.db.Exec(ctx, query,
		msg.ID,
		msg.ChannelID,
		msg.UserID,
		msg.ParentID,
		msg.Content,
		msg.Type,
		msg.Edited,
		msg.EditedAt,
		msg.Pinned,
		msg.PinnedBy,
		msg.PinnedAt,
		msg.ForwardedFrom,
		msg.SavedFrom,
		msg.QuotedMessageID,
		msg.QuotedSnapshot,
		msg.CreatedAt,
		msg.UpdatedAt,
		msg.DeletedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: create message: %w", err)
	}

	return nil
}

func (r *MessageRepo) GetByID(ctx context.Context, id uuid.UUID) (*entity.Message, error) {
	query := `
		SELECT
			m.id, m.channel_id, m.user_id, m.parent_id, m.content, m.type,
			m.edited, m.edited_at, m.pinned, m.pinned_by, m.pinned_at,
			m.forwarded_from, m.saved_from, m.quoted_message_id, m.quoted_snapshot,
			m.created_at, m.updated_at, m.deleted_at,
			u.id, u.email, u.display_name, u.avatar_url, u.position, u.department, u.password_hash, u.status,
			u.deactivated_at, u.saved_messages_mode, u.locale, u.created_at, u.updated_at
		FROM messages m
		LEFT JOIN users u ON u.id = m.user_id
		WHERE m.id = $1`

	msg := &entity.Message{}
	var user nullableUserScan

	err := r.db.QueryRow(ctx, query, id).Scan(
		&msg.ID,
		&msg.ChannelID,
		&msg.UserID,
		&msg.ParentID,
		&msg.Content,
		&msg.Type,
		&msg.Edited,
		&msg.EditedAt,
		&msg.Pinned,
		&msg.PinnedBy,
		&msg.PinnedAt,
		&msg.ForwardedFrom,
		&msg.SavedFrom,
		&msg.QuotedMessageID,
		&msg.QuotedSnapshot,
		&msg.CreatedAt,
		&msg.UpdatedAt,
		&msg.DeletedAt,
		&user.id,
		&user.email,
		&user.displayName,
		&user.avatarURL,
		&user.position,
		&user.department,
		&user.passwordHash,
		&user.status,
		&user.deactivatedAt,
		&user.savedMessagesMode,
		&user.locale,
		&user.createdAt,
		&user.updatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("message not found")
		}
		return nil, fmt.Errorf("postgres: get message by id: %w", err)
	}

	msg.User = user.user()

	return msg, nil
}

func (r *MessageRepo) ListByChannel(ctx context.Context, channelID uuid.UUID, p pagination.Params) ([]entity.Message, error) {
	p.Normalize()

	var (
		rows pgx.Rows
		err  error
	)

	if p.Cursor != uuid.Nil {
		query := `
			SELECT
				m.id, m.channel_id, m.user_id, m.parent_id, m.content, m.type,
				m.edited, m.edited_at, m.pinned, m.pinned_by, m.pinned_at,
				m.forwarded_from, m.saved_from, m.quoted_message_id, m.quoted_snapshot,
				m.created_at, m.updated_at, m.deleted_at,
				u.id, u.email, u.display_name, u.avatar_url, u.position, u.department, u.password_hash, u.status,
				u.deactivated_at, u.saved_messages_mode, u.locale, u.created_at, u.updated_at
			FROM messages m
			LEFT JOIN users u ON u.id = m.user_id
			WHERE m.channel_id = $1 AND m.id < $2
			ORDER BY m.id DESC
			LIMIT $3`
		rows, err = r.db.Query(ctx, query, channelID, p.Cursor, p.Limit+1)
	} else {
		query := `
			SELECT
				m.id, m.channel_id, m.user_id, m.parent_id, m.content, m.type,
				m.edited, m.edited_at, m.pinned, m.pinned_by, m.pinned_at,
				m.forwarded_from, m.saved_from, m.quoted_message_id, m.quoted_snapshot,
				m.created_at, m.updated_at, m.deleted_at,
				u.id, u.email, u.display_name, u.avatar_url, u.position, u.department, u.password_hash, u.status,
				u.deactivated_at, u.saved_messages_mode, u.locale, u.created_at, u.updated_at
			FROM messages m
			LEFT JOIN users u ON u.id = m.user_id
			WHERE m.channel_id = $1
			ORDER BY m.id DESC
			LIMIT $2`
		rows, err = r.db.Query(ctx, query, channelID, p.Limit+1)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: list messages by channel: %w", err)
	}
	defer rows.Close()

	var messages []entity.Message
	for rows.Next() {
		var msg entity.Message
		var user nullableUserScan

		if err := rows.Scan(
			&msg.ID,
			&msg.ChannelID,
			&msg.UserID,
			&msg.ParentID,
			&msg.Content,
			&msg.Type,
			&msg.Edited,
			&msg.EditedAt,
			&msg.Pinned,
			&msg.PinnedBy,
			&msg.PinnedAt,
			&msg.ForwardedFrom,
			&msg.SavedFrom,
			&msg.QuotedMessageID,
			&msg.QuotedSnapshot,
			&msg.CreatedAt,
			&msg.UpdatedAt,
			&msg.DeletedAt,
			&user.id,
			&user.email,
			&user.displayName,
			&user.avatarURL,
			&user.position,
			&user.department,
			&user.passwordHash,
			&user.status,
			&user.deactivatedAt,
			&user.savedMessagesMode,
			&user.locale,
			&user.createdAt,
			&user.updatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list messages by channel scan: %w", err)
		}

		msg.User = user.user()

		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list messages by channel rows: %w", err)
	}

	if len(messages) > p.Limit {
		messages = messages[:p.Limit]
	}

	return messages, nil
}

func (r *MessageRepo) HasActiveMessage(ctx context.Context, channelID uuid.UUID) (bool, error) {
	query := `
		SELECT EXISTS(
			SELECT 1
			FROM messages
			WHERE channel_id = $1 AND deleted_at IS NULL
		)`

	var exists bool
	if err := r.db.QueryRow(ctx, query, channelID).Scan(&exists); err != nil {
		return false, fmt.Errorf("postgres: probe active message: %w", err)
	}
	return exists, nil
}

func (r *MessageRepo) ListThreadReplies(ctx context.Context, parentID uuid.UUID, p pagination.Params) ([]entity.Message, error) {
	p.Normalize()

	var (
		rows pgx.Rows
		err  error
	)

	if p.Cursor != uuid.Nil {
		query := `
			SELECT
				m.id, m.channel_id, m.user_id, m.parent_id, m.content, m.type,
				m.edited, m.edited_at, m.pinned, m.pinned_by, m.pinned_at,
				m.forwarded_from, m.saved_from, m.quoted_message_id, m.quoted_snapshot,
				m.created_at, m.updated_at, m.deleted_at,
				u.id, u.email, u.display_name, u.avatar_url, u.position, u.department, u.password_hash, u.status,
				u.deactivated_at, u.saved_messages_mode, u.locale, u.created_at, u.updated_at
			FROM messages m
			LEFT JOIN users u ON u.id = m.user_id
			WHERE m.parent_id = $1 AND m.id < $2
			ORDER BY m.id DESC
			LIMIT $3`
		rows, err = r.db.Query(ctx, query, parentID, p.Cursor, p.Limit+1)
	} else {
		query := `
			SELECT
				m.id, m.channel_id, m.user_id, m.parent_id, m.content, m.type,
				m.edited, m.edited_at, m.pinned, m.pinned_by, m.pinned_at,
				m.forwarded_from, m.saved_from, m.quoted_message_id, m.quoted_snapshot,
				m.created_at, m.updated_at, m.deleted_at,
				u.id, u.email, u.display_name, u.avatar_url, u.position, u.department, u.password_hash, u.status,
				u.deactivated_at, u.saved_messages_mode, u.locale, u.created_at, u.updated_at
			FROM messages m
			LEFT JOIN users u ON u.id = m.user_id
			WHERE m.parent_id = $1
			ORDER BY m.id DESC
			LIMIT $2`
		rows, err = r.db.Query(ctx, query, parentID, p.Limit+1)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: list thread replies: %w", err)
	}
	defer rows.Close()

	var messages []entity.Message
	for rows.Next() {
		var msg entity.Message
		var user nullableUserScan

		if err := rows.Scan(
			&msg.ID,
			&msg.ChannelID,
			&msg.UserID,
			&msg.ParentID,
			&msg.Content,
			&msg.Type,
			&msg.Edited,
			&msg.EditedAt,
			&msg.Pinned,
			&msg.PinnedBy,
			&msg.PinnedAt,
			&msg.ForwardedFrom,
			&msg.SavedFrom,
			&msg.QuotedMessageID,
			&msg.QuotedSnapshot,
			&msg.CreatedAt,
			&msg.UpdatedAt,
			&msg.DeletedAt,
			&user.id,
			&user.email,
			&user.displayName,
			&user.avatarURL,
			&user.position,
			&user.department,
			&user.passwordHash,
			&user.status,
			&user.deactivatedAt,
			&user.savedMessagesMode,
			&user.locale,
			&user.createdAt,
			&user.updatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list thread replies scan: %w", err)
		}

		msg.User = user.user()

		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list thread replies rows: %w", err)
	}

	if len(messages) > p.Limit {
		messages = messages[:p.Limit]
	}

	return messages, nil
}

func (r *MessageRepo) Update(ctx context.Context, msg *entity.Message) error {
	now := time.Now().UTC()
	query := `
		UPDATE messages
		SET content = $2, edited = true, edited_at = $3, updated_at = $3
		WHERE id = $1 AND deleted_at IS NULL`

	tag, err := r.db.Exec(ctx, query, msg.ID, msg.Content, now)
	if err != nil {
		return fmt.Errorf("postgres: update message: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("message not found")
	}

	msg.Edited = true
	msg.EditedAt = &now
	msg.UpdatedAt = now

	return nil
}

func (r *MessageRepo) Move(ctx context.Context, msg *entity.Message) error {
	now := time.Now().UTC()
	query := `
		UPDATE messages
		SET channel_id = $2,
			parent_id = $3,
			pinned = false,
			pinned_by = NULL,
			pinned_at = NULL,
			updated_at = $4
		WHERE id = $1 AND deleted_at IS NULL`

	tag, err := r.db.Exec(ctx, query, msg.ID, msg.ChannelID, msg.ParentID, now)
	if err != nil {
		return fmt.Errorf("postgres: move message: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("message not found")
	}

	msg.Pinned = false
	msg.PinnedBy = nil
	msg.PinnedAt = nil
	msg.UpdatedAt = now
	return nil
}

func (r *MessageRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC()
	query := `
		UPDATE messages
		SET content = '',
			edited = false,
			edited_at = NULL,
			pinned = false,
			pinned_by = NULL,
			pinned_at = NULL,
			forwarded_from = NULL,
			quoted_message_id = NULL,
			quoted_snapshot = NULL,
			updated_at = $2,
			deleted_at = $2
		WHERE id = $1 AND deleted_at IS NULL`

	tag, err := r.db.Exec(ctx, query, id, now)
	if err != nil {
		return fmt.Errorf("postgres: soft delete message: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("message not found")
	}

	return nil
}

func (r *MessageRepo) SoftDeleteWithCascade(ctx context.Context, id uuid.UUID) ([]uuid.UUID, error) {
	now := time.Now().UTC()
	query := `
		UPDATE messages
		SET content = '',
			edited = false,
			edited_at = NULL,
			pinned = false,
			pinned_by = NULL,
			pinned_at = NULL,
			forwarded_from = NULL,
			quoted_message_id = NULL,
			quoted_snapshot = NULL,
			updated_at = $2,
			deleted_at = $2
		WHERE id = $1 AND deleted_at IS NULL`

	tag, err := r.db.Exec(ctx, query, id, now)
	if err != nil {
		return nil, fmt.Errorf("postgres: soft delete message: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, cerrors.NotFound("message not found")
	}

	cascadeQuery := `
		UPDATE messages
		SET quoted_snapshot = jsonb_set(COALESCE(quoted_snapshot, '{}'::jsonb), '{deleted}', 'true'::jsonb, true),
			updated_at = $2
		WHERE quoted_message_id = $1
		RETURNING id`

	rows, err := r.db.Query(ctx, cascadeQuery, id, now)
	if err != nil {
		return nil, fmt.Errorf("postgres: cascade quoted message soft delete: %w", err)
	}
	defer rows.Close()

	var affected []uuid.UUID
	for rows.Next() {
		var affectedID uuid.UUID
		if err := rows.Scan(&affectedID); err != nil {
			return nil, fmt.Errorf("postgres: cascade quoted message scan: %w", err)
		}
		affected = append(affected, affectedID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: cascade quoted message rows: %w", err)
	}

	return affected, nil
}

// --- Pin methods ---

func (r *MessageRepo) Pin(ctx context.Context, messageID, userID uuid.UUID) error {
	now := time.Now().UTC()
	query := `
		UPDATE messages
		SET pinned = true, pinned_by = $2, pinned_at = $3, updated_at = $3
		WHERE id = $1 AND deleted_at IS NULL`

	tag, err := r.db.Exec(ctx, query, messageID, userID, now)
	if err != nil {
		return fmt.Errorf("postgres: pin message: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("message not found")
	}

	return nil
}

func (r *MessageRepo) Unpin(ctx context.Context, messageID uuid.UUID) error {
	now := time.Now().UTC()
	query := `
		UPDATE messages
		SET pinned = false, pinned_by = NULL, pinned_at = NULL, updated_at = $2
		WHERE id = $1 AND deleted_at IS NULL`

	tag, err := r.db.Exec(ctx, query, messageID, now)
	if err != nil {
		return fmt.Errorf("postgres: unpin message: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("message not found")
	}

	return nil
}

func (r *MessageRepo) ListPinned(ctx context.Context, channelID uuid.UUID) ([]entity.Message, error) {
	query := `
		SELECT
			m.id, m.channel_id, m.user_id, m.parent_id, m.content, m.type,
			m.edited, m.edited_at, m.pinned, m.pinned_by, m.pinned_at,
			m.forwarded_from, m.saved_from, m.quoted_message_id, m.quoted_snapshot,
			m.created_at, m.updated_at, m.deleted_at
		FROM messages m
		WHERE m.channel_id = $1 AND m.pinned = true AND m.deleted_at IS NULL
		ORDER BY m.pinned_at DESC`

	rows, err := r.db.Query(ctx, query, channelID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list pinned messages: %w", err)
	}
	defer rows.Close()

	var messages []entity.Message
	for rows.Next() {
		var msg entity.Message
		if err := rows.Scan(
			&msg.ID,
			&msg.ChannelID,
			&msg.UserID,
			&msg.ParentID,
			&msg.Content,
			&msg.Type,
			&msg.Edited,
			&msg.EditedAt,
			&msg.Pinned,
			&msg.PinnedBy,
			&msg.PinnedAt,
			&msg.ForwardedFrom,
			&msg.SavedFrom,
			&msg.QuotedMessageID,
			&msg.QuotedSnapshot,
			&msg.CreatedAt,
			&msg.UpdatedAt,
			&msg.DeletedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list pinned messages scan: %w", err)
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list pinned messages rows: %w", err)
	}

	return messages, nil
}

// ListMentions returns recent @-mentions of the calling user across every
// channel the user is a member of in the given workspace.
//
// The mention syntax stored in `messages.content` is `@<email-local-part>`
// (composer-web `mention-transformer.ts`). We derive the local-part from the
// caller's `users.email` on the fly. Self-mentions and tombstoned messages are
// excluded. The query intentionally avoids the notifications table — that
// path is currently unused for chat mentions; this is the express read model.
func (r *MessageRepo) ListMentions(
	ctx context.Context,
	workspaceID, userID uuid.UUID,
	limit int,
) ([]entity.MentionRow, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		WITH caller AS (
			SELECT id, LOWER(SPLIT_PART(email, '@', 1)) AS handle
			FROM users
			WHERE id = $2
		)
		SELECT
			m.id,
			m.channel_id,
			c.name,
			c.type,
			c.archived,
			u.id,
			u.display_name,
			COALESCE(u.avatar_url, ''),
			u.email,
			m.content,
			m.created_at,
			m.parent_id
		FROM messages m
		INNER JOIN channels c ON c.id = m.channel_id
		INNER JOIN channel_members cm ON cm.channel_id = c.id AND cm.user_id = $2
		INNER JOIN users u ON u.id = m.user_id
		INNER JOIN caller ON TRUE
		WHERE c.workspace_id = $1
		  AND m.deleted_at IS NULL
		  AND m.user_id <> caller.id
		  AND caller.handle <> ''
		  AND m.content ~* ('(^|[^A-Za-z0-9_])@' || caller.handle || '([^A-Za-z0-9_.-]|$)')
		ORDER BY m.created_at DESC
		LIMIT $3`

	rows, err := r.db.Query(ctx, query, workspaceID, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list mentions: %w", err)
	}
	defer rows.Close()

	mentions := make([]entity.MentionRow, 0)
	for rows.Next() {
		var row entity.MentionRow
		if err := rows.Scan(
			&row.MessageID,
			&row.ChannelID,
			&row.ChannelName,
			&row.ChannelType,
			&row.ChannelArchived,
			&row.AuthorID,
			&row.AuthorName,
			&row.AuthorAvatarURL,
			&row.AuthorEmail,
			&row.Body,
			&row.CreatedAt,
			&row.ParentMessageID,
		); err != nil {
			return nil, fmt.Errorf("postgres: list mentions scan: %w", err)
		}
		mentions = append(mentions, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list mentions rows: %w", err)
	}

	return mentions, nil
}

// --- Reaction methods ---

func (r *MessageRepo) AddReaction(ctx context.Context, reaction *entity.Reaction) error {
	query := `
		INSERT INTO reactions (id, message_id, user_id, emoji, created_at)
		VALUES ($1, $2, $3, $4, $5)`

	_, err := r.db.Exec(ctx, query,
		reaction.ID,
		reaction.MessageID,
		reaction.UserID,
		reaction.Emoji,
		reaction.CreatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return cerrors.AlreadyExists("reaction already exists")
		}
		return fmt.Errorf("postgres: add reaction: %w", err)
	}

	return nil
}

func (r *MessageRepo) GetReactionByID(ctx context.Context, id uuid.UUID) (*entity.Reaction, error) {
	query := `
		SELECT id, message_id, user_id, emoji, created_at
		FROM reactions
		WHERE id = $1`

	var reaction entity.Reaction
	if err := r.db.QueryRow(ctx, query, id).Scan(
		&reaction.ID,
		&reaction.MessageID,
		&reaction.UserID,
		&reaction.Emoji,
		&reaction.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("reaction not found")
		}
		return nil, fmt.Errorf("postgres: get reaction by id: %w", err)
	}

	return &reaction, nil
}

func (r *MessageRepo) GetReactionByMessageUserEmoji(ctx context.Context, messageID, userID uuid.UUID, emoji string) (*entity.Reaction, error) {
	query := `
		SELECT id, message_id, user_id, emoji, created_at
		FROM reactions
		WHERE message_id = $1 AND user_id = $2 AND emoji = $3`

	var reaction entity.Reaction
	if err := r.db.QueryRow(ctx, query, messageID, userID, emoji).Scan(
		&reaction.ID,
		&reaction.MessageID,
		&reaction.UserID,
		&reaction.Emoji,
		&reaction.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("reaction not found")
		}
		return nil, fmt.Errorf("postgres: get reaction by message user emoji: %w", err)
	}

	return &reaction, nil
}

func (r *MessageRepo) RemoveReaction(ctx context.Context, messageID, userID uuid.UUID, emoji string) error {
	query := `
		DELETE FROM reactions
		WHERE message_id = $1 AND user_id = $2 AND emoji = $3`

	tag, err := r.db.Exec(ctx, query, messageID, userID, emoji)
	if err != nil {
		return fmt.Errorf("postgres: remove reaction: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("reaction not found")
	}

	return nil
}

func (r *MessageRepo) RemoveReactionByID(ctx context.Context, id uuid.UUID) error {
	query := `
		DELETE FROM reactions
		WHERE id = $1`

	tag, err := r.db.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("postgres: remove reaction by id: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("reaction not found")
	}

	return nil
}

func (r *MessageRepo) ListReactions(ctx context.Context, messageID uuid.UUID) ([]entity.Reaction, error) {
	query := `
		SELECT id, message_id, user_id, emoji, created_at
		FROM reactions
		WHERE message_id = $1
		ORDER BY created_at`

	rows, err := r.db.Query(ctx, query, messageID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list reactions: %w", err)
	}
	defer rows.Close()

	var reactions []entity.Reaction
	for rows.Next() {
		var reaction entity.Reaction
		if err := rows.Scan(
			&reaction.ID,
			&reaction.MessageID,
			&reaction.UserID,
			&reaction.Emoji,
			&reaction.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list reactions scan: %w", err)
		}
		reactions = append(reactions, reaction)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list reactions rows: %w", err)
	}

	return reactions, nil
}

// --- Attachment methods ---

func (r *MessageRepo) CreateAttachment(ctx context.Context, a *entity.Attachment) error {
	query := `
		INSERT INTO attachments (id, message_id, file_name, file_size, mime_type, storage_path, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err := r.db.Exec(ctx, query,
		a.ID,
		a.MessageID,
		a.FileName,
		a.FileSize,
		a.MimeType,
		a.StoragePath,
		a.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: create attachment: %w", err)
	}

	return nil
}

func (r *MessageRepo) DeleteAttachment(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM attachments WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete attachment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("attachment not found")
	}
	return nil
}

func (r *MessageRepo) GetAttachmentByStoragePath(ctx context.Context, storagePath string) (*entity.Attachment, error) {
	query := `
		SELECT id, message_id, file_name, file_size, mime_type, storage_path, created_at
		FROM attachments
		WHERE storage_path = $1`

	var a entity.Attachment
	if err := r.db.QueryRow(ctx, query, storagePath).Scan(
		&a.ID,
		&a.MessageID,
		&a.FileName,
		&a.FileSize,
		&a.MimeType,
		&a.StoragePath,
		&a.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("attachment not found")
		}
		return nil, fmt.Errorf("postgres: get attachment by storage path: %w", err)
	}
	a.URL = "/files/" + a.StoragePath
	return &a, nil
}

func (r *MessageRepo) ListAttachments(ctx context.Context, messageID uuid.UUID) ([]entity.Attachment, error) {
	query := `
		SELECT id, message_id, file_name, file_size, mime_type, storage_path, created_at
		FROM attachments
		WHERE message_id = $1
		ORDER BY created_at`

	rows, err := r.db.Query(ctx, query, messageID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list attachments: %w", err)
	}
	defer rows.Close()

	var attachments []entity.Attachment
	for rows.Next() {
		var a entity.Attachment
		if err := rows.Scan(
			&a.ID,
			&a.MessageID,
			&a.FileName,
			&a.FileSize,
			&a.MimeType,
			&a.StoragePath,
			&a.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list attachments scan: %w", err)
		}
		a.URL = "/files/" + a.StoragePath
		attachments = append(attachments, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list attachments rows: %w", err)
	}

	return attachments, nil
}

func (r *MessageRepo) CountUnread(ctx context.Context, channelID, userID uuid.UUID, since time.Time) (int, error) {
	query := `
		SELECT COUNT(*)
		FROM messages
		WHERE channel_id = $1 AND user_id != $2 AND created_at > $3 AND deleted_at IS NULL`

	var count int
	err := r.db.QueryRow(ctx, query, channelID, userID, since).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("postgres: count unread: %w", err)
	}
	return count, nil
}

// BatchUnreadCounts returns unread counts for every channel the user is a
// member of in the workspace, in a single query. Guest-only channels
// (tracked in channel_access_state rather than channel_members) are not
// covered; callers should merge those separately if they matter.
func (r *MessageRepo) BatchUnreadCounts(ctx context.Context, workspaceID, userID uuid.UUID) ([]repository.UnreadSummary, error) {
	query := `
		SELECT cm.channel_id,
		       cm.last_read_at,
		       COUNT(m.id) AS unread
		FROM channel_members cm
		JOIN channels c ON c.id = cm.channel_id
		LEFT JOIN messages m ON m.channel_id = cm.channel_id
		    AND m.user_id != cm.user_id
		    AND m.created_at > cm.last_read_at
		    AND m.deleted_at IS NULL
		WHERE cm.user_id = $1 AND c.workspace_id = $2
		GROUP BY cm.channel_id, cm.last_read_at`

	rows, err := r.db.Query(ctx, query, userID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("postgres: batch unread counts: %w", err)
	}
	defer rows.Close()

	var out []repository.UnreadSummary
	for rows.Next() {
		var s repository.UnreadSummary
		if err := rows.Scan(&s.ChannelID, &s.LastReadAt, &s.Unread); err != nil {
			return nil, fmt.Errorf("postgres: batch unread counts scan: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: batch unread counts rows: %w", err)
	}
	return out, nil
}

func (r *MessageRepo) CountThreadReplies(ctx context.Context, parentID uuid.UUID) (int, error) {
	query := `
		SELECT COUNT(*)
		FROM messages
		WHERE parent_id = $1 AND deleted_at IS NULL`

	var count int
	err := r.db.QueryRow(ctx, query, parentID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("postgres: count thread replies: %w", err)
	}
	return count, nil
}

// derefStr safely dereferences a *string, returning "" if nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
