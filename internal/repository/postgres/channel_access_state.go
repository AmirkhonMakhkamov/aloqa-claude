package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
)

type ChannelAccessStateRepo struct {
	pool *pgxpool.Pool
}

func NewChannelAccessStateRepo(pool *pgxpool.Pool) repository.ChannelAccessStateRepository {
	return &ChannelAccessStateRepo{pool: pool}
}

func (r *ChannelAccessStateRepo) GetState(ctx context.Context, channelID, userID uuid.UUID) (*entity.ChannelAccessState, error) {
	query := `
		SELECT channel_id, user_id, last_read_at, created_at, updated_at
		FROM channel_access_state
		WHERE channel_id = $1 AND user_id = $2`

	state := &entity.ChannelAccessState{}
	err := r.pool.QueryRow(ctx, query, channelID, userID).Scan(
		&state.ChannelID,
		&state.UserID,
		&state.LastReadAt,
		&state.CreatedAt,
		&state.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("channel access state not found")
		}
		return nil, fmt.Errorf("postgres: get channel access state: %w", err)
	}
	return state, nil
}

func (r *ChannelAccessStateRepo) UpsertState(ctx context.Context, state *entity.ChannelAccessState) error {
	now := time.Now().UTC()
	if state.CreatedAt.IsZero() {
		state.CreatedAt = now
	}
	state.UpdatedAt = now
	if state.LastReadAt.IsZero() {
		state.LastReadAt = now
	}

	query := `
		INSERT INTO channel_access_state (channel_id, user_id, last_read_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (channel_id, user_id) DO UPDATE
		SET last_read_at = EXCLUDED.last_read_at,
			updated_at = EXCLUDED.updated_at`

	if _, err := r.pool.Exec(ctx, query,
		state.ChannelID,
		state.UserID,
		state.LastReadAt.UTC(),
		state.CreatedAt.UTC(),
		state.UpdatedAt.UTC(),
	); err != nil {
		return fmt.Errorf("postgres: upsert channel access state: %w", err)
	}
	return nil
}
