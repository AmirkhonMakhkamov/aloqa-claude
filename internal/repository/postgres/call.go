package postgres

import (
	"context"
	"encoding/json"
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

// CallRepo implements repository.CallRepository using PostgreSQL.
type CallRepo struct {
	pool *pgxpool.Pool
	db   queryable
}

// NewCallRepo creates a new CallRepo.
func NewCallRepo(pool *pgxpool.Pool) *CallRepo {
	return &CallRepo{pool: pool, db: pool}
}

func (r *CallRepo) withTx(tx pgx.Tx) *CallRepo {
	if r == nil {
		return nil
	}
	return &CallRepo{pool: r.pool, db: tx}
}

func (r *CallRepo) Create(ctx context.Context, call *entity.Call) error {
	settingsJSON, err := json.Marshal(call.Settings)
	if err != nil {
		return fmt.Errorf("postgres: marshal call settings: %w", err)
	}

	query := `
		INSERT INTO calls (id, workspace_id, channel_id, type, status, title, created_by, settings, started_at, ended_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

	_, err = r.db.Exec(ctx, query,
		call.ID,
		call.WorkspaceID,
		call.ChannelID,
		call.Type,
		call.Status,
		call.Title,
		call.CreatedBy,
		settingsJSON,
		call.StartedAt,
		call.EndedAt,
		call.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: create call: %w", err)
	}

	return nil
}

func (r *CallRepo) GetByID(ctx context.Context, id uuid.UUID) (*entity.Call, error) {
	query := `
		SELECT id, workspace_id, channel_id, type, status, title, created_by, settings, started_at, ended_at, created_at
		FROM calls
		WHERE id = $1`

	call := &entity.Call{}
	var settingsJSON []byte

	err := r.db.QueryRow(ctx, query, id).Scan(
		&call.ID,
		&call.WorkspaceID,
		&call.ChannelID,
		&call.Type,
		&call.Status,
		&call.Title,
		&call.CreatedBy,
		&settingsJSON,
		&call.StartedAt,
		&call.EndedAt,
		&call.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("call not found")
		}
		return nil, fmt.Errorf("postgres: get call by id: %w", err)
	}

	if err := json.Unmarshal(settingsJSON, &call.Settings); err != nil {
		return nil, fmt.Errorf("postgres: unmarshal call settings: %w", err)
	}

	return call, nil
}

func (r *CallRepo) ListActiveByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]entity.Call, error) {
	query := `
		SELECT id, workspace_id, channel_id, type, status, title, created_by, settings, started_at, ended_at, created_at
		FROM calls
		WHERE workspace_id = $1 AND status != 'ended'
		ORDER BY created_at DESC`

	rows, err := r.db.Query(ctx, query, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list active calls: %w", err)
	}
	defer rows.Close()

	var calls []entity.Call
	for rows.Next() {
		var call entity.Call
		var settingsJSON []byte

		if err := rows.Scan(
			&call.ID,
			&call.WorkspaceID,
			&call.ChannelID,
			&call.Type,
			&call.Status,
			&call.Title,
			&call.CreatedBy,
			&settingsJSON,
			&call.StartedAt,
			&call.EndedAt,
			&call.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list active calls scan: %w", err)
		}

		if err := json.Unmarshal(settingsJSON, &call.Settings); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal call settings: %w", err)
		}

		calls = append(calls, call)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list active calls rows: %w", err)
	}

	return calls, nil
}

func (r *CallRepo) ListRecentByWorkspace(ctx context.Context, workspaceID uuid.UUID, limit int, before *time.Time) ([]entity.Call, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	query := `
		SELECT id, workspace_id, channel_id, type, status, title, created_by, settings, started_at, ended_at, created_at
		FROM calls
		WHERE workspace_id = $1`
	args := []any{workspaceID}
	if before != nil {
		query += ` AND created_at < $2`
		args = append(args, *before)
		query += ` ORDER BY created_at DESC LIMIT $3`
		args = append(args, limit+1)
	} else {
		query += ` ORDER BY created_at DESC LIMIT $2`
		args = append(args, limit+1)
	}

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list recent calls: %w", err)
	}
	defer rows.Close()

	var calls []entity.Call
	for rows.Next() {
		var call entity.Call
		var settingsJSON []byte
		if err := rows.Scan(
			&call.ID,
			&call.WorkspaceID,
			&call.ChannelID,
			&call.Type,
			&call.Status,
			&call.Title,
			&call.CreatedBy,
			&settingsJSON,
			&call.StartedAt,
			&call.EndedAt,
			&call.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list recent calls scan: %w", err)
		}
		if err := json.Unmarshal(settingsJSON, &call.Settings); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal recent call settings: %w", err)
		}
		calls = append(calls, call)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list recent calls rows: %w", err)
	}
	return calls, nil
}

func (r *CallRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status entity.CallStatus) error {
	query := `
		UPDATE calls
		SET status = $2
		WHERE id = $1`

	tag, err := r.db.Exec(ctx, query, id, status)
	if err != nil {
		return fmt.Errorf("postgres: update call status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("call not found")
	}

	return nil
}

func (r *CallRepo) End(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC()
	query := `
		UPDATE calls
		SET status = 'ended', ended_at = $2
		WHERE id = $1`

	tag, err := r.db.Exec(ctx, query, id, now)
	if err != nil {
		return fmt.Errorf("postgres: end call: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("call not found")
	}

	return nil
}

// --- Participant methods ---

func (r *CallRepo) AddParticipant(ctx context.Context, p *entity.CallParticipant) error {
	query := `
		INSERT INTO call_participants (id, call_id, user_id, role, status, audio_muted, video_muted, screen_sharing, joined_at, left_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	_, err := r.db.Exec(ctx, query,
		p.ID,
		p.CallID,
		p.UserID,
		p.Role,
		p.Status,
		p.AudioMuted,
		p.VideoMuted,
		p.ScreenSharing,
		p.JoinedAt,
		p.LeftAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return cerrors.AlreadyExists("participant already in call")
		}
		return fmt.Errorf("postgres: add call participant: %w", err)
	}

	return nil
}

func (r *CallRepo) AddParticipantIfCapacity(ctx context.Context, p *entity.CallParticipant, maxParticipants int) error {
	// Use an INSERT ... SELECT that atomically checks the active participant
	// count within the same statement, eliminating the TOCTOU race between
	// checking capacity and inserting.
	query := `
		INSERT INTO call_participants (id, call_id, user_id, role, status, audio_muted, video_muted, screen_sharing, joined_at, left_at)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
		WHERE (
			SELECT COUNT(*) FROM call_participants
			WHERE call_id = $2 AND status IN ('active', 'reconnecting')
		) < $11`

	tag, err := r.db.Exec(ctx, query,
		p.ID, p.CallID, p.UserID, p.Role, p.Status,
		p.AudioMuted, p.VideoMuted, p.ScreenSharing,
		p.JoinedAt, p.LeftAt, maxParticipants,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return cerrors.AlreadyExists("participant already in call")
		}
		return fmt.Errorf("postgres: add participant with capacity check: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.Forbidden("call has reached maximum participant capacity")
	}
	return nil
}

func (r *CallRepo) GetParticipant(ctx context.Context, callID, userID uuid.UUID) (*entity.CallParticipant, error) {
	query := `
		SELECT id, call_id, user_id, breakout_room_id, role, status, audio_muted, video_muted, screen_sharing, joined_at, left_at
		FROM call_participants
		WHERE call_id = $1 AND user_id = $2`

	p := &entity.CallParticipant{}
	err := r.db.QueryRow(ctx, query, callID, userID).Scan(
		&p.ID,
		&p.CallID,
		&p.UserID,
		&p.BreakoutRoomID,
		&p.Role,
		&p.Status,
		&p.AudioMuted,
		&p.VideoMuted,
		&p.ScreenSharing,
		&p.JoinedAt,
		&p.LeftAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("call participant not found")
		}
		return nil, fmt.Errorf("postgres: get call participant: %w", err)
	}

	return p, nil
}

func (r *CallRepo) ListParticipants(ctx context.Context, callID uuid.UUID) ([]entity.CallParticipant, error) {
	query := `
		SELECT id, call_id, user_id, breakout_room_id, role, status, audio_muted, video_muted, screen_sharing, joined_at, left_at
		FROM call_participants
		WHERE call_id = $1
		ORDER BY joined_at`

	rows, err := r.db.Query(ctx, query, callID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list call participants: %w", err)
	}
	defer rows.Close()

	var participants []entity.CallParticipant
	for rows.Next() {
		var p entity.CallParticipant
		if err := rows.Scan(
			&p.ID,
			&p.CallID,
			&p.UserID,
			&p.BreakoutRoomID,
			&p.Role,
			&p.Status,
			&p.AudioMuted,
			&p.VideoMuted,
			&p.ScreenSharing,
			&p.JoinedAt,
			&p.LeftAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list call participants scan: %w", err)
		}
		participants = append(participants, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list call participants rows: %w", err)
	}

	return participants, nil
}

func (r *CallRepo) UpdateParticipantStatus(ctx context.Context, id uuid.UUID, status entity.ParticipantStatus) error {
	query := `
		UPDATE call_participants
		SET status = $2
		WHERE id = $1`

	tag, err := r.db.Exec(ctx, query, id, status)
	if err != nil {
		return fmt.Errorf("postgres: update participant status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("call participant not found")
	}

	return nil
}

func (r *CallRepo) UpdateParticipantRole(ctx context.Context, id uuid.UUID, role entity.CallRole) error {
	query := `
		UPDATE call_participants
		SET role = $2
		WHERE id = $1`

	tag, err := r.db.Exec(ctx, query, id, role)
	if err != nil {
		return fmt.Errorf("postgres: update participant role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("call participant not found")
	}

	return nil
}

func (r *CallRepo) UpdateParticipantMedia(ctx context.Context, id uuid.UUID, audioMuted, videoMuted, screenSharing bool) error {
	query := `
		UPDATE call_participants
		SET audio_muted = $2, video_muted = $3, screen_sharing = $4
		WHERE id = $1`

	tag, err := r.db.Exec(ctx, query, id, audioMuted, videoMuted, screenSharing)
	if err != nil {
		return fmt.Errorf("postgres: update participant media: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("call participant not found")
	}

	return nil
}

func (r *CallRepo) RemoveParticipant(ctx context.Context, callID, userID uuid.UUID) error {
	query := `
		DELETE FROM call_participants
		WHERE call_id = $1 AND user_id = $2`

	tag, err := r.db.Exec(ctx, query, callID, userID)
	if err != nil {
		return fmt.Errorf("postgres: remove call participant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("call participant not found")
	}

	return nil
}
