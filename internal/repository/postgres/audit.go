package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/pagination"
)

// AuditRepo implements repository.AuditRepository using PostgreSQL.
type AuditRepo struct {
	pool *pgxpool.Pool
	db   queryable
}

// NewAuditRepo creates a new AuditRepo.
func NewAuditRepo(pool *pgxpool.Pool) *AuditRepo {
	return &AuditRepo{pool: pool, db: pool}
}

func (r *AuditRepo) withTx(tx pgx.Tx) *AuditRepo {
	if r == nil {
		return nil
	}
	return &AuditRepo{pool: r.pool, db: tx}
}

func (r *AuditRepo) Create(ctx context.Context, entry *entity.AuditEntry) error {
	meta, err := json.Marshal(entry.Metadata)
	if err != nil {
		return fmt.Errorf("postgres: marshal audit metadata: %w", err)
	}

	query := `
		INSERT INTO audit_log (id, workspace_id, actor_id, action, target_type, target_id, metadata, ip_address, user_agent, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	_, err = r.db.Exec(ctx, query,
		entry.ID, entry.WorkspaceID, entry.ActorID, entry.Action,
		entry.TargetType, entry.TargetID, meta,
		entry.IPAddress, entry.UserAgent, entry.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: create audit entry: %w", err)
	}
	return nil
}

func (r *AuditRepo) List(ctx context.Context, workspaceID uuid.UUID, p pagination.Params) ([]entity.AuditEntry, int, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}

	countQuery := `SELECT COUNT(*) FROM audit_log WHERE workspace_id = $1`
	var total int
	if err := r.db.QueryRow(ctx, countQuery, workspaceID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres: count audit entries: %w", err)
	}

	query := `
		SELECT id, workspace_id, actor_id, action, target_type, target_id, metadata, ip_address, user_agent, created_at
		FROM audit_log WHERE workspace_id = $1
		ORDER BY created_at DESC LIMIT $2 OFFSET $3`

	entries, err := r.queryEntries(ctx, query, workspaceID, limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

func (r *AuditRepo) ListByActor(ctx context.Context, workspaceID, actorID uuid.UUID, p pagination.Params) ([]entity.AuditEntry, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT id, workspace_id, actor_id, action, target_type, target_id, metadata, ip_address, user_agent, created_at
		FROM audit_log WHERE workspace_id = $1 AND actor_id = $2
		ORDER BY created_at DESC LIMIT $3 OFFSET $4`

	return r.queryEntries(ctx, query, workspaceID, actorID, limit, p.Offset)
}

func (r *AuditRepo) ListByAction(ctx context.Context, workspaceID uuid.UUID, action entity.AuditAction, p pagination.Params) ([]entity.AuditEntry, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT id, workspace_id, actor_id, action, target_type, target_id, metadata, ip_address, user_agent, created_at
		FROM audit_log WHERE workspace_id = $1 AND action = $2
		ORDER BY created_at DESC LIMIT $3 OFFSET $4`

	return r.queryEntries(ctx, query, workspaceID, action, limit, p.Offset)
}

func (r *AuditRepo) queryEntries(ctx context.Context, query string, args ...any) ([]entity.AuditEntry, error) {
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: query audit entries: %w", err)
	}
	defer rows.Close()

	var entries []entity.AuditEntry
	for rows.Next() {
		var e entity.AuditEntry
		var meta []byte
		if err := rows.Scan(
			&e.ID, &e.WorkspaceID, &e.ActorID, &e.Action,
			&e.TargetType, &e.TargetID, &meta,
			&e.IPAddress, &e.UserAgent, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan audit entry: %w", err)
		}
		if len(meta) > 0 {
			if err := json.Unmarshal(meta, &e.Metadata); err != nil {
				return nil, fmt.Errorf("postgres: unmarshal audit metadata: %w", err)
			}
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate audit entries: %w", err)
	}
	return entries, nil
}
