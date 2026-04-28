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

type WorkspaceRoleRepo struct {
	pool *pgxpool.Pool
	db   queryable
}

func NewWorkspaceRoleRepo(pool *pgxpool.Pool) *WorkspaceRoleRepo {
	return &WorkspaceRoleRepo{pool: pool, db: pool}
}

func (r *WorkspaceRoleRepo) withTx(tx pgx.Tx) *WorkspaceRoleRepo {
	if r == nil {
		return nil
	}
	return &WorkspaceRoleRepo{pool: r.pool, db: tx}
}

func (r *WorkspaceRoleRepo) CreateDefinition(ctx context.Context, role *entity.WorkspaceRoleDefinition) error {
	query := `
		INSERT INTO workspace_role_definitions (id, workspace_id, name, base_role, permissions, system, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	_, err := r.db.Exec(ctx, query,
		role.ID,
		role.WorkspaceID,
		role.Name,
		role.BaseRole,
		role.Permissions,
		role.System,
		role.CreatedAt,
		role.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return cerrors.AlreadyExists("workspace role definition already exists")
		}
		return fmt.Errorf("postgres: create workspace role definition: %w", err)
	}
	return nil
}

func (r *WorkspaceRoleRepo) GetDefinition(ctx context.Context, workspaceID, roleID uuid.UUID) (*entity.WorkspaceRoleDefinition, error) {
	query := `
		SELECT id, workspace_id, name, base_role, permissions, system, created_at, updated_at
		FROM workspace_role_definitions
		WHERE workspace_id = $1 AND id = $2`
	role := &entity.WorkspaceRoleDefinition{}
	err := r.db.QueryRow(ctx, query, workspaceID, roleID).Scan(
		&role.ID,
		&role.WorkspaceID,
		&role.Name,
		&role.BaseRole,
		&role.Permissions,
		&role.System,
		&role.CreatedAt,
		&role.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("workspace role definition not found")
		}
		return nil, fmt.Errorf("postgres: get workspace role definition: %w", err)
	}
	return role, nil
}

func (r *WorkspaceRoleRepo) ListDefinitions(ctx context.Context, workspaceID uuid.UUID) ([]entity.WorkspaceRoleDefinition, error) {
	query := `
		SELECT id, workspace_id, name, base_role, permissions, system, created_at, updated_at
		FROM workspace_role_definitions
		WHERE workspace_id = $1
		ORDER BY system DESC, name ASC`
	rows, err := r.db.Query(ctx, query, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list workspace role definitions: %w", err)
	}
	defer rows.Close()

	var roles []entity.WorkspaceRoleDefinition
	for rows.Next() {
		var role entity.WorkspaceRoleDefinition
		if err := rows.Scan(
			&role.ID,
			&role.WorkspaceID,
			&role.Name,
			&role.BaseRole,
			&role.Permissions,
			&role.System,
			&role.CreatedAt,
			&role.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list workspace role definitions scan: %w", err)
		}
		roles = append(roles, role)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list workspace role definitions rows: %w", err)
	}
	return roles, nil
}

func (r *WorkspaceRoleRepo) UpdateDefinition(ctx context.Context, role *entity.WorkspaceRoleDefinition) error {
	query := `
		UPDATE workspace_role_definitions
		SET name = $3, base_role = $4, permissions = $5, updated_at = $6
		WHERE workspace_id = $1 AND id = $2`
	tag, err := r.db.Exec(ctx, query,
		role.WorkspaceID,
		role.ID,
		role.Name,
		role.BaseRole,
		role.Permissions,
		role.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return cerrors.AlreadyExists("workspace role definition already exists")
		}
		return fmt.Errorf("postgres: update workspace role definition: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("workspace role definition not found")
	}
	return nil
}

func (r *WorkspaceRoleRepo) DeleteDefinition(ctx context.Context, workspaceID, roleID uuid.UUID) error {
	query := `
		DELETE FROM workspace_role_definitions
		WHERE workspace_id = $1 AND id = $2`
	tag, err := r.db.Exec(ctx, query, workspaceID, roleID)
	if err != nil {
		return fmt.Errorf("postgres: delete workspace role definition: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("workspace role definition not found")
	}
	return nil
}

func (r *WorkspaceRoleRepo) AssignRole(ctx context.Context, assignment *entity.WorkspaceRoleAssignment) error {
	query := `
		INSERT INTO workspace_role_assignments (id, workspace_id, user_id, role_id, assigned_by, assigned_at)
		VALUES ($1, $2, $3, $4, $5, $6)`
	_, err := r.db.Exec(ctx, query,
		assignment.ID,
		assignment.WorkspaceID,
		assignment.UserID,
		assignment.RoleID,
		assignment.AssignedBy,
		assignment.AssignedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return cerrors.AlreadyExists("workspace role is already assigned to member")
		}
		return fmt.Errorf("postgres: assign workspace role: %w", err)
	}
	return nil
}

func (r *WorkspaceRoleRepo) UnassignRole(ctx context.Context, workspaceID, userID, roleID uuid.UUID) error {
	query := `
		DELETE FROM workspace_role_assignments
		WHERE workspace_id = $1 AND user_id = $2 AND role_id = $3`
	tag, err := r.db.Exec(ctx, query, workspaceID, userID, roleID)
	if err != nil {
		return fmt.Errorf("postgres: unassign workspace role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("workspace role assignment not found")
	}
	return nil
}

func (r *WorkspaceRoleRepo) ListAssignedDefinitions(ctx context.Context, workspaceID, userID uuid.UUID) ([]entity.WorkspaceRoleDefinition, error) {
	query := `
		SELECT d.id, d.workspace_id, d.name, d.base_role, d.permissions, d.system, d.created_at, d.updated_at
		FROM workspace_role_assignments a
		INNER JOIN workspace_role_definitions d
			ON d.workspace_id = a.workspace_id
			AND d.id = a.role_id
		WHERE a.workspace_id = $1 AND a.user_id = $2
		ORDER BY d.system DESC, d.name ASC`
	rows, err := r.db.Query(ctx, query, workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list assigned workspace role definitions: %w", err)
	}
	defer rows.Close()

	var roles []entity.WorkspaceRoleDefinition
	for rows.Next() {
		var role entity.WorkspaceRoleDefinition
		if err := rows.Scan(
			&role.ID,
			&role.WorkspaceID,
			&role.Name,
			&role.BaseRole,
			&role.Permissions,
			&role.System,
			&role.CreatedAt,
			&role.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list assigned workspace role definitions scan: %w", err)
		}
		roles = append(roles, role)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list assigned workspace role definitions rows: %w", err)
	}
	return roles, nil
}
