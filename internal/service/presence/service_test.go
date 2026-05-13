package presence

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/event"
)

type capturedPublish struct {
	subject string
	data    []byte
}

type fakePublisher struct {
	mu       sync.Mutex
	captured []capturedPublish
	err      error
}

func (f *fakePublisher) Publish(_ context.Context, subject string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	cloned := make([]byte, len(data))
	copy(cloned, data)
	f.captured = append(f.captured, capturedPublish{subject: subject, data: cloned})
	return nil
}

func (f *fakePublisher) snapshot() []capturedPublish {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capturedPublish, len(f.captured))
	copy(out, f.captured)
	return out
}

// TestPublishPresenceChanged_OnlineHasNoLastSeen asserts that the helper emits
// a presence.changed event on aloqa.ws.<workspaceID> with the expected shape
// when transitioning to online.
func TestPublishPresenceChanged_OnlineHasNoLastSeen(t *testing.T) {
	pub := &fakePublisher{}
	svc := &Service{pubsub: pub}

	userID := uuid.New()
	workspaceID := uuid.New()

	svc.publishPresenceChanged(context.Background(), userID, workspaceID, StatusOnline, time.Now().UTC())

	captured := pub.snapshot()
	if len(captured) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(captured))
	}
	if got, want := captured[0].subject, "aloqa.ws."+workspaceID.String(); got != want {
		t.Fatalf("subject = %q, want %q", got, want)
	}

	var evt event.Event
	if err := json.Unmarshal(captured[0].data, &evt); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if evt.Type != event.TypePresenceChanged {
		t.Errorf("evt.Type = %q, want %q", evt.Type, event.TypePresenceChanged)
	}
	if evt.WorkspaceID != workspaceID {
		t.Errorf("evt.WorkspaceID = %s, want %s", evt.WorkspaceID, workspaceID)
	}
	if evt.UserID != userID {
		t.Errorf("evt.UserID = %s, want %s", evt.UserID, userID)
	}

	// Payload assertions — re-marshal to inspect the typed payload.
	raw, err := json.Marshal(evt.Payload)
	if err != nil {
		t.Fatalf("re-marshal payload: %v", err)
	}
	var payload event.PresencePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Status != string(StatusOnline) {
		t.Errorf("payload.Status = %q, want %q", payload.Status, StatusOnline)
	}
	if !payload.Online {
		t.Error("payload.Online = false, want true")
	}
	if payload.LastSeenAt != nil {
		t.Errorf("payload.LastSeenAt = %v, want nil for online", payload.LastSeenAt)
	}
	if payload.WorkspaceID != workspaceID {
		t.Errorf("payload.WorkspaceID = %s, want %s", payload.WorkspaceID, workspaceID)
	}
}

// TestPublishPresenceChanged_AwaySetsLastSeen asserts that away/offline
// transitions include a LastSeenAt timestamp so clients can render "last seen
// 5m ago" style hints.
func TestPublishPresenceChanged_AwaySetsLastSeen(t *testing.T) {
	pub := &fakePublisher{}
	svc := &Service{pubsub: pub}

	userID := uuid.New()
	workspaceID := uuid.New()
	lastSeen := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)

	svc.publishPresenceChanged(context.Background(), userID, workspaceID, StatusAway, lastSeen)

	captured := pub.snapshot()
	if len(captured) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(captured))
	}

	var evt event.Event
	if err := json.Unmarshal(captured[0].data, &evt); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	raw, err := json.Marshal(evt.Payload)
	if err != nil {
		t.Fatalf("re-marshal payload: %v", err)
	}
	var payload event.PresencePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Status != string(StatusAway) {
		t.Errorf("payload.Status = %q, want %q", payload.Status, StatusAway)
	}
	if payload.Online {
		t.Error("payload.Online = true, want false")
	}
	if payload.LastSeenAt == nil || !payload.LastSeenAt.Equal(lastSeen) {
		t.Errorf("payload.LastSeenAt = %v, want %v", payload.LastSeenAt, lastSeen)
	}
}

// TestPublishPresenceChanged_NoPublisher must not panic and must skip silently
// when EventPublisher is unset (graceful degradation path).
func TestPublishPresenceChanged_NoPublisher(t *testing.T) {
	svc := &Service{}
	// Should not panic, should not block.
	svc.publishPresenceChanged(context.Background(), uuid.New(), uuid.New(), StatusOnline, time.Now())
}

// TestPublishPresenceChanged_NilWorkspaceSkipped ensures we never publish to
// aloqa.ws.<nil-uuid>.
func TestPublishPresenceChanged_NilWorkspaceSkipped(t *testing.T) {
	pub := &fakePublisher{}
	svc := &Service{pubsub: pub}

	svc.publishPresenceChanged(context.Background(), uuid.New(), uuid.Nil, StatusOnline, time.Now())

	if got := len(pub.snapshot()); got != 0 {
		t.Fatalf("expected 0 publishes for nil workspace, got %d", got)
	}
}

// TestPublishPresenceChanged_PublishErrorSwallowed verifies that publish
// errors are logged but never bubble out — presence mutation must remain a
// 204-success even if NATS is temporarily down.
func TestPublishPresenceChanged_PublishErrorSwallowed(t *testing.T) {
	pub := &fakePublisher{err: errors.New("nats down")}
	svc := &Service{pubsub: pub}

	// Helper does not return an error; the test simply asserts it doesn't panic.
	svc.publishPresenceChanged(context.Background(), uuid.New(), uuid.New(), StatusOnline, time.Now())
}
