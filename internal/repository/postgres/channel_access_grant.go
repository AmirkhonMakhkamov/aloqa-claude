package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
)

type ChannelAccessGrantRepo struct {
	pool *pgxpool.Pool
	db   queryable
}

func NewChannelAccessGrantRepo(pool *pgxpool.Pool) *ChannelAccessGrantRepo {
	return &ChannelAccessGrantRepo{pool: pool, db: pool}
}

func (r *ChannelAccessGrantRepo) withTx(tx pgx.Tx) *ChannelAccessGrantRepo {
	if r == nil {
		return nil
	}
	return &ChannelAccessGrantRepo{pool: r.pool, db: tx}
}

func (r *ChannelAccessGrantRepo) CreateGrant(ctx context.Context, grant *entity.ChannelAccessGrant) error {
	query := `
		INSERT INTO channel_access_grants (
			id, channel_id, workspace_id, user_id, source_user_id, remote_workspace_id, kind, allow_calls, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	_, err := r.db.Exec(ctx, query,
		grant.ID,
		grant.ChannelID,
		grant.WorkspaceID,
		grant.UserID,
		grant.SourceUserID,
		grant.RemoteWorkspaceID,
		grant.Kind,
		grant.AllowCalls,
		grant.CreatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return cerrors.AlreadyExists("channel access grant already exists")
		}
		return fmt.Errorf("postgres: create channel access grant: %w", err)
	}
	return nil
}

func (r *ChannelAccessGrantRepo) GetGrant(ctx context.Context, channelID, userID uuid.UUID) (*entity.ChannelAccessGrant, error) {
	query := `
		SELECT id, channel_id, workspace_id, user_id, source_user_id, remote_workspace_id, kind, allow_calls, created_at
		FROM channel_access_grants
		WHERE channel_id = $1 AND user_id = $2`

	grant := &entity.ChannelAccessGrant{}
	err := r.db.QueryRow(ctx, query, channelID, userID).Scan(
		&grant.ID,
		&grant.ChannelID,
		&grant.WorkspaceID,
		&grant.UserID,
		&grant.SourceUserID,
		&grant.RemoteWorkspaceID,
		&grant.Kind,
		&grant.AllowCalls,
		&grant.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("channel access grant not found")
		}
		return nil, fmt.Errorf("postgres: get channel access grant: %w", err)
	}
	return grant, nil
}

func (r *ChannelAccessGrantRepo) ListByChannel(ctx context.Context, channelID uuid.UUID) ([]entity.ChannelAccessGrant, error) {
	query := `
		SELECT id, channel_id, workspace_id, user_id, source_user_id, remote_workspace_id, kind, allow_calls, created_at
		FROM channel_access_grants
		WHERE channel_id = $1
		ORDER BY created_at`

	rows, err := r.db.Query(ctx, query, channelID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list channel access grants: %w", err)
	}
	defer rows.Close()

	var grants []entity.ChannelAccessGrant
	for rows.Next() {
		var grant entity.ChannelAccessGrant
		if err := rows.Scan(
			&grant.ID,
			&grant.ChannelID,
			&grant.WorkspaceID,
			&grant.UserID,
			&grant.SourceUserID,
			&grant.RemoteWorkspaceID,
			&grant.Kind,
			&grant.AllowCalls,
			&grant.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list channel access grants scan: %w", err)
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list channel access grants rows: %w", err)
	}
	return grants, nil
}
