package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
)

type WorkspaceCollaborationRepo struct {
	pool *pgxpool.Pool
}

func NewWorkspaceCollaborationRepo(pool *pgxpool.Pool) repository.WorkspaceCollaborationRepository {
	return &WorkspaceCollaborationRepo{pool: pool}
}

func (r *WorkspaceCollaborationRepo) CreateConnection(ctx context.Context, connection *entity.WorkspaceConnection) error {
	policy, err := json.Marshal(connection.Policy)
	if err != nil {
		return fmt.Errorf("postgres: marshal workspace connection policy: %w", err)
	}

	query := `
		INSERT INTO workspace_connections (
			id, source_workspace_id, target_workspace_id, status, policy, created_by, approved_by, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	_, err = r.pool.Exec(ctx, query,
		connection.ID,
		connection.SourceWorkspaceID,
		connection.TargetWorkspaceID,
		connection.Status,
		policy,
		connection.CreatedBy,
		connection.ApprovedBy,
		connection.CreatedAt,
		connection.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return cerrors.AlreadyExists("workspace connection already exists")
		}
		return fmt.Errorf("postgres: create workspace connection: %w", err)
	}
	return nil
}

func (r *WorkspaceCollaborationRepo) GetConnection(ctx context.Context, sourceWorkspaceID, targetWorkspaceID uuid.UUID) (*entity.WorkspaceConnection, error) {
	query := `
		SELECT id, source_workspace_id, target_workspace_id, status, policy, created_by, approved_by, created_at, updated_at
		FROM workspace_connections
		WHERE source_workspace_id = $1 AND target_workspace_id = $2`

	return scanWorkspaceConnection(r.pool.QueryRow(ctx, query, sourceWorkspaceID, targetWorkspaceID))
}

func (r *WorkspaceCollaborationRepo) ListConnections(ctx context.Context, workspaceID uuid.UUID, p pagination.Params) ([]entity.WorkspaceConnection, error) {
	p.Normalize()

	query := `
		SELECT id, source_workspace_id, target_workspace_id, status, policy, created_by, approved_by, created_at, updated_at
		FROM workspace_connections
		WHERE source_workspace_id = $1 OR target_workspace_id = $1
		ORDER BY created_at DESC
		LIMIT $2`

	rows, err := r.pool.Query(ctx, query, workspaceID, p.Limit+1)
	if err != nil {
		return nil, fmt.Errorf("postgres: list workspace connections: %w", err)
	}
	defer rows.Close()

	var connections []entity.WorkspaceConnection
	for rows.Next() {
		connection, err := scanWorkspaceConnection(rows)
		if err != nil {
			return nil, err
		}
		connections = append(connections, *connection)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list workspace connections rows: %w", err)
	}
	if len(connections) > p.Limit {
		connections = connections[:p.Limit]
	}
	return connections, nil
}

func (r *WorkspaceCollaborationRepo) UpdateConnectionPolicy(ctx context.Context, id uuid.UUID, policy entity.WorkspaceConnectionPolicy) error {
	data, err := json.Marshal(policy)
	if err != nil {
		return fmt.Errorf("postgres: marshal workspace connection policy: %w", err)
	}

	tag, err := r.pool.Exec(ctx, `UPDATE workspace_connections SET policy = $2 WHERE id = $1`, id, data)
	if err != nil {
		return fmt.Errorf("postgres: update workspace connection policy: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("workspace connection not found")
	}
	return nil
}

func (r *WorkspaceCollaborationRepo) UpdateConnectionStatus(ctx context.Context, id uuid.UUID, status entity.WorkspaceConnectionStatus, approvedBy *uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `UPDATE workspace_connections SET status = $2, approved_by = $3 WHERE id = $1`, id, status, approvedBy)
	if err != nil {
		return fmt.Errorf("postgres: update workspace connection status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("workspace connection not found")
	}
	return nil
}

type workspaceConnectionScanner interface {
	Scan(dest ...any) error
}

func scanWorkspaceConnection(scanner workspaceConnectionScanner) (*entity.WorkspaceConnection, error) {
	connection := &entity.WorkspaceConnection{}
	var policyData []byte
	if err := scanner.Scan(
		&connection.ID,
		&connection.SourceWorkspaceID,
		&connection.TargetWorkspaceID,
		&connection.Status,
		&policyData,
		&connection.CreatedBy,
		&connection.ApprovedBy,
		&connection.CreatedAt,
		&connection.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("workspace connection not found")
		}
		return nil, fmt.Errorf("postgres: scan workspace connection: %w", err)
	}
	if len(policyData) > 0 {
		if err := json.Unmarshal(policyData, &connection.Policy); err != nil {
			return nil, fmt.Errorf("postgres: decode workspace connection policy: %w", err)
		}
	}
	return connection, nil
}
