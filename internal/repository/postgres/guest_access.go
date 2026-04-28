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
)

type GuestAccessRepo struct {
	pool *pgxpool.Pool
	db   queryable
}

func NewGuestAccessRepo(pool *pgxpool.Pool) *GuestAccessRepo {
	return &GuestAccessRepo{pool: pool, db: pool}
}

func (r *GuestAccessRepo) withTx(tx pgx.Tx) *GuestAccessRepo {
	if r == nil {
		return nil
	}
	return &GuestAccessRepo{pool: r.pool, db: tx}
}

func (r *GuestAccessRepo) CreateGrant(ctx context.Context, grant *entity.GuestAccessGrant) error {
	query := `
		INSERT INTO guest_access_grants (id, invite_id, workspace_id, user_id, channel_ids, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`
	_, err := r.db.Exec(ctx, query,
		grant.ID,
		grant.InviteID,
		grant.WorkspaceID,
		grant.UserID,
		grant.ChannelIDs,
		grant.ExpiresAt,
		grant.CreatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return cerrors.AlreadyExists("guest access grant already exists")
		}
		return fmt.Errorf("postgres: create guest access grant: %w", err)
	}
	return nil
}

func (r *GuestAccessRepo) ListActiveByUserWorkspace(ctx context.Context, userID, workspaceID uuid.UUID, now time.Time) ([]entity.GuestAccessGrant, error) {
	query := `
		SELECT id, invite_id, workspace_id, user_id, channel_ids, expires_at, created_at
		FROM guest_access_grants
		WHERE user_id = $1 AND workspace_id = $2 AND expires_at > $3
		ORDER BY created_at DESC`
	rows, err := r.db.Query(ctx, query, userID, workspaceID, now)
	if err != nil {
		return nil, fmt.Errorf("postgres: list guest access grants: %w", err)
	}
	defer rows.Close()

	var grants []entity.GuestAccessGrant
	for rows.Next() {
		var grant entity.GuestAccessGrant
		if err := rows.Scan(
			&grant.ID,
			&grant.InviteID,
			&grant.WorkspaceID,
			&grant.UserID,
			&grant.ChannelIDs,
			&grant.ExpiresAt,
			&grant.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan guest access grant: %w", err)
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate guest access grants: %w", err)
	}
	return grants, nil
}
