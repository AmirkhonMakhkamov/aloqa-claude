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

// UserRepo implements repository.UserRepository using PostgreSQL.
type UserRepo struct {
	pool *pgxpool.Pool
	db   queryable
}

// NewUserRepo creates a new UserRepo.
func NewUserRepo(pool *pgxpool.Pool) *UserRepo {
	return &UserRepo{pool: pool, db: pool}
}

func (r *UserRepo) withTx(tx pgx.Tx) *UserRepo {
	if r == nil {
		return nil
	}
	return &UserRepo{pool: r.pool, db: tx}
}

func (r *UserRepo) Create(ctx context.Context, user *entity.User) error {
	if user.SavedMessagesMode == "" {
		user.SavedMessagesMode = entity.SavedMessagesModePerWorkspace
	}
	query := `
		INSERT INTO users (id, email, display_name, avatar_url, password_hash, status, deactivated_at, saved_messages_mode, locale, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

	_, err := r.db.Exec(ctx, query,
		user.ID,
		user.Email,
		user.DisplayName,
		user.AvatarURL,
		user.PasswordHash,
		user.Status,
		user.DeactivatedAt,
		user.SavedMessagesMode,
		user.Locale,
		user.CreatedAt,
		user.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return cerrors.AlreadyExists("user already exists")
		}
		return fmt.Errorf("postgres: create user: %w", err)
	}

	return nil
}

func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*entity.User, error) {
	query := `
		SELECT id, email, display_name, avatar_url, password_hash, status, deactivated_at, saved_messages_mode, locale, created_at, updated_at
		FROM users
		WHERE id = $1`

	user := &entity.User{}
	err := r.db.QueryRow(ctx, query, id).Scan(
		&user.ID,
		&user.Email,
		&user.DisplayName,
		&user.AvatarURL,
		&user.PasswordHash,
		&user.Status,
		&user.DeactivatedAt,
		&user.SavedMessagesMode,
		&user.Locale,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("user not found")
		}
		return nil, fmt.Errorf("postgres: get user by id: %w", err)
	}

	return user, nil
}

func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*entity.User, error) {
	// Case-insensitive lookup: registration historically stored emails
	// verbatim (mixed case in, mixed case out), but several call-sites
	// (admin.InviteMember, password reset) normalise to lower-case before
	// looking the user up. That mismatch surfaced as cryptic
	// "user not found" responses for accounts that very much exist.
	// Comparing on LOWER(email) on both sides means every code path —
	// register existence-check, login, invite, reset — finds the same
	// row regardless of the case the caller typed.
	query := `
		SELECT id, email, display_name, avatar_url, password_hash, status, deactivated_at, saved_messages_mode, locale, created_at, updated_at
		FROM users
		WHERE LOWER(email) = LOWER($1)`

	user := &entity.User{}
	err := r.db.QueryRow(ctx, query, email).Scan(
		&user.ID,
		&user.Email,
		&user.DisplayName,
		&user.AvatarURL,
		&user.PasswordHash,
		&user.Status,
		&user.DeactivatedAt,
		&user.SavedMessagesMode,
		&user.Locale,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("user not found")
		}
		return nil, fmt.Errorf("postgres: get user by email: %w", err)
	}

	return user, nil
}

func (r *UserRepo) ListActiveExcept(ctx context.Context, excludedID uuid.UUID, limit int) ([]entity.User, error) {
	if limit <= 0 {
		return []entity.User{}, nil
	}
	if limit > 50 {
		limit = 50
	}

	query := `
		SELECT id, email, display_name, avatar_url, password_hash, status, deactivated_at, saved_messages_mode, locale, created_at, updated_at
		FROM users
		WHERE id <> $1 AND status = $2
		ORDER BY created_at ASC
		LIMIT $3`

	rows, err := r.db.Query(ctx, query, excludedID, entity.UserStatusActive, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list active users except: %w", err)
	}
	defer rows.Close()

	users := make([]entity.User, 0, limit)
	for rows.Next() {
		var user entity.User
		if err := rows.Scan(
			&user.ID,
			&user.Email,
			&user.DisplayName,
			&user.AvatarURL,
			&user.PasswordHash,
			&user.Status,
			&user.DeactivatedAt,
			&user.SavedMessagesMode,
			&user.Locale,
			&user.CreatedAt,
			&user.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list active users except scan: %w", err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list active users except rows: %w", err)
	}

	return users, nil
}

func (r *UserRepo) Update(ctx context.Context, user *entity.User) error {
	if user.SavedMessagesMode == "" {
		user.SavedMessagesMode = entity.SavedMessagesModePerWorkspace
	}
	query := `
		UPDATE users
		SET display_name = $2, avatar_url = $3, status = $4, deactivated_at = $5, saved_messages_mode = $6, updated_at = $7
		WHERE id = $1`

	user.UpdatedAt = time.Now().UTC()

	tag, err := r.db.Exec(ctx, query,
		user.ID,
		user.DisplayName,
		user.AvatarURL,
		user.Status,
		user.DeactivatedAt,
		user.SavedMessagesMode,
		user.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: update user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("user not found")
	}

	return nil
}
