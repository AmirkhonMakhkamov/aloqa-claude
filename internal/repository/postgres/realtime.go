package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/platform/reliability"
)

const (
	realtimeEventStatusPending    = "pending"
	realtimeEventStatusProcessing = "processing"
	realtimeEventStatusPublished  = "published"
	realtimeEventStatusFailed     = "failed"
	realtimeEventStatusDead       = "dead"
)

type RealtimeRepo struct {
	pool *pgxpool.Pool
	db   queryable
}

type ConsumerCursor struct {
	ConsumerName      string
	StreamName        string
	LastEventID       *uuid.UUID
	LastEventSequence int64
	Deliveries        int64
	Failures          int64
	Lag               int64
	Status            string
	LastError         string
	UpdatedAt         time.Time
}

func NewRealtimeRepo(pool *pgxpool.Pool) *RealtimeRepo {
	return &RealtimeRepo{pool: pool, db: pool}
}

func (r *RealtimeRepo) withTx(tx pgx.Tx) *RealtimeRepo {
	if r == nil {
		return nil
	}
	return &RealtimeRepo{pool: r.pool, db: tx}
}

func (r *RealtimeRepo) Pressure() reliability.Pressure {
	if r == nil || r.pool == nil {
		return reliability.Pressure{}
	}
	return reliability.PostgresPressure(r.pool.Stat())
}

func (r *RealtimeRepo) Enqueue(ctx context.Context, evt event.Event, body []byte, maxAttempts int) error {
	if maxAttempts <= 0 {
		maxAttempts = 8
	}

	channelID := nullableUUID(evt.ChannelID)
	if _, err := r.db.Exec(ctx, `
		INSERT INTO realtime_events (
			id, version, type, subject, workspace_id, channel_id, user_id,
			delivery_semantic, replayable, body, created_at, available_at,
			max_attempts, status, last_error
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11, $12, $13, '')
	`,
		evt.ID,
		evt.Version,
		string(evt.Type),
		evt.Subject,
		evt.WorkspaceID,
		channelID,
		evt.UserID,
		string(evt.DeliverySemantic),
		evt.Replayable,
		body,
		evt.Timestamp.UTC(),
		maxAttempts,
		realtimeEventStatusPending,
	); err != nil {
		return fmt.Errorf("postgres: enqueue realtime event: %w", err)
	}
	return nil
}

func (r *RealtimeRepo) ClaimPending(ctx context.Context, batchSize int) ([]event.QueuedEvent, error) {
	if batchSize <= 0 {
		batchSize = 100
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("postgres: begin realtime event claim tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	rows, err := tx.Query(ctx, `
		SELECT id, version, sequence, type, subject, workspace_id, channel_id, user_id,
			delivery_semantic, replayable, body, created_at, attempts, max_attempts
		FROM realtime_events
		WHERE available_at <= NOW()
		  AND (
			status IN ('pending', 'failed')
			OR (status = 'processing' AND locked_at < NOW() - INTERVAL '5 minutes')
		  )
		ORDER BY sequence ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, batchSize)
	if err != nil {
		return nil, fmt.Errorf("postgres: query pending realtime events: %w", err)
	}
	defer rows.Close()

	var events []event.QueuedEvent
	var ids []uuid.UUID
	for rows.Next() {
		var (
			evt         event.Event
			channelID   *uuid.UUID
			body        []byte
			delivery    string
			eventType   string
			attempts    int
			maxAttempts int
		)
		if err := rows.Scan(
			&evt.ID,
			&evt.Version,
			&evt.Sequence,
			&eventType,
			&evt.Subject,
			&evt.WorkspaceID,
			&channelID,
			&evt.UserID,
			&delivery,
			&evt.Replayable,
			&body,
			&evt.Timestamp,
			&attempts,
			&maxAttempts,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan realtime event: %w", err)
		}
		evt.Type = event.Type(eventType)
		evt.DeliverySemantic = event.DeliverySemantic(delivery)
		if channelID != nil {
			evt.ChannelID = *channelID
		}
		if err := json.Unmarshal(body, &evt); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal realtime event body: %w", err)
		}
		events = append(events, event.QueuedEvent{
			Event:       evt,
			Body:        body,
			Attempts:    attempts + 1,
			MaxAttempts: maxAttempts,
		})
		ids = append(ids, evt.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate realtime events: %w", err)
	}
	if len(ids) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("postgres: commit realtime event claim tx: %w", err)
		}
		return nil, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE realtime_events
		SET status = 'processing',
			attempts = attempts + 1,
			locked_at = NOW()
		WHERE id = ANY($1)
	`, ids); err != nil {
		return nil, fmt.Errorf("postgres: mark realtime events processing: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("postgres: commit realtime event claim tx: %w", err)
	}
	return events, nil
}

func (r *RealtimeRepo) MarkPublished(ctx context.Context, eventID uuid.UUID) error {
	if _, err := r.db.Exec(ctx, `
		UPDATE realtime_events
		SET status = 'published',
			published_at = NOW(),
			locked_at = NULL,
			available_at = NOW(),
			last_error = ''
		WHERE id = $1
	`, eventID); err != nil {
		return fmt.Errorf("postgres: mark realtime event published: %w", err)
	}
	return nil
}

func (r *RealtimeRepo) MarkFailed(ctx context.Context, eventID uuid.UUID, lastError string, nextRetryAt *time.Time, dead bool) error {
	status := realtimeEventStatusFailed
	if dead {
		status = realtimeEventStatusDead
	}
	if _, err := r.db.Exec(ctx, `
		UPDATE realtime_events
		SET status = $2,
			last_error = $3,
			available_at = COALESCE($4, available_at),
			locked_at = NULL
		WHERE id = $1
	`, eventID, status, lastError, nextRetryAt); err != nil {
		return fmt.Errorf("postgres: mark realtime event failed: %w", err)
	}
	return nil
}

func (r *RealtimeRepo) ReplayRoom(ctx context.Context, room string, afterSequence int64, limit int) ([]event.Event, error) {
	if limit <= 0 {
		limit = 200
	}
	room = strings.TrimSpace(room)
	if room == "" || strings.HasPrefix(room, "aloqa.signal.") {
		return nil, nil
	}

	subject := room
	if strings.HasPrefix(room, "channel:") {
		subject = "aloqa.chat." + strings.TrimPrefix(room, "channel:")
	}

	rows, err := r.db.Query(ctx, `
		SELECT body
		FROM realtime_events
		WHERE status = 'published'
		  AND replayable = true
		  AND subject = $1
		  AND sequence > $2
		ORDER BY sequence ASC
		LIMIT $3
	`, subject, afterSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: replay room events: %w", err)
	}
	defer rows.Close()

	events := make([]event.Event, 0, limit)
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, fmt.Errorf("postgres: scan replay room event: %w", err)
		}
		var evt event.Event
		if err := json.Unmarshal(body, &evt); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal replay room event: %w", err)
		}
		events = append(events, evt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate replay room events: %w", err)
	}
	return events, nil
}

func (r *RealtimeRepo) UpdateConsumerCursor(ctx context.Context, consumerName, streamName string, evt *event.Event, success bool, status, lastError string) error {
	lastSequence := int64(0)
	var lastEventID *uuid.UUID
	if evt != nil {
		lastSequence = evt.Sequence
		lastEventID = &evt.ID
	}
	failuresInc := int64(0)
	deliveriesInc := int64(0)
	if success {
		deliveriesInc = 1
	} else {
		failuresInc = 1
	}

	if _, err := r.db.Exec(ctx, `
		INSERT INTO event_consumer_cursors (
			consumer_name, stream_name, last_event_id, last_event_sequence,
			deliveries, failures, lag, status, last_error, updated_at
		)
		VALUES (
			$1, $2, $3, $4, $5, $6,
			GREATEST((SELECT COALESCE(MAX(sequence), 0) FROM realtime_events WHERE status = 'published') - $4, 0),
			$7, $8, NOW()
		)
		ON CONFLICT (consumer_name) DO UPDATE
		SET stream_name = EXCLUDED.stream_name,
			last_event_id = COALESCE(EXCLUDED.last_event_id, event_consumer_cursors.last_event_id),
			last_event_sequence = GREATEST(event_consumer_cursors.last_event_sequence, EXCLUDED.last_event_sequence),
			deliveries = event_consumer_cursors.deliveries + EXCLUDED.deliveries,
			failures = event_consumer_cursors.failures + EXCLUDED.failures,
			lag = GREATEST((SELECT COALESCE(MAX(sequence), 0) FROM realtime_events WHERE status = 'published') - GREATEST(event_consumer_cursors.last_event_sequence, EXCLUDED.last_event_sequence), 0),
			status = EXCLUDED.status,
			last_error = EXCLUDED.last_error,
			updated_at = NOW()
	`, consumerName, streamName, lastEventID, lastSequence, deliveriesInc, failuresInc, status, lastError); err != nil {
		return fmt.Errorf("postgres: update event consumer cursor: %w", err)
	}
	return nil
}

func (r *RealtimeRepo) QueueStats(ctx context.Context) (*entity.QueueRuntimeStats, error) {
	stats := &entity.QueueRuntimeStats{
		Name:      "realtime_outbox",
		UpdatedAt: time.Now().UTC(),
	}
	row := r.db.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status = 'pending')::bigint,
			COUNT(*) FILTER (WHERE status = 'processing')::bigint,
			COUNT(*) FILTER (WHERE status = 'failed')::bigint,
			COUNT(*) FILTER (WHERE status = 'dead')::bigint,
			COUNT(*) FILTER (WHERE status = 'published')::bigint,
			COALESCE((EXTRACT(EPOCH FROM NOW() - MIN(created_at)) * 1000)::bigint, 0)
		FROM realtime_events
		WHERE status IN ('pending', 'processing', 'failed', 'dead', 'published')
	`)
	if err := row.Scan(&stats.Pending, &stats.Processing, &stats.Failed, &stats.Dead, &stats.Published, &stats.OldestPendingAgeMs); err != nil {
		return nil, fmt.Errorf("postgres: realtime queue stats: %w", err)
	}
	return stats, nil
}

func (r *RealtimeRepo) ListConsumerLag(ctx context.Context, limit int) ([]entity.EventConsumerLag, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Query(ctx, `
		SELECT consumer_name, stream_name, last_event_id, last_event_sequence,
			deliveries, failures, lag, status, last_error, updated_at
		FROM event_consumer_cursors
		ORDER BY lag DESC, updated_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list consumer lag: %w", err)
	}
	defer rows.Close()

	items := make([]entity.EventConsumerLag, 0, limit)
	for rows.Next() {
		var (
			item    entity.EventConsumerLag
			eventID *uuid.UUID
		)
		if err := rows.Scan(
			&item.ConsumerName,
			&item.StreamName,
			&eventID,
			&item.LastEventSequence,
			&item.Deliveries,
			&item.Failures,
			&item.Lag,
			&item.Status,
			&item.LastError,
			&item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan consumer lag: %w", err)
		}
		if eventID != nil {
			item.LastEventID = eventID.String()
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate consumer lag: %w", err)
	}
	return items, nil
}

func (r *RealtimeRepo) ResetStuckJobs(ctx context.Context, stuckAfter time.Duration) (int64, error) {
	if stuckAfter <= 0 {
		stuckAfter = 5 * time.Minute
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE realtime_events
		SET status    = 'pending',
			locked_at = NULL
		WHERE status    = 'processing'
		  AND locked_at < NOW() - $1::interval
	`, fmt.Sprintf("%d seconds", int(stuckAfter.Seconds())))
	if err != nil {
		return 0, fmt.Errorf("postgres: reset stuck realtime jobs: %w", err)
	}
	return tag.RowsAffected(), nil
}

func nullableUUID(value uuid.UUID) *uuid.UUID {
	if value == uuid.Nil {
		return nil
	}
	return &value
}
