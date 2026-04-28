package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
)

// GuestInviteRepo implements repository.GuestInviteRepository using PostgreSQL.
type GuestInviteRepo struct {
	pool *pgxpool.Pool
	db   queryable
}

// NewGuestInviteRepo creates a new GuestInviteRepo.
func NewGuestInviteRepo(pool *pgxpool.Pool) *GuestInviteRepo {
	return &GuestInviteRepo{pool: pool, db: pool}
}

func (r *GuestInviteRepo) withTx(tx pgx.Tx) *GuestInviteRepo {
	if r == nil {
		return nil
	}
	return &GuestInviteRepo{pool: r.pool, db: tx}
}

func (r *GuestInviteRepo) Create(ctx context.Context, invite *entity.GuestInvite) error {
	query := `
		INSERT INTO guest_invites (id, workspace_id, created_by, token, email, channel_ids, max_uses, use_count, status, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

	_, err := r.db.Exec(ctx, query,
		invite.ID, invite.WorkspaceID, invite.CreatedBy, invite.Token,
		invite.Email, invite.ChannelIDs, invite.MaxUses, invite.UseCount,
		invite.Status, invite.ExpiresAt, invite.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: create guest invite: %w", err)
	}
	return nil
}

func (r *GuestInviteRepo) GetByToken(ctx context.Context, token string) (*entity.GuestInvite, error) {
	query := `
		SELECT id, workspace_id, created_by, token, email, channel_ids, max_uses, use_count, status, expires_at, created_at
		FROM guest_invites WHERE token = $1`

	return r.scanInvite(ctx, query, token)
}

func (r *GuestInviteRepo) GetByID(ctx context.Context, id uuid.UUID) (*entity.GuestInvite, error) {
	query := `
		SELECT id, workspace_id, created_by, token, email, channel_ids, max_uses, use_count, status, expires_at, created_at
		FROM guest_invites WHERE id = $1`

	return r.scanInvite(ctx, query, id)
}

func (r *GuestInviteRepo) IncrementUseCount(ctx context.Context, id uuid.UUID) error {
	query := `UPDATE guest_invites SET use_count = use_count + 1 WHERE id = $1 AND use_count < max_uses AND status = 'active'`
	tag, err := r.db.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("postgres: increment invite use count: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.Forbidden("invite has reached its maximum number of uses or is no longer active")
	}
	return nil
}

func (r *GuestInviteRepo) Revoke(ctx context.Context, id uuid.UUID) error {
	query := `UPDATE guest_invites SET status = 'revoked' WHERE id = $1`
	tag, err := r.db.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("postgres: revoke guest invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("invite not found")
	}
	return nil
}

func (r *GuestInviteRepo) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]entity.GuestInvite, error) {
	query := `
		SELECT id, workspace_id, created_by, token, email, channel_ids, max_uses, use_count, status, expires_at, created_at
		FROM guest_invites WHERE workspace_id = $1 ORDER BY created_at DESC`

	rows, err := r.db.Query(ctx, query, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list guest invites: %w", err)
	}
	defer rows.Close()

	var invites []entity.GuestInvite
	for rows.Next() {
		var inv entity.GuestInvite
		if err := rows.Scan(
			&inv.ID, &inv.WorkspaceID, &inv.CreatedBy, &inv.Token,
			&inv.Email, &inv.ChannelIDs, &inv.MaxUses, &inv.UseCount,
			&inv.Status, &inv.ExpiresAt, &inv.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan guest invite: %w", err)
		}
		invites = append(invites, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate guest invites: %w", err)
	}
	return invites, nil
}

func (r *GuestInviteRepo) scanInvite(ctx context.Context, query string, args ...any) (*entity.GuestInvite, error) {
	inv := &entity.GuestInvite{}
	err := r.db.QueryRow(ctx, query, args...).Scan(
		&inv.ID, &inv.WorkspaceID, &inv.CreatedBy, &inv.Token,
		&inv.Email, &inv.ChannelIDs, &inv.MaxUses, &inv.UseCount,
		&inv.Status, &inv.ExpiresAt, &inv.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("invite not found")
		}
		return nil, fmt.Errorf("postgres: get guest invite: %w", err)
	}
	return inv, nil
}
