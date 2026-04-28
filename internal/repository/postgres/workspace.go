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

// WorkspaceRepo implements repository.WorkspaceRepository using PostgreSQL.
type WorkspaceRepo struct {
	pool *pgxpool.Pool
	db   queryable
}

// NewWorkspaceRepo creates a new WorkspaceRepo.
func NewWorkspaceRepo(pool *pgxpool.Pool) *WorkspaceRepo {
	return &WorkspaceRepo{pool: pool, db: pool}
}

func (r *WorkspaceRepo) withTx(tx pgx.Tx) *WorkspaceRepo {
	if r == nil {
		return nil
	}
	return &WorkspaceRepo{pool: r.pool, db: tx}
}

func (r *WorkspaceRepo) Create(ctx context.Context, ws *entity.Workspace) error {
	query := `
		INSERT INTO workspaces (id, name, slug, avatar_url, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err := r.db.Exec(ctx, query,
		ws.ID,
		ws.Name,
		ws.Slug,
		ws.AvatarURL,
		ws.CreatedBy,
		ws.CreatedAt,
		ws.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return cerrors.AlreadyExists("workspace already exists")
		}
		return fmt.Errorf("postgres: create workspace: %w", err)
	}

	return nil
}

func (r *WorkspaceRepo) CreateWithOwner(ctx context.Context, ws *entity.Workspace, owner *entity.WorkspaceMember) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("postgres: begin create workspace tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	workspaceQuery := `
		INSERT INTO workspaces (id, name, slug, avatar_url, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`
	if _, err := tx.Exec(ctx, workspaceQuery,
		ws.ID,
		ws.Name,
		ws.Slug,
		ws.AvatarURL,
		ws.CreatedBy,
		ws.CreatedAt,
		ws.UpdatedAt,
	); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return cerrors.AlreadyExists("workspace already exists")
		}
		return fmt.Errorf("postgres: create workspace in tx: %w", err)
	}

	memberQuery := `
		INSERT INTO workspace_members (id, workspace_id, user_id, role, joined_at)
		VALUES ($1, $2, $3, $4, $5)`
	if _, err := tx.Exec(ctx, memberQuery,
		owner.ID,
		owner.WorkspaceID,
		owner.UserID,
		owner.Role,
		owner.JoinedAt,
	); err != nil {
		return fmt.Errorf("postgres: create workspace owner in tx: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit create workspace tx: %w", err)
	}
	return nil
}

func (r *WorkspaceRepo) GetByID(ctx context.Context, id uuid.UUID) (*entity.Workspace, error) {
	query := `
		SELECT id, name, slug, avatar_url, created_by, created_at, updated_at
		FROM workspaces
		WHERE id = $1`

	ws := &entity.Workspace{}
	err := r.db.QueryRow(ctx, query, id).Scan(
		&ws.ID,
		&ws.Name,
		&ws.Slug,
		&ws.AvatarURL,
		&ws.CreatedBy,
		&ws.CreatedAt,
		&ws.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("workspace not found")
		}
		return nil, fmt.Errorf("postgres: get workspace by id: %w", err)
	}

	return ws, nil
}

func (r *WorkspaceRepo) GetBySlug(ctx context.Context, slug string) (*entity.Workspace, error) {
	query := `
		SELECT id, name, slug, avatar_url, created_by, created_at, updated_at
		FROM workspaces
		WHERE slug = $1`

	ws := &entity.Workspace{}
	err := r.db.QueryRow(ctx, query, slug).Scan(
		&ws.ID,
		&ws.Name,
		&ws.Slug,
		&ws.AvatarURL,
		&ws.CreatedBy,
		&ws.CreatedAt,
		&ws.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("workspace not found")
		}
		return nil, fmt.Errorf("postgres: get workspace by slug: %w", err)
	}

	return ws, nil
}

func (r *WorkspaceRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]entity.Workspace, error) {
	query := `
		SELECT w.id, w.name, w.slug, w.avatar_url, w.created_by, w.created_at, w.updated_at
		FROM workspaces w
		INNER JOIN workspace_members wm ON wm.workspace_id = w.id
		WHERE wm.user_id = $1
		ORDER BY w.name`

	rows, err := r.db.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list workspaces by user: %w", err)
	}
	defer rows.Close()

	var workspaces []entity.Workspace
	for rows.Next() {
		var ws entity.Workspace
		if err := rows.Scan(
			&ws.ID,
			&ws.Name,
			&ws.Slug,
			&ws.AvatarURL,
			&ws.CreatedBy,
			&ws.CreatedAt,
			&ws.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list workspaces by user scan: %w", err)
		}
		workspaces = append(workspaces, ws)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list workspaces by user rows: %w", err)
	}

	return workspaces, nil
}

func (r *WorkspaceRepo) Update(ctx context.Context, ws *entity.Workspace) error {
	query := `
		UPDATE workspaces
		SET name = $2, slug = $3, avatar_url = $4, updated_at = $5
		WHERE id = $1`

	ws.UpdatedAt = time.Now().UTC()

	tag, err := r.db.Exec(ctx, query,
		ws.ID,
		ws.Name,
		ws.Slug,
		ws.AvatarURL,
		ws.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return cerrors.AlreadyExists("workspace slug already exists")
		}
		return fmt.Errorf("postgres: update workspace: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("workspace not found")
	}

	return nil
}

// --- Workspace Member methods ---

func (r *WorkspaceRepo) AddMember(ctx context.Context, m *entity.WorkspaceMember) error {
	query := `
		INSERT INTO workspace_members (id, workspace_id, user_id, role, joined_at)
		VALUES ($1, $2, $3, $4, $5)`

	_, err := r.db.Exec(ctx, query,
		m.ID,
		m.WorkspaceID,
		m.UserID,
		m.Role,
		m.JoinedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return cerrors.AlreadyExists("member already exists in workspace")
		}
		return fmt.Errorf("postgres: add workspace member: %w", err)
	}

	return nil
}

func (r *WorkspaceRepo) GetMember(ctx context.Context, workspaceID, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	query := `
		SELECT id, workspace_id, user_id, role, joined_at
		FROM workspace_members
		WHERE workspace_id = $1 AND user_id = $2`

	m := &entity.WorkspaceMember{}
	err := r.db.QueryRow(ctx, query, workspaceID, userID).Scan(
		&m.ID,
		&m.WorkspaceID,
		&m.UserID,
		&m.Role,
		&m.JoinedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("workspace member not found")
		}
		return nil, fmt.Errorf("postgres: get workspace member: %w", err)
	}

	return m, nil
}

func (r *WorkspaceRepo) ListMembers(ctx context.Context, workspaceID uuid.UUID, p pagination.Params) ([]entity.WorkspaceMember, error) {
	p.Normalize()

	var (
		rows pgx.Rows
		err  error
	)

	// JOIN users so the admin UI and members cache can render names/emails
	// without a separate round-trip per user.
	if p.Cursor != uuid.Nil {
		query := `
			SELECT wm.id, wm.workspace_id, wm.user_id, wm.role, wm.joined_at,
			       u.id, u.email, u.display_name, u.avatar_url, u.status, u.locale, u.created_at, u.updated_at
			FROM workspace_members wm
			JOIN users u ON u.id = wm.user_id
			WHERE wm.workspace_id = $1 AND wm.id < $2
			ORDER BY wm.id DESC
			LIMIT $3`
		rows, err = r.db.Query(ctx, query, workspaceID, p.Cursor, p.Limit+1)
	} else {
		query := `
			SELECT wm.id, wm.workspace_id, wm.user_id, wm.role, wm.joined_at,
			       u.id, u.email, u.display_name, u.avatar_url, u.status, u.locale, u.created_at, u.updated_at
			FROM workspace_members wm
			JOIN users u ON u.id = wm.user_id
			WHERE wm.workspace_id = $1
			ORDER BY wm.id DESC
			LIMIT $2`
		rows, err = r.db.Query(ctx, query, workspaceID, p.Limit+1)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: list workspace members: %w", err)
	}
	defer rows.Close()

	var members []entity.WorkspaceMember
	for rows.Next() {
		var m entity.WorkspaceMember
		var u entity.User
		if err := rows.Scan(
			&m.ID,
			&m.WorkspaceID,
			&m.UserID,
			&m.Role,
			&m.JoinedAt,
			&u.ID,
			&u.Email,
			&u.DisplayName,
			&u.AvatarURL,
			&u.Status,
			&u.Locale,
			&u.CreatedAt,
			&u.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list workspace members scan: %w", err)
		}
		m.User = &u
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list workspace members rows: %w", err)
	}

	// Trim the extra element used for HasMore detection.
	if len(members) > p.Limit {
		members = members[:p.Limit]
	}

	return members, nil
}

func (r *WorkspaceRepo) UpdateMemberRole(ctx context.Context, workspaceID, userID uuid.UUID, role entity.WorkspaceRole) error {
	query := `
		UPDATE workspace_members
		SET role = $3
		WHERE workspace_id = $1 AND user_id = $2`

	tag, err := r.db.Exec(ctx, query, workspaceID, userID, role)
	if err != nil {
		return fmt.Errorf("postgres: update workspace member role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("workspace member not found")
	}

	return nil
}

func (r *WorkspaceRepo) RemoveMember(ctx context.Context, workspaceID, userID uuid.UUID) error {
	query := `
		DELETE FROM workspace_members
		WHERE workspace_id = $1 AND user_id = $2`

	tag, err := r.db.Exec(ctx, query, workspaceID, userID)
	if err != nil {
		return fmt.Errorf("postgres: remove workspace member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("workspace member not found")
	}

	return nil
}
