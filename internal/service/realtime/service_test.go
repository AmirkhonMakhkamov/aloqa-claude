package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/event"
)

func TestPublishEnqueuesDurableEvents(t *testing.T) {
	store := &fakeStore{}
	transport := &fakeTransport{}
	svc := NewService(store, transport, Config{})

	evt := event.Event{
		ID:          uuid.New(),
		Type:        event.TypeMessageCreated,
		WorkspaceID: uuid.New(),
		UserID:      uuid.New(),
		Timestamp:   time.Now().UTC(),
		Payload:     map[string]any{"message": "hello"},
	}
	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	if err := svc.Publish(context.Background(), "aloqa.chat."+uuid.NewString(), data); err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}
	if len(store.enqueued) != 1 {
		t.Fatalf("enqueued %d events, want 1", len(store.enqueued))
	}
	if len(transport.published) != 0 {
		t.Fatalf("published %d transport messages, want 0", len(transport.published))
	}
	if store.enqueued[0].Event.DeliverySemantic != event.DeliveryAtLeastOnce || !store.enqueued[0].Event.Replayable {
		t.Fatalf("unexpected durable event definition: %+v", store.enqueued[0].Event)
	}
}

func TestPublishSendsEphemeralEventsDirectly(t *testing.T) {
	store := &fakeStore{}
	transport := &fakeTransport{}
	svc := NewService(store, transport, Config{})

	evt := event.Event{
		ID:          uuid.New(),
		Type:        event.TypeSignalOffer,
		WorkspaceID: uuid.New(),
		UserID:      uuid.New(),
		Timestamp:   time.Now().UTC(),
		Payload:     map[string]any{"sdp": "v=0"},
	}
	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	if err := svc.Publish(context.Background(), "aloqa.signal."+uuid.NewString(), data); err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}
	if len(store.enqueued) != 0 {
		t.Fatalf("enqueued %d events, want 0", len(store.enqueued))
	}
	if len(transport.published) != 1 {
		t.Fatalf("published %d transport messages, want 1", len(transport.published))
	}
}

func TestProcessPendingMarksDeadAfterMaxAttempts(t *testing.T) {
	store := &fakeStore{
		claimed: []event.QueuedEvent{{
			Event: event.Event{
				ID:               uuid.New(),
				Type:             event.TypeMessageCreated,
				WorkspaceID:      uuid.New(),
				UserID:           uuid.New(),
				Subject:          "aloqa.chat." + uuid.NewString(),
				DeliverySemantic: event.DeliveryAtLeastOnce,
				Replayable:       true,
				Timestamp:        time.Now().UTC(),
			},
			Body:        []byte(`{"type":"message.created"}`),
			Attempts:    8,
			MaxAttempts: 8,
		}},
	}
	transport := &fakeTransport{publishErr: errors.New("nats unavailable")}
	svc := NewService(store, transport, Config{MaxAttempts: 8})

	processed, failed, dead, err := svc.ProcessPending(context.Background())
	if err != nil {
		t.Fatalf("ProcessPending returned error: %v", err)
	}
	if processed != 0 || failed != 0 || dead != 1 {
		t.Fatalf("processed=%d failed=%d dead=%d, want 0/0/1", processed, failed, dead)
	}
	if len(store.failures) != 1 || !store.failures[0].dead {
		t.Fatalf("failures = %+v, want one dead-letter failure", store.failures)
	}
}

type fakeStore struct {
	enqueued []event.QueuedEvent
	claimed  []event.QueuedEvent
	failures []failureRecord
}

type failureRecord struct {
	id   uuid.UUID
	dead bool
}

func (f *fakeStore) Enqueue(_ context.Context, evt event.Event, body []byte, maxAttempts int) error {
	f.enqueued = append(f.enqueued, event.QueuedEvent{
		Event:       evt,
		Body:        append([]byte(nil), body...),
		MaxAttempts: maxAttempts,
	})
	return nil
}

func (f *fakeStore) ClaimPending(context.Context, int) ([]event.QueuedEvent, error) {
	return f.claimed, nil
}

func (f *fakeStore) MarkPublished(context.Context, uuid.UUID) error { return nil }

func (f *fakeStore) MarkFailed(_ context.Context, eventID uuid.UUID, _ string, _ *time.Time, dead bool) error {
	f.failures = append(f.failures, failureRecord{id: eventID, dead: dead})
	return nil
}

func (f *fakeStore) ReplayRoom(context.Context, string, int64, int) ([]event.Event, error) {
	return nil, nil
}

func (f *fakeStore) UpdateConsumerCursor(context.Context, string, string, *event.Event, bool, string, string) error {
	return nil
}

func (f *fakeStore) ResetStuckJobs(context.Context, time.Duration) (int64, error) { return 0, nil }

type fakeTransport struct {
	published  []string
	publishErr error
}

func (f *fakeTransport) PublishWithID(_ context.Context, subject string, _ []byte, _ string) error {
	f.published = append(f.published, subject)
	return f.publishErr
}
