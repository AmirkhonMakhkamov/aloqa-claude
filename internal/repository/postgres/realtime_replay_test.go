package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
)

// setupRealtimeReplayWorkspace creates the workspace + creator user that the
// realtime_events workspace_id foreign key requires. Deleting the workspace
// cascades any leftover realtime_events rows.
func setupRealtimeReplayWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (workspaceID, userID uuid.UUID) {
	t.Helper()

	workspaceID = uuid.New()
	userID = uuid.New()
	now := time.Now().UTC()

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM workspaces WHERE id = $1`, workspaceID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, display_name, avatar_url, password_hash, status, locale, created_at, updated_at)
		VALUES ($1, $2, 'Realtime Replay Test User', '', 'hash', 'active', 'en', $3, $3)`,
		userID, "realtime-replay-test-"+userID.String()+"@example.com", now,
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, name, slug, avatar_url, created_by, created_at, updated_at)
		VALUES ($1, 'Realtime Replay Test Workspace', $2, '', $3, $4, $4)`,
		workspaceID, "realtime-replay-test-"+workspaceID.String(), userID, now,
	); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	return workspaceID, userID
}

// insertPublishedReplayEvent seeds a published, replayable outbox row with an
// explicit created_at so freshness behaviour can be exercised. The body mirrors
// the real event envelope: when callID is non-nil it nests the id under
// payload.call.id, which is where the call-end tombstone matches it.
func insertPublishedReplayEvent(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, userID uuid.UUID,
	eventType event.Type,
	subject string,
	callID *uuid.UUID,
	createdAt time.Time,
) uuid.UUID {
	t.Helper()

	eventID := uuid.New()
	body := map[string]any{
		"id":      eventID.String(),
		"type":    string(eventType),
		"subject": subject,
	}
	if callID != nil {
		body["payload"] = map[string]any{"call": map[string]any{"id": callID.String()}}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal event body: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO realtime_events (
			id, version, type, subject, workspace_id, channel_id, user_id,
			delivery_semantic, replayable, body, created_at, available_at,
			max_attempts, status, last_error
		)
		VALUES ($1, 1, $2, $3, $4, NULL, $5, 'at_least_once', true, $6, $7, $7, 8, 'published', '')`,
		eventID, string(eventType), subject, workspaceID, userID, raw, createdAt,
	); err != nil {
		t.Fatalf("insert realtime event: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM realtime_events WHERE id = $1`, eventID)
	})
	return eventID
}

func replayEventIDSet(events []event.Event) map[uuid.UUID]bool {
	set := make(map[uuid.UUID]bool, len(events))
	for _, e := range events {
		set[e.ID] = true
	}
	return set
}

// TestCallRepoEndSupersedesCallStartedReplayEvent proves the call-end tombstone:
// ending a call flips ONLY its own durable call.started row to replayable=false,
// leaving other calls' start events and unrelated event types untouched, so an
// ended call can never re-seed a phantom in-call surface on a fresh socket.
func TestCallRepoEndSupersedesCallStartedReplayEvent(t *testing.T) {
	ctx, pool := setupCallMessageRepoPostgresTest(t)
	workspaceID, userID := setupRealtimeReplayWorkspace(t, ctx, pool)

	now := time.Now().UTC()
	endedCallID := uuid.New()
	unrelatedCallID := uuid.New()

	if _, err := pool.Exec(ctx, `
		INSERT INTO calls (id, workspace_id, type, status, title, created_by, settings, created_at)
		VALUES ($1, $2, 'group', 'active', 'Zombie Tombstone Test Call', $3, '{"chat": true}', $4)`,
		endedCallID, workspaceID, userID, now,
	); err != nil {
		t.Fatalf("insert call: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM calls WHERE id = $1`, endedCallID)
	})

	subject := "aloqa.ws." + workspaceID.String()
	endedEventID := insertPublishedReplayEvent(t, ctx, pool, workspaceID, userID, event.TypeCallStarted, subject, &endedCallID, now)
	unrelatedEventID := insertPublishedReplayEvent(t, ctx, pool, workspaceID, userID, event.TypeCallStarted, subject, &unrelatedCallID, now)
	messageEventID := insertPublishedReplayEvent(t, ctx, pool, workspaceID, userID, event.TypeMessageCreated, "aloqa.chat."+uuid.New().String(), nil, now)

	repo := NewCallRepo(pool)
	ended, err := repo.EndWithReasonIfNotEnded(ctx, endedCallID, entity.CallEndReasonHostEnded)
	if err != nil {
		t.Fatalf("EndWithReasonIfNotEnded: %v", err)
	}
	if !ended {
		t.Fatalf("EndWithReasonIfNotEnded returned ended=false, want true")
	}

	assertReplayable := func(label string, eventID uuid.UUID, want bool) {
		t.Helper()
		var replayable bool
		if err := pool.QueryRow(ctx, `SELECT replayable FROM realtime_events WHERE id = $1`, eventID).Scan(&replayable); err != nil {
			t.Fatalf("query replayable for %s: %v", label, err)
		}
		if replayable != want {
			t.Fatalf("%s event %s replayable = %v, want %v", label, eventID, replayable, want)
		}
	}
	assertReplayable("ended call.started", endedEventID, false)
	assertReplayable("unrelated call.started", unrelatedEventID, true)
	assertReplayable("message.created", messageEventID, true)
}

// TestRealtimeRepoReplayRoomBoundsOnlyCallStartedByFreshness proves the bounded
// replay is scoped to the zombie-call case: a >24h call.started is excluded so
// it cannot seed a phantom surface, but every other durable type (here
// message.created) still replays across an arbitrarily old cursor gap. A blanket
// time window would silently drop legitimate chat/calendar replay.
func TestRealtimeRepoReplayRoomBoundsOnlyCallStartedByFreshness(t *testing.T) {
	ctx, pool := setupCallMessageRepoPostgresTest(t)
	workspaceID, userID := setupRealtimeReplayWorkspace(t, ctx, pool)

	repo := NewRealtimeRepo(pool)
	now := time.Now().UTC()
	old := now.Add(-25 * time.Hour)
	recent := now.Add(-1 * time.Hour)

	// Chat messages on a channel subject must replay regardless of age.
	channelKey := uuid.New().String()
	chatSubject := "aloqa.chat." + channelKey
	oldMessageID := insertPublishedReplayEvent(t, ctx, pool, workspaceID, userID, event.TypeMessageCreated, chatSubject, nil, old)
	recentMessageID := insertPublishedReplayEvent(t, ctx, pool, workspaceID, userID, event.TypeMessageCreated, chatSubject, nil, recent)

	chatEvents, err := repo.ReplayRoom(ctx, "channel:"+channelKey, 0, 200)
	if err != nil {
		t.Fatalf("ReplayRoom(chat): %v", err)
	}
	chatIDs := replayEventIDSet(chatEvents)
	if !chatIDs[oldMessageID] {
		t.Fatalf("ReplayRoom dropped a >24h message.created (%s); only call.started should be freshness-bounded", oldMessageID)
	}
	if !chatIDs[recentMessageID] {
		t.Fatalf("ReplayRoom dropped a recent message.created (%s)", recentMessageID)
	}

	// call.started on the workspace subject IS freshness-bounded.
	wsSubject := "aloqa.ws." + workspaceID.String()
	oldCallID := uuid.New()
	recentCallID := uuid.New()
	oldCallStartedID := insertPublishedReplayEvent(t, ctx, pool, workspaceID, userID, event.TypeCallStarted, wsSubject, &oldCallID, old)
	recentCallStartedID := insertPublishedReplayEvent(t, ctx, pool, workspaceID, userID, event.TypeCallStarted, wsSubject, &recentCallID, recent)

	callEvents, err := repo.ReplayRoom(ctx, wsSubject, 0, 200)
	if err != nil {
		t.Fatalf("ReplayRoom(ws): %v", err)
	}
	callIDs := replayEventIDSet(callEvents)
	if callIDs[oldCallStartedID] {
		t.Fatalf("ReplayRoom replayed a >24h call.started (%s); zombie surfaces would reappear", oldCallStartedID)
	}
	if !callIDs[recentCallStartedID] {
		t.Fatalf("ReplayRoom dropped a recent call.started (%s)", recentCallStartedID)
	}
}
