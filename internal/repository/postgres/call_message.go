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
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
)

// CallMessageRepo implements repository.CallMessageRepository using PostgreSQL.
type CallMessageRepo struct {
	pool *pgxpool.Pool
	db   queryable
}

// NewCallMessageRepo creates a new CallMessageRepo.
func NewCallMessageRepo(pool *pgxpool.Pool) *CallMessageRepo {
	return &CallMessageRepo{pool: pool, db: pool}
}

func (r *CallMessageRepo) withTx(tx pgx.Tx) *CallMessageRepo {
	if r == nil {
		return nil
	}
	return &CallMessageRepo{pool: r.pool, db: tx}
}

func (r *CallMessageRepo) Create(ctx context.Context, msg *entity.CallMessage) error {
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}

	query := `
		INSERT INTO call_messages (id, call_id, sender_id, body, created_at, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6)`

	_, err := r.db.Exec(ctx, query,
		msg.ID,
		msg.CallID,
		msg.SenderID,
		msg.Body,
		msg.CreatedAt,
		msg.DeletedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: create call message: %w", err)
	}

	return nil
}

func (r *CallMessageRepo) ListByCall(ctx context.Context, callID uuid.UUID, p pagination.Params) ([]entity.CallMessage, error) {
	p.Normalize()

	query := `
		SELECT id, call_id, sender_id, body, created_at, deleted_at
		FROM call_messages
		WHERE call_id = $1
			AND deleted_at IS NULL
			AND ($2 = '00000000-0000-0000-0000-000000000000'::uuid OR id < $2)
		ORDER BY id DESC
		LIMIT $3`

	rows, err := r.db.Query(ctx, query, callID, p.Cursor, p.Limit+1)
	if err != nil {
		return nil, fmt.Errorf("postgres: list call messages: %w", err)
	}
	defer rows.Close()

	var messages []entity.CallMessage
	for rows.Next() {
		var msg entity.CallMessage
		if err := rows.Scan(
			&msg.ID,
			&msg.CallID,
			&msg.SenderID,
			&msg.Body,
			&msg.CreatedAt,
			&msg.DeletedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list call messages scan: %w", err)
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list call messages rows: %w", err)
	}

	return messages, nil
}

func (r *CallMessageRepo) SoftDelete(ctx context.Context, id, callID uuid.UUID) error {
	query := `
		UPDATE call_messages
		SET deleted_at = NOW()
		WHERE id = $1 AND call_id = $2 AND deleted_at IS NULL`

	if _, err := r.db.Exec(ctx, query, id, callID); err != nil {
		return fmt.Errorf("postgres: soft delete call message: %w", err)
	}

	return nil
}

func (r *CallMessageRepo) GetByID(ctx context.Context, id uuid.UUID) (*entity.CallMessage, error) {
	query := `
		SELECT id, call_id, sender_id, body, created_at, deleted_at
		FROM call_messages
		WHERE id = $1`

	msg := &entity.CallMessage{}
	err := r.db.QueryRow(ctx, query, id).Scan(
		&msg.ID,
		&msg.CallID,
		&msg.SenderID,
		&msg.Body,
		&msg.CreatedAt,
		&msg.DeletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("call message not found")
		}
		return nil, fmt.Errorf("postgres: get call message by id: %w", err)
	}

	return msg, nil
}
