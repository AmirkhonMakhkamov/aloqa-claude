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
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
)

// ChannelRepo implements repository.ChannelRepository using PostgreSQL.
type ChannelRepo struct {
	pool *pgxpool.Pool
	db   queryable
}

// NewChannelRepo creates a new ChannelRepo.
func NewChannelRepo(pool *pgxpool.Pool) *ChannelRepo {
	return &ChannelRepo{pool: pool, db: pool}
}

func (r *ChannelRepo) withTx(tx pgx.Tx) *ChannelRepo {
	if r == nil {
		return nil
	}
	return &ChannelRepo{pool: r.pool, db: tx}
}

func (r *ChannelRepo) Create(ctx context.Context, ch *entity.Channel) error {
	query := `
		INSERT INTO channels (id, workspace_id, name, topic, type, created_by, owner_user_id, archived, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	_, err := r.db.Exec(ctx, query,
		ch.ID,
		ch.WorkspaceID,
		ch.Name,
		ch.Topic,
		ch.Type,
		ch.CreatedBy,
		ch.OwnerUserID,
		ch.Archived,
		ch.CreatedAt,
		ch.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return cerrors.AlreadyExists("channel already exists")
		}
		return fmt.Errorf("postgres: create channel: %w", err)
	}

	return nil
}

func (r *ChannelRepo) GetByID(ctx context.Context, id uuid.UUID) (*entity.Channel, error) {
	query := `
		SELECT id, workspace_id, name, topic, type, created_by, owner_user_id, archived, created_at, updated_at
		FROM channels
		WHERE id = $1`

	ch := &entity.Channel{}
	err := r.db.QueryRow(ctx, query, id).Scan(
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
			return nil, cerrors.NotFound("channel not found")
		}
		return nil, fmt.Errorf("postgres: get channel by id: %w", err)
	}

	return ch, nil
}

func (r *ChannelRepo) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID, p pagination.Params) ([]entity.Channel, error) {
	p.Normalize()

	var (
		rows pgx.Rows
		err  error
	)

	if p.Cursor != uuid.Nil {
		query := `
			SELECT id, workspace_id, name, topic, type, created_by, owner_user_id, archived, created_at, updated_at
			FROM channels
			WHERE workspace_id = $1 AND id < $2 AND type NOT IN ('saved', 'saved_global')
			ORDER BY id DESC
			LIMIT $3`
		rows, err = r.db.Query(ctx, query, workspaceID, p.Cursor, p.Limit+1)
	} else {
		query := `
			SELECT id, workspace_id, name, topic, type, created_by, owner_user_id, archived, created_at, updated_at
			FROM channels
			WHERE workspace_id = $1 AND type NOT IN ('saved', 'saved_global')
			ORDER BY id DESC
			LIMIT $2`
		rows, err = r.db.Query(ctx, query, workspaceID, p.Limit+1)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: list channels by workspace: %w", err)
	}
	defer rows.Close()

	var channels []entity.Channel
	for rows.Next() {
		var ch entity.Channel
		if err := rows.Scan(
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
		); err != nil {
			return nil, fmt.Errorf("postgres: list channels by workspace scan: %w", err)
		}
		channels = append(channels, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list channels by workspace rows: %w", err)
	}

	if len(channels) > p.Limit {
		channels = channels[:p.Limit]
	}

	return channels, nil
}

func (r *ChannelRepo) ListByUser(ctx context.Context, workspaceID, userID uuid.UUID) ([]entity.Channel, error) {
	// Lazy DM visibility: hide DMs from a member if they did not initiate the
	// conversation AND no non-deleted message has been posted yet. The creator
	// always sees the channel (so they can come back to it from their own
	// sidebar after abandoning); the recipient only sees it once a real
	// message lands. Non-DM channels are unaffected.
	query := `
		SELECT c.id, c.workspace_id, c.name, c.topic, c.type, c.created_by, c.owner_user_id, c.archived, c.created_at, c.updated_at
		FROM channels c
		INNER JOIN channel_members cm ON cm.channel_id = c.id
		WHERE c.workspace_id = $1
		  AND cm.user_id = $2
		  AND c.type NOT IN ('saved', 'saved_global')
		  AND (
		    c.type <> 'dm'
		    OR c.created_by = $2
		    OR EXISTS (
		      SELECT 1
		      FROM messages m
		      WHERE m.channel_id = c.id
		        AND m.deleted_at IS NULL
		      LIMIT 1
		    )
		  )
		ORDER BY c.name`

	rows, err := r.db.Query(ctx, query, workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list channels by user: %w", err)
	}
	defer rows.Close()

	var channels []entity.Channel
	for rows.Next() {
		var ch entity.Channel
		if err := rows.Scan(
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
		); err != nil {
			return nil, fmt.Errorf("postgres: list channels by user scan: %w", err)
		}
		channels = append(channels, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list channels by user rows: %w", err)
	}

	return channels, nil
}

func (r *ChannelRepo) Update(ctx context.Context, ch *entity.Channel) error {
	// archived_at follows the archived flag: stamped to now on the first
	// transition into archived, preserved on a redundant write, and cleared
	// when the channel is unarchived (ALK-617).
	query := `
		UPDATE channels
		SET name = $2,
		    topic = $3,
		    archived = $4,
		    archived_at = CASE WHEN $4 THEN COALESCE(archived_at, $5) ELSE NULL END,
		    updated_at = $5
		WHERE id = $1`

	ch.UpdatedAt = time.Now().UTC()

	tag, err := r.db.Exec(ctx, query,
		ch.ID,
		ch.Name,
		ch.Topic,
		ch.Archived,
		ch.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return cerrors.AlreadyExists("channel name already exists")
		}
		return fmt.Errorf("postgres: update channel: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("channel not found")
	}

	return nil
}

func (r *ChannelRepo) Archive(ctx context.Context, id uuid.UUID) error {
	query := `
		UPDATE channels
		SET archived = true,
		    archived_at = COALESCE(archived_at, $2),
		    updated_at = $2
		WHERE id = $1`

	tag, err := r.db.Exec(ctx, query, id, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("postgres: archive channel: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("channel not found")
	}

	return nil
}

// ListArchivedByUser returns archived channels the user is a member of in the
// given workspace, enriched with member count and last-activity timestamp.
// DMs/group DMs/saved channels are excluded — only conventional public/private
// channels can be browsed for unarchive.
func (r *ChannelRepo) ListArchivedByUser(ctx context.Context, workspaceID, userID uuid.UUID) ([]entity.ArchivedChannelInfo, error) {
	query := `
		SELECT c.id, c.workspace_id, c.name, c.topic, c.type, c.created_by, c.owner_user_id,
		       c.archived, c.created_at, c.updated_at, c.archived_at,
		       (SELECT COUNT(*) FROM channel_members cm2 WHERE cm2.channel_id = c.id) AS members_count,
		       (SELECT MAX(m.created_at) FROM messages m WHERE m.channel_id = c.id AND m.deleted_at IS NULL) AS last_activity_at
		FROM channels c
		INNER JOIN channel_members cm ON cm.channel_id = c.id
		WHERE c.workspace_id = $1
		  AND cm.user_id = $2
		  AND c.archived = TRUE
		  AND c.type IN ('public', 'private')
		ORDER BY c.archived_at DESC NULLS LAST, c.name ASC
		LIMIT 200`

	rows, err := r.db.Query(ctx, query, workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list archived channels by user: %w", err)
	}
	defer rows.Close()

	infos := make([]entity.ArchivedChannelInfo, 0)
	for rows.Next() {
		var info entity.ArchivedChannelInfo
		if err := rows.Scan(
			&info.ID,
			&info.WorkspaceID,
			&info.Name,
			&info.Topic,
			&info.Type,
			&info.CreatedBy,
			&info.OwnerUserID,
			&info.Archived,
			&info.CreatedAt,
			&info.UpdatedAt,
			&info.ArchivedAt,
			&info.MembersCount,
			&info.LastActivityAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list archived channels by user scan: %w", err)
		}
		infos = append(infos, info)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list archived channels by user rows: %w", err)
	}

	return infos, nil
}

// --- Channel Member methods ---

func (r *ChannelRepo) AddMember(ctx context.Context, m *entity.ChannelMember) error {
	query := `
		INSERT INTO channel_members (id, channel_id, user_id, role, muted_until, last_read_at, joined_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err := r.db.Exec(ctx, query,
		m.ID,
		m.ChannelID,
		m.UserID,
		m.Role,
		m.MutedUntil,
		m.LastReadAt,
		m.JoinedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return cerrors.AlreadyExists("member already exists in channel")
		}
		return fmt.Errorf("postgres: add channel member: %w", err)
	}

	return nil
}

func (r *ChannelRepo) GetMember(ctx context.Context, channelID, userID uuid.UUID) (*entity.ChannelMember, error) {
	query := `
		SELECT id, channel_id, user_id, role, muted_until, last_read_at, joined_at
		FROM channel_members
		WHERE channel_id = $1 AND user_id = $2`

	m := &entity.ChannelMember{}
	err := r.db.QueryRow(ctx, query, channelID, userID).Scan(
		&m.ID,
		&m.ChannelID,
		&m.UserID,
		&m.Role,
		&m.MutedUntil,
		&m.LastReadAt,
		&m.JoinedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("channel member not found")
		}
		return nil, fmt.Errorf("postgres: get channel member: %w", err)
	}

	return m, nil
}

func (r *ChannelRepo) ListMembers(ctx context.Context, channelID uuid.UUID) ([]entity.ChannelMember, error) {
	query := `
		SELECT id, channel_id, user_id, role, muted_until, last_read_at, joined_at
		FROM channel_members
		WHERE channel_id = $1
		ORDER BY joined_at`

	rows, err := r.db.Query(ctx, query, channelID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list channel members: %w", err)
	}
	defer rows.Close()

	var members []entity.ChannelMember
	for rows.Next() {
		var m entity.ChannelMember
		if err := rows.Scan(
			&m.ID,
			&m.ChannelID,
			&m.UserID,
			&m.Role,
			&m.MutedUntil,
			&m.LastReadAt,
			&m.JoinedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list channel members scan: %w", err)
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list channel members rows: %w", err)
	}

	return members, nil
}

// SearchMentionableMembers returns channel members matching the query for
// @mention autocomplete (ALK-838), ranked by display name. Matches the display
// name (substring) and the username = email local part (prefix), both
// case-insensitive. An empty query returns the first `limit` members.
func (r *ChannelRepo) SearchMentionableMembers(
	ctx context.Context,
	channelID, excludeUserID uuid.UUID,
	query string,
	limit int,
) ([]entity.MentionSuggestion, error) {
	const q = `
		SELECT u.id,
		       split_part(u.email, '@', 1) AS username,
		       u.display_name,
		       COALESCE(u.avatar_url, '') AS avatar_url,
		       u.position
		FROM channel_members cm
		JOIN users u ON u.id = cm.user_id
		WHERE cm.channel_id = $1
		  AND u.id <> $2
		  AND (
		    $3 = ''
		    OR u.display_name ILIKE '%' || $3 || '%'
		    OR split_part(u.email, '@', 1) ILIKE $3 || '%'
		  )
		ORDER BY u.display_name
		LIMIT $4`

	rows, err := r.db.Query(ctx, q, channelID, excludeUserID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: search mentionable members: %w", err)
	}
	defer rows.Close()

	results := make([]entity.MentionSuggestion, 0)
	for rows.Next() {
		var m entity.MentionSuggestion
		if err := rows.Scan(&m.ID, &m.Username, &m.DisplayName, &m.AvatarURL, &m.Position); err != nil {
			return nil, fmt.Errorf("postgres: search mentionable members scan: %w", err)
		}
		results = append(results, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: search mentionable members rows: %w", err)
	}

	return results, nil
}

func (r *ChannelRepo) RemoveMember(ctx context.Context, channelID, userID uuid.UUID) error {
	query := `
		DELETE FROM channel_members
		WHERE channel_id = $1 AND user_id = $2`

	tag, err := r.db.Exec(ctx, query, channelID, userID)
	if err != nil {
		return fmt.Errorf("postgres: remove channel member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("channel member not found")
	}

	return nil
}

func (r *ChannelRepo) UpdateLastRead(ctx context.Context, channelID, userID uuid.UUID) error {
	query := `
		UPDATE channel_members
		SET last_read_at = $3
		WHERE channel_id = $1 AND user_id = $2`

	tag, err := r.db.Exec(ctx, query, channelID, userID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("postgres: update last read: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("channel member not found")
	}

	return nil
}

func (r *ChannelRepo) GetDMChannel(ctx context.Context, workspaceID, userA, userB uuid.UUID) (*entity.Channel, error) {
	query := `
		SELECT c.id, c.workspace_id, c.name, c.topic, c.type, c.created_by, c.owner_user_id, c.archived, c.created_at, c.updated_at
		FROM channels c
		INNER JOIN channel_members cm1 ON cm1.channel_id = c.id AND cm1.user_id = $2
		INNER JOIN channel_members cm2 ON cm2.channel_id = c.id AND cm2.user_id = $3
		WHERE c.workspace_id = $1 AND c.type = 'dm'
		LIMIT 1`

	ch := &entity.Channel{}
	err := r.db.QueryRow(ctx, query, workspaceID, userA, userB).Scan(
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
			return nil, cerrors.NotFound("dm channel not found")
		}
		return nil, fmt.Errorf("postgres: get dm channel: %w", err)
	}

	return ch, nil
}

// ListCommonChannels returns every non-archived channel in the workspace
// where both the viewer (`viewerID`) and the target user (`targetID`) are
// members. The result includes DMs, group DMs, public and private channels.
// Newest first by id so the popup shows the most-recent shared surfaces at
// the top.
func (r *ChannelRepo) ListCommonChannels(ctx context.Context, workspaceID, viewerID, targetID uuid.UUID) ([]entity.Channel, error) {
	query := `
		SELECT c.id, c.workspace_id, c.name, c.topic, c.type, c.created_by, c.owner_user_id, c.archived, c.created_at, c.updated_at
		FROM channels c
		INNER JOIN channel_members cm1 ON cm1.channel_id = c.id AND cm1.user_id = $2
		INNER JOIN channel_members cm2 ON cm2.channel_id = c.id AND cm2.user_id = $3
		WHERE c.workspace_id = $1
		  AND c.archived = FALSE
		  AND c.type NOT IN ('saved', 'saved_global')
		ORDER BY c.id DESC`

	rows, err := r.db.Query(ctx, query, workspaceID, viewerID, targetID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list common channels: %w", err)
	}
	defer rows.Close()

	channels := make([]entity.Channel, 0)
	for rows.Next() {
		var ch entity.Channel
		if err := rows.Scan(
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
		); err != nil {
			return nil, fmt.Errorf("postgres: list common channels scan: %w", err)
		}
		channels = append(channels, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list common channels rows: %w", err)
	}

	return channels, nil
}
