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

func (r *CallRepo) ClaimLiveKitWebhookEvent(ctx context.Context, event *entity.LiveKitWebhookEvent) (entity.LiveKitWebhookClaimResult, error) {
	if event == nil {
		return "", cerrors.InvalidInput("livekit webhook event is required")
	}
	if event.ClaimToken == "" {
		return "", cerrors.InvalidInput("livekit webhook claim token is required")
	}
	query := `
		INSERT INTO call_livekit_webhook_events (event_id, call_id, event_type, status, claim_token, received_at, lease_expires_at)
		VALUES ($1, $2, $3, 'processing', $4, $5, $6)
		ON CONFLICT (event_id) DO NOTHING`

	receivedAt := event.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}
	leaseExpiresAt := event.LeaseExpiresAt
	if leaseExpiresAt == nil {
		lease := time.Now().Add(2 * time.Minute).UTC()
		leaseExpiresAt = &lease
	}
	tag, err := r.db.Exec(ctx, query, event.EventID, event.CallID, event.EventType, event.ClaimToken, receivedAt, leaseExpiresAt)
	if err != nil {
		return "", fmt.Errorf("postgres: claim livekit webhook event: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return entity.LiveKitWebhookClaimProcess, nil
	}

	return r.reclaimLiveKitWebhookEvent(ctx, event.EventID, event.ClaimToken, receivedAt, *leaseExpiresAt)
}

func (r *CallRepo) reclaimLiveKitWebhookEvent(ctx context.Context, eventID, claimToken string, receivedAt, leaseExpiresAt time.Time) (entity.LiveKitWebhookClaimResult, error) {
	var status string
	var currentLease *time.Time
	if err := r.db.QueryRow(ctx, `
		SELECT status, lease_expires_at
		FROM call_livekit_webhook_events
		WHERE event_id = $1`, eventID).Scan(&status, &currentLease); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return entity.LiveKitWebhookClaimInProgress, nil
		}
		return "", fmt.Errorf("postgres: load livekit webhook event claim: %w", err)
	}
	if status == "processed" {
		return entity.LiveKitWebhookClaimDuplicate, nil
	}
	if currentLease != nil && currentLease.After(time.Now().UTC()) {
		return entity.LiveKitWebhookClaimInProgress, nil
	}

	tag, err := r.db.Exec(ctx, `
		UPDATE call_livekit_webhook_events
		SET received_at = $2,
		    lease_expires_at = $3,
		    claim_token = $4
		WHERE event_id = $1
		  AND status = 'processing'
		  AND (lease_expires_at IS NULL OR lease_expires_at <= now())`,
		eventID,
		receivedAt,
		leaseExpiresAt,
		claimToken,
	)
	if err != nil {
		return "", fmt.Errorf("postgres: reclaim livekit webhook event: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return entity.LiveKitWebhookClaimProcess, nil
	}
	return entity.LiveKitWebhookClaimInProgress, nil
}

func (r *CallRepo) MarkLiveKitWebhookEventProcessed(ctx context.Context, eventID, claimToken string) (bool, error) {
	if eventID == "" || claimToken == "" {
		return true, nil
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE call_livekit_webhook_events
		SET status = 'processed',
		    processed_at = now(),
		    lease_expires_at = NULL
		WHERE event_id = $1
		  AND claim_token = $2
		  AND status = 'processing'`, eventID, claimToken)
	if err != nil {
		return false, fmt.Errorf("postgres: mark livekit webhook event processed: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func nullableCallEndReason(reason entity.CallEndReason) any {
	if reason == "" {
		return nil
	}
	return string(reason)
}

func nullableParticipantLeftReason(reason entity.ParticipantLeftReason) any {
	if reason == "" {
		return nil
	}
	return string(reason)
}

func (r *CallRepo) Create(ctx context.Context, call *entity.Call) error {
	settingsJSON, err := json.Marshal(call.Settings)
	if err != nil {
		return fmt.Errorf("postgres: marshal call settings: %w", err)
	}

	query := `
		INSERT INTO calls (id, workspace_id, channel_id, type, status, title, created_by, scheduled_call_id, settings, started_at, ended_at, end_reason, featured_share_user_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`

	_, err = r.db.Exec(ctx, query,
		call.ID,
		call.WorkspaceID,
		call.ChannelID,
		call.Type,
		call.Status,
		call.Title,
		call.CreatedBy,
		call.ScheduledCallID,
		settingsJSON,
		call.StartedAt,
		call.EndedAt,
		nullableCallEndReason(call.EndReason),
		call.FeaturedShareUserID,
		call.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: create call: %w", err)
	}

	return nil
}

func (r *CallRepo) GetByID(ctx context.Context, id uuid.UUID) (*entity.Call, error) {
	query := `
		SELECT id, workspace_id, channel_id, type, status, title, created_by, scheduled_call_id, settings, started_at, ended_at, COALESCE(end_reason, ''), featured_share_user_id, created_at
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
		&call.ScheduledCallID,
		&settingsJSON,
		&call.StartedAt,
		&call.EndedAt,
		&call.EndReason,
		&call.FeaturedShareUserID,
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
		SELECT id, workspace_id, channel_id, type, status, title, created_by, scheduled_call_id, settings, started_at, ended_at, COALESCE(end_reason, ''), featured_share_user_id, created_at
		FROM calls
		WHERE workspace_id = $1 AND status != 'ended'
		ORDER BY created_at DESC`

	rows, err := r.db.Query(ctx, query, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list active calls: %w", err)
	}
	defer rows.Close()

	calls := make([]entity.Call, 0)
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
			&call.ScheduledCallID,
			&settingsJSON,
			&call.StartedAt,
			&call.EndedAt,
			&call.EndReason,
			&call.FeaturedShareUserID,
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

// ListStaleOpen returns non-ended calls old enough to reconcile against the
// LiveKit media plane. The service performs the presence check before applying
// any terminal transition.
func (r *CallRepo) ListStaleOpen(ctx context.Context, before time.Time, limit int) ([]entity.Call, error) {
	if limit <= 0 {
		limit = defaultStaleOpenCallLimit
	}
	if limit > maxStaleOpenCallLimit {
		limit = maxStaleOpenCallLimit
	}

	query := `
		SELECT id, workspace_id, channel_id, type, status, title, created_by, scheduled_call_id, settings, started_at, ended_at, COALESCE(end_reason, ''), featured_share_user_id, created_at
		FROM calls
		WHERE status IN ('ringing', 'active')
		  AND COALESCE(started_at, created_at) < $1
		ORDER BY COALESCE(started_at, created_at) ASC
		LIMIT $2`

	rows, err := r.db.Query(ctx, query, before, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list stale open calls: %w", err)
	}
	defer rows.Close()

	calls := make([]entity.Call, 0)
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
			&call.ScheduledCallID,
			&settingsJSON,
			&call.StartedAt,
			&call.EndedAt,
			&call.EndReason,
			&call.FeaturedShareUserID,
			&call.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: list stale open calls scan: %w", err)
		}

		if err := json.Unmarshal(settingsJSON, &call.Settings); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal stale call settings: %w", err)
		}

		calls = append(calls, call)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list stale open calls rows: %w", err)
	}

	return calls, nil
}

const (
	defaultStaleOpenCallLimit     = 100
	maxStaleOpenCallLimit         = 500
	defaultActiveObservationLimit = 100
	maxActiveObservationLimit     = 500
)

// ListActiveObservations returns active/ringing calls across workspaces for
// Prometheus/Grafana observability. Keep the projection compact because every
// text field becomes a metric label on the active call table.
func (r *CallRepo) ListActiveObservations(ctx context.Context, limit int) ([]entity.ActiveCallObservation, error) {
	if limit <= 0 {
		limit = defaultActiveObservationLimit
	}
	if limit > maxActiveObservationLimit {
		limit = maxActiveObservationLimit
	}

	const query = `
		SELECT
			c.id,
			c.workspace_id,
			c.type,
			c.status,
			c.title,
			COALESCE(c.started_at, c.created_at) AS started_at,
			ch.name AS channel_name,
			COALESCE(u.display_name, '') AS host_display_name,
			COALESCE((c.settings->>'recording')::bool, false) AS recording,
			COALESCE(NOT (c.settings->>'waiting_room')::bool, true) AS is_open,
			COALESCE(pc.participant_count, 0) AS participant_count,
			COALESCE(pc.observer_count, 0) AS observer_count
		FROM calls c
		LEFT JOIN channels ch ON ch.id = c.channel_id
		LEFT JOIN users u ON u.id = c.created_by
		LEFT JOIN LATERAL (
			SELECT
				COUNT(*) FILTER (WHERE cp.role <> 'viewer' AND cp.status = 'connected') AS participant_count,
				COUNT(*) FILTER (WHERE cp.role = 'viewer' AND cp.status = 'connected') AS observer_count
			FROM call_participants cp
			WHERE cp.call_id = c.id
		) pc ON TRUE
		WHERE c.status <> 'ended'
		ORDER BY c.started_at DESC NULLS LAST, c.created_at DESC
		LIMIT $1`

	rows, err := r.db.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list active call observations: %w", err)
	}
	defer rows.Close()

	observations := []entity.ActiveCallObservation{}
	for rows.Next() {
		var item entity.ActiveCallObservation
		if err := rows.Scan(
			&item.ID,
			&item.WorkspaceID,
			&item.Type,
			&item.Status,
			&item.Title,
			&item.StartedAt,
			&item.ChannelName,
			&item.HostDisplayName,
			&item.Recording,
			&item.IsOpen,
			&item.ParticipantCount,
			&item.ObserverCount,
		); err != nil {
			return nil, fmt.Errorf("postgres: list active call observations scan: %w", err)
		}
		observations = append(observations, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list active call observations rows: %w", err)
	}
	return observations, nil
}

// topParticipantsLimit caps how many participant projections we send on the
// Live Now card; the FE renders the rest as a "+N" pill in the avatar stack.
const topParticipantsLimit = 4

// ListActiveSummariesByWorkspace returns enriched summaries for every
// non-ended call in the workspace: channel name, host display name,
// connected-participant / observer counts, and up to four top participants
// for the avatar stack. Used by GET /calls/active for the Calls Home page.
func (r *CallRepo) ListActiveSummariesByWorkspace(
	ctx context.Context,
	workspaceID uuid.UUID,
) ([]entity.ActiveCallSummary, error) {
	const query = `
		SELECT
			c.id, c.type, c.title, c.started_at, c.created_by,
			c.channel_id, ch.name AS channel_name,
			COALESCE(u.display_name, '') AS host_display_name,
			COALESCE((c.settings->>'recording')::bool, false) AS recording,
			COALESCE(NOT (c.settings->>'waiting_room')::bool, true) AS is_open,
			COALESCE(pc.participant_count, 0) AS participant_count,
			COALESCE(pc.observer_count, 0) AS observer_count
		FROM calls c
		LEFT JOIN channels ch ON ch.id = c.channel_id
		LEFT JOIN users u ON u.id = c.created_by
		LEFT JOIN LATERAL (
			SELECT
				COUNT(*) FILTER (WHERE cp.role <> 'viewer' AND cp.status = 'connected') AS participant_count,
				COUNT(*) FILTER (WHERE cp.role = 'viewer' AND cp.status = 'connected') AS observer_count
			FROM call_participants cp
			WHERE cp.call_id = c.id
		) pc ON TRUE
		WHERE c.workspace_id = $1 AND c.status <> 'ended'
		ORDER BY c.started_at DESC NULLS LAST, c.created_at DESC`

	rows, err := r.db.Query(ctx, query, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list active call summaries: %w", err)
	}
	defer rows.Close()

	summaries := []entity.ActiveCallSummary{}
	callIDs := []uuid.UUID{}
	for rows.Next() {
		var s entity.ActiveCallSummary
		var startedAt *time.Time
		if err := rows.Scan(
			&s.ID,
			&s.Type,
			&s.Title,
			&startedAt,
			&s.HostUserID,
			&s.ChannelID,
			&s.ChannelName,
			&s.HostDisplayName,
			&s.Recording,
			&s.IsOpen,
			&s.ParticipantCount,
			&s.ObserverCount,
		); err != nil {
			return nil, fmt.Errorf("postgres: list active call summaries scan: %w", err)
		}
		// FE schema requires started_at; fall back to "now" when DB has NULL
		// (calls in ringing state may not have started_at yet) so the projection
		// remains a valid time string instead of breaking the Zod parse.
		if startedAt != nil {
			s.StartedAt = *startedAt
		} else {
			s.StartedAt = time.Now()
		}
		s.TopParticipants = []entity.TopParticipant{}
		summaries = append(summaries, s)
		callIDs = append(callIDs, s.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list active call summaries rows: %w", err)
	}
	if len(summaries) == 0 {
		return summaries, nil
	}

	const topQuery = `
		SELECT call_id, user_id, display_name, avatar_url, avatar_color
		FROM (
			SELECT
				cp.call_id,
				cp.user_id,
				u.display_name,
				u.avatar_url,
				u.avatar_color,
				ROW_NUMBER() OVER (
					PARTITION BY cp.call_id
					ORDER BY cp.joined_at NULLS LAST, cp.user_id
				) AS rn
			FROM call_participants cp
			JOIN users u ON u.id = cp.user_id
			WHERE cp.call_id = ANY($1)
			  AND cp.status = 'connected'
			  AND cp.role <> 'viewer'
		) ranked
		WHERE rn <= $2`

	topRows, err := r.db.Query(ctx, topQuery, callIDs, topParticipantsLimit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list top participants: %w", err)
	}
	defer topRows.Close()

	byCall := map[uuid.UUID][]entity.TopParticipant{}
	for topRows.Next() {
		var callID uuid.UUID
		var p entity.TopParticipant
		if err := topRows.Scan(&callID, &p.UserID, &p.DisplayName, &p.AvatarURL, &p.AvatarColor); err != nil {
			return nil, fmt.Errorf("postgres: list top participants scan: %w", err)
		}
		byCall[callID] = append(byCall[callID], p)
	}
	if err := topRows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list top participants rows: %w", err)
	}

	for i := range summaries {
		if tops, ok := byCall[summaries[i].ID]; ok {
			summaries[i].TopParticipants = tops
		}
	}

	return summaries, nil
}

func (r *CallRepo) ListRecentByWorkspace(ctx context.Context, workspaceID uuid.UUID, limit int, before *time.Time) ([]entity.Call, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	query := `
		SELECT id, workspace_id, channel_id, type, status, title, created_by, scheduled_call_id, settings, started_at, ended_at, COALESCE(end_reason, ''), featured_share_user_id, created_at
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

	calls := make([]entity.Call, 0)
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
			&call.ScheduledCallID,
			&settingsJSON,
			&call.StartedAt,
			&call.EndedAt,
			&call.EndReason,
			&call.FeaturedShareUserID,
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

func (r *CallRepo) UpdateSettings(ctx context.Context, id uuid.UUID, settings entity.CallSettings) error {
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("postgres: marshal call settings: %w", err)
	}

	query := `
		UPDATE calls
		SET settings = $2
		WHERE id = $1`

	tag, err := r.db.Exec(ctx, query, id, settingsJSON)
	if err != nil {
		return fmt.Errorf("postgres: update call settings: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("call not found")
	}

	return nil
}

func (r *CallRepo) ActivateRinging(ctx context.Context, id uuid.UUID) (bool, error) {
	query := `
		UPDATE calls
		SET status = 'active',
		    started_at = now()
		WHERE id = $1 AND status = 'ringing'`

	tag, err := r.db.Exec(ctx, query, id)
	if err != nil {
		return false, fmt.Errorf("postgres: activate ringing call: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	if err := r.ensureCallExists(ctx, id); err != nil {
		return false, err
	}
	return false, nil
}

func (r *CallRepo) End(ctx context.Context, id uuid.UUID) error {
	return r.EndWithReason(ctx, id, "")
}

func (r *CallRepo) EndWithReason(ctx context.Context, id uuid.UUID, reason entity.CallEndReason) error {
	_, err := r.EndWithReasonIfNotEnded(ctx, id, reason)
	return err
}

func (r *CallRepo) EndWithReasonIfNotEnded(ctx context.Context, id uuid.UUID, reason entity.CallEndReason) (bool, error) {
	now := time.Now().UTC()
	query := `
		UPDATE calls
		SET status = 'ended',
		    ended_at = COALESCE(ended_at, $2),
		    end_reason = COALESCE(end_reason, $3)
		WHERE id = $1 AND status <> 'ended'`

	tag, err := r.db.Exec(ctx, query, id, now, nullableCallEndReason(reason))
	if err != nil {
		return false, fmt.Errorf("postgres: end call: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	if err := r.ensureCallExists(ctx, id); err != nil {
		return false, err
	}
	return false, nil
}

func (r *CallRepo) CancelRingingWithReason(ctx context.Context, id uuid.UUID, reason entity.CallEndReason) (bool, error) {
	now := time.Now().UTC()
	query := `
		UPDATE calls
		SET status = 'ended',
		    ended_at = COALESCE(ended_at, $2),
		    end_reason = COALESCE(end_reason, $3)
		WHERE id = $1 AND status = 'ringing'`

	tag, err := r.db.Exec(ctx, query, id, now, nullableCallEndReason(reason))
	if err != nil {
		return false, fmt.Errorf("postgres: cancel ringing call: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	if err := r.ensureCallExists(ctx, id); err != nil {
		return false, err
	}
	return false, nil
}

func (r *CallRepo) ensureCallExists(ctx context.Context, id uuid.UUID) error {
	var exists bool
	if err := r.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM calls WHERE id = $1)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("postgres: check call exists: %w", err)
	}
	if !exists {
		return cerrors.NotFound("call not found")
	}
	return nil
}

func (r *CallRepo) ensureCallParticipantExists(ctx context.Context, id uuid.UUID) error {
	var exists bool
	if err := r.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM call_participants WHERE id = $1)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("postgres: check call participant exists: %w", err)
	}
	if !exists {
		return cerrors.NotFound("call participant not found")
	}
	return nil
}

// --- Participant methods ---

func (r *CallRepo) AddParticipant(ctx context.Context, p *entity.CallParticipant) error {
	query := `
		INSERT INTO call_participants (id, call_id, user_id, role, status, audio_muted, video_muted, screen_sharing, can_screen_share, joined_at, left_at, left_reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`

	_, err := r.db.Exec(ctx, query,
		p.ID,
		p.CallID,
		p.UserID,
		p.Role,
		p.Status,
		p.AudioMuted,
		p.VideoMuted,
		p.ScreenSharing,
		p.CanScreenShare,
		p.JoinedAt,
		p.LeftAt,
		nullableParticipantLeftReason(p.LeftReason),
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
		INSERT INTO call_participants (id, call_id, user_id, role, status, audio_muted, video_muted, screen_sharing, can_screen_share, joined_at, left_at, left_reason)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
		WHERE (
			SELECT COUNT(*) FROM call_participants
			WHERE call_id = $2 AND status IN ('connected', 'joining')
		) < $13`

	tag, err := r.db.Exec(ctx, query,
		p.ID, p.CallID, p.UserID, p.Role, p.Status,
		p.AudioMuted, p.VideoMuted, p.ScreenSharing, p.CanScreenShare,
		p.JoinedAt, p.LeftAt, nullableParticipantLeftReason(p.LeftReason), maxParticipants,
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
		SELECT cp.id, cp.call_id, cp.user_id, cp.breakout_room_id, cp.role, cp.status, cp.audio_muted, cp.video_muted, cp.screen_sharing, cp.can_screen_share, cp.joined_at, cp.left_at, COALESCE(cp.left_reason, ''),
		       COALESCE(u.display_name, ''),
		       (EXISTS (SELECT 1 FROM guest_access_grants g WHERE g.user_id = cp.user_id AND g.workspace_id = c.workspace_id AND g.expires_at > now())
		        AND NOT EXISTS (SELECT 1 FROM workspace_members wm WHERE wm.workspace_id = c.workspace_id AND wm.user_id = cp.user_id)) AS is_guest
		FROM call_participants cp
		JOIN calls c ON c.id = cp.call_id
		LEFT JOIN users u ON u.id = cp.user_id
		WHERE cp.call_id = $1 AND cp.user_id = $2`

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
		&p.CanScreenShare,
		&p.JoinedAt,
		&p.LeftAt,
		&p.LeftReason,
		&p.DisplayName,
		&p.IsGuest,
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
		SELECT cp.id, cp.call_id, cp.user_id, cp.breakout_room_id, cp.role, cp.status, cp.audio_muted, cp.video_muted, cp.screen_sharing, cp.can_screen_share, cp.joined_at, cp.left_at, COALESCE(cp.left_reason, ''),
		       COALESCE(u.display_name, ''),
		       (EXISTS (SELECT 1 FROM guest_access_grants g WHERE g.user_id = cp.user_id AND g.workspace_id = c.workspace_id AND g.expires_at > now())
		        AND NOT EXISTS (SELECT 1 FROM workspace_members wm WHERE wm.workspace_id = c.workspace_id AND wm.user_id = cp.user_id)) AS is_guest
		FROM call_participants cp
		JOIN calls c ON c.id = cp.call_id
		LEFT JOIN users u ON u.id = cp.user_id
		WHERE cp.call_id = $1
		ORDER BY cp.joined_at`

	rows, err := r.db.Query(ctx, query, callID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list call participants: %w", err)
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
			&p.CanScreenShare,
			&p.JoinedAt,
			&p.LeftAt,
			&p.LeftReason,
			&p.DisplayName,
			&p.IsGuest,
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
	return r.UpdateParticipantStatusWithReason(ctx, id, status, "")
}

func (r *CallRepo) UpdateParticipantStatusWithReason(ctx context.Context, id uuid.UUID, status entity.ParticipantStatus, leftReason entity.ParticipantLeftReason) error {
	now := time.Now().UTC()
	query := `
		UPDATE call_participants
		SET status = $2,
		    joined_at = CASE
		        WHEN $2 = 'connected' AND joined_at IS NULL THEN $3
		        ELSE joined_at
		    END,
		    left_at = CASE
		        WHEN $2 = 'disconnected' AND left_at IS NULL THEN $3
		        WHEN $2 = 'connected' THEN NULL
		        ELSE left_at
		    END,
		    left_reason = CASE
		        WHEN $2 = 'disconnected' THEN $4
		        WHEN $2 = 'connected' THEN NULL
		        ELSE left_reason
		    END
		WHERE id = $1`

	tag, err := r.db.Exec(ctx, query, id, status, now, nullableParticipantLeftReason(leftReason))
	if err != nil {
		return fmt.Errorf("postgres: update participant status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("call participant not found")
	}

	return nil
}

func (r *CallRepo) DisconnectParticipantIfConnectedWithReason(ctx context.Context, id uuid.UUID, leftReason entity.ParticipantLeftReason) (bool, error) {
	now := time.Now().UTC()
	query := `
		UPDATE call_participants
		SET status = 'disconnected',
		    left_at = COALESCE(left_at, $2),
		    left_reason = COALESCE(left_reason, $3)
		WHERE id = $1 AND status <> 'disconnected'`

	tag, err := r.db.Exec(ctx, query, id, now, nullableParticipantLeftReason(leftReason))
	if err != nil {
		return false, fmt.Errorf("postgres: disconnect participant if connected: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	if err := r.ensureCallParticipantExists(ctx, id); err != nil {
		return false, err
	}
	return false, nil
}

func (r *CallRepo) UpdateParticipantRole(ctx context.Context, id uuid.UUID, role entity.CallRole) error {
	// `role <> 'host'` guard: the host slot is single-occupancy and only changes
	// hands through TransferHost. This also closes a race where a generic role
	// update (which read the target as non-host) could otherwise clobber a host
	// that a concurrent TransferHost just promoted, leaving the call with zero
	// hosts. (ALK-696)
	query := `
		UPDATE call_participants
		SET role = $2
		WHERE id = $1 AND role <> 'host'`

	tag, err := r.db.Exec(ctx, query, id, role)
	if err != nil {
		return fmt.Errorf("postgres: update participant role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("call participant not found")
	}

	return nil
}

func (r *CallRepo) TransferHost(ctx context.Context, callID, fromUserID, toUserID uuid.UUID) (bool, error) {
	// Lock both participant rows up front (FOR UPDATE) so concurrent transfers
	// serialise on them and each observes the other's committed state, then
	// validate and swap inside the same transaction. This is all-or-nothing — the
	// call can never end up with zero or two hosts — and the loser of a race sees
	// the actor is no longer host (or the target no longer eligible) and reports a
	// no-op the caller turns into a retryable conflict. (ALK-696)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("postgres: transfer host begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Deterministic lock order (by user_id) avoids deadlocks between two
	// transfers that touch the same pair of rows.
	rows, err := tx.Query(ctx, `
		SELECT user_id, role, status
		FROM call_participants
		WHERE call_id = $1 AND user_id IN ($2, $3)
		ORDER BY user_id
		FOR UPDATE`, callID, fromUserID, toUserID)
	if err != nil {
		return false, fmt.Errorf("postgres: transfer host lock: %w", err)
	}
	var fromIsHost, targetEligible bool
	for rows.Next() {
		var uid uuid.UUID
		var role, status string
		if err := rows.Scan(&uid, &role, &status); err != nil {
			rows.Close()
			return false, fmt.Errorf("postgres: transfer host scan: %w", err)
		}
		switch uid {
		case fromUserID:
			fromIsHost = role == string(entity.CallRoleHost)
		case toUserID:
			targetEligible = role != string(entity.CallRoleHost) &&
				status == string(entity.ParticipantStatusConnected)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("postgres: transfer host rows: %w", err)
	}
	if !fromIsHost || !targetEligible {
		return false, nil
	}

	if _, err := tx.Exec(ctx,
		`UPDATE call_participants SET role = $3 WHERE call_id = $1 AND user_id = $2`,
		callID, fromUserID, entity.CallRoleParticipant); err != nil {
		return false, fmt.Errorf("postgres: transfer host demote: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE call_participants SET role = $3 WHERE call_id = $1 AND user_id = $2`,
		callID, toUserID, entity.CallRoleHost); err != nil {
		return false, fmt.Errorf("postgres: transfer host promote: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("postgres: transfer host commit: %w", err)
	}
	return true, nil
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

// SetCanScreenShare flips a participant's screen-share grant, keyed by the
// participant id (uuid) to match UpdateParticipantMedia. (ALK-697)
func (r *CallRepo) SetCanScreenShare(ctx context.Context, id uuid.UUID, canShare bool) error {
	_, err := r.db.Exec(ctx, `UPDATE call_participants SET can_screen_share = $2 WHERE id = $1`, id, canShare)
	if err != nil {
		return fmt.Errorf("postgres: set participant can_screen_share: %w", err)
	}
	return nil
}

// SetFeaturedShareUserID sets (or clears with nil) the host-featured share pick
// on a call row. Passing nil writes SQL NULL. (ALK-697)
func (r *CallRepo) SetFeaturedShareUserID(ctx context.Context, callID uuid.UUID, userID *uuid.UUID) error {
	_, err := r.db.Exec(ctx, `UPDATE calls SET featured_share_user_id = $2 WHERE id = $1`, callID, userID)
	if err != nil {
		return fmt.Errorf("postgres: set call featured_share_user_id: %w", err)
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
