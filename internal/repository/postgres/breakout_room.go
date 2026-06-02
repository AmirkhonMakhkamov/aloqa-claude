package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"

	"github.com/google/uuid"
)

// BreakoutRoomRepo implements repository.BreakoutRoomRepository using PostgreSQL.
type BreakoutRoomRepo struct {
	pool *pgxpool.Pool
}

// NewBreakoutRoomRepo creates a new BreakoutRoomRepo.
func NewBreakoutRoomRepo(pool *pgxpool.Pool) repository.BreakoutRoomRepository {
	return &BreakoutRoomRepo{pool: pool}
}

func (r *BreakoutRoomRepo) Create(ctx context.Context, room *entity.BreakoutRoom) error {
	query := `
		INSERT INTO breakout_rooms (id, call_id, name, created_by, time_limit, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err := r.pool.Exec(ctx, query,
		room.ID,
		room.CallID,
		room.Name,
		room.CreatedBy,
		room.TimeLimit,
		room.Status,
		room.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: create breakout room: %w", err)
	}

	return nil
}

func (r *BreakoutRoomRepo) GetByID(ctx context.Context, id uuid.UUID) (*entity.BreakoutRoom, error) {
	query := `
		SELECT id, call_id, name, created_by, time_limit, status, created_at, closed_at
		FROM breakout_rooms
		WHERE id = $1`

	room := &entity.BreakoutRoom{}
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&room.ID,
		&room.CallID,
		&room.Name,
		&room.CreatedBy,
		&room.TimeLimit,
		&room.Status,
		&room.CreatedAt,
		&room.ClosedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("breakout room not found")
		}
		return nil, fmt.Errorf("postgres: get breakout room: %w", err)
	}

	return room, nil
}

func (r *BreakoutRoomRepo) ListByCall(ctx context.Context, callID uuid.UUID) ([]entity.BreakoutRoom, error) {
	query := `
		SELECT id, call_id, name, created_by, time_limit, status, created_at, closed_at
		FROM breakout_rooms
		WHERE call_id = $1
		ORDER BY created_at`

	rows, err := r.pool.Query(ctx, query, callID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list breakout rooms: %w", err)
	}
	defer rows.Close()

	rooms := make([]entity.BreakoutRoom, 0)
	for rows.Next() {
		var room entity.BreakoutRoom
		if err := rows.Scan(
			&room.ID,
			&room.CallID,
			&room.Name,
			&room.CreatedBy,
			&room.TimeLimit,
			&room.Status,
			&room.CreatedAt,
			&room.ClosedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list breakout rooms scan: %w", err)
		}
		rooms = append(rooms, room)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list breakout rooms rows: %w", err)
	}

	return rooms, nil
}

func (r *BreakoutRoomRepo) Close(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC()
	query := `
		UPDATE breakout_rooms
		SET status = 'closed', closed_at = $2
		WHERE id = $1 AND status = 'active'`

	tag, err := r.pool.Exec(ctx, query, id, now)
	if err != nil {
		return fmt.Errorf("postgres: close breakout room: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("breakout room not found or already closed")
	}

	return nil
}

func (r *BreakoutRoomRepo) CloseAllByCall(ctx context.Context, callID uuid.UUID) error {
	now := time.Now().UTC()
	query := `
		UPDATE breakout_rooms
		SET status = 'closed', closed_at = $2
		WHERE call_id = $1 AND status = 'active'`

	_, err := r.pool.Exec(ctx, query, callID, now)
	if err != nil {
		return fmt.Errorf("postgres: close all breakout rooms: %w", err)
	}

	return nil
}

func (r *BreakoutRoomRepo) AssignParticipant(ctx context.Context, callID, userID, breakoutRoomID uuid.UUID) error {
	query := `
		UPDATE call_participants
		SET breakout_room_id = $3
		WHERE call_id = $1 AND user_id = $2 AND status IN ('joining', 'connected')`

	tag, err := r.pool.Exec(ctx, query, callID, userID, breakoutRoomID)
	if err != nil {
		return fmt.Errorf("postgres: assign participant to breakout room: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("active participant not found in call")
	}

	return nil
}

func (r *BreakoutRoomRepo) UnassignParticipant(ctx context.Context, callID, userID uuid.UUID) error {
	query := `
		UPDATE call_participants
		SET breakout_room_id = NULL
		WHERE call_id = $1 AND user_id = $2`

	tag, err := r.pool.Exec(ctx, query, callID, userID)
	if err != nil {
		return fmt.Errorf("postgres: unassign participant from breakout room: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("participant not found in call")
	}

	return nil
}

func (r *BreakoutRoomRepo) UnassignAllByRoom(ctx context.Context, breakoutRoomID uuid.UUID) error {
	query := `
		UPDATE call_participants
		SET breakout_room_id = NULL
		WHERE breakout_room_id = $1`

	_, err := r.pool.Exec(ctx, query, breakoutRoomID)
	if err != nil {
		return fmt.Errorf("postgres: unassign all participants from breakout room: %w", err)
	}

	return nil
}

func (r *BreakoutRoomRepo) ListParticipants(ctx context.Context, breakoutRoomID uuid.UUID) ([]entity.CallParticipant, error) {
	query := `
		SELECT id, call_id, user_id, breakout_room_id, role, status, audio_muted, video_muted, screen_sharing, joined_at, left_at
		FROM call_participants
		WHERE breakout_room_id = $1 AND status IN ('joining', 'connected')
		ORDER BY joined_at`

	rows, err := r.pool.Query(ctx, query, breakoutRoomID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list breakout room participants: %w", err)
	}
	defer rows.Close()

	participants := make([]entity.CallParticipant, 0)
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
			return nil, fmt.Errorf("postgres: list breakout room participants scan: %w", err)
		}
		participants = append(participants, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list breakout room participants rows: %w", err)
	}

	return participants, nil
}
