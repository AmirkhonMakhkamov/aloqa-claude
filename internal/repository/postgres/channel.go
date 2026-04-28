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
		INSERT INTO channels (id, workspace_id, name, topic, type, created_by, archived, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	_, err := r.db.Exec(ctx, query,
		ch.ID,
		ch.WorkspaceID,
		ch.Name,
		ch.Topic,
		ch.Type,
		ch.CreatedBy,
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
		SELECT id, workspace_id, name, topic, type, created_by, archived, created_at, updated_at
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
			SELECT id, workspace_id, name, topic, type, created_by, archived, created_at, updated_at
			FROM channels
			WHERE workspace_id = $1 AND id < $2
			ORDER BY id DESC
			LIMIT $3`
		rows, err = r.db.Query(ctx, query, workspaceID, p.Cursor, p.Limit+1)
	} else {
		query := `
			SELECT id, workspace_id, name, topic, type, created_by, archived, created_at, updated_at
			FROM channels
			WHERE workspace_id = $1
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
	query := `
		SELECT c.id, c.workspace_id, c.name, c.topic, c.type, c.created_by, c.archived, c.created_at, c.updated_at
		FROM channels c
		INNER JOIN channel_members cm ON cm.channel_id = c.id
		WHERE c.workspace_id = $1 AND cm.user_id = $2
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
	query := `
		UPDATE channels
		SET name = $2, topic = $3, updated_at = $4
		WHERE id = $1`

	ch.UpdatedAt = time.Now().UTC()

	tag, err := r.db.Exec(ctx, query,
		ch.ID,
		ch.Name,
		ch.Topic,
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
		SET archived = true, updated_at = $2
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
		SELECT c.id, c.workspace_id, c.name, c.topic, c.type, c.created_by, c.archived, c.created_at, c.updated_at
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
