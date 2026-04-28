package extension

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/google/uuid"
)

func TestHookDispatcherDispatchesTypedAndWildcardHandlers(t *testing.T) {
	dispatcher := NewHookDispatcher()
	workspaceID := uuid.New()
	var calls int

	dispatcher.Register(HookCallStarted, HookHandlerFunc(func(_ context.Context, event HookEvent) error {
		calls++
		if event.ID == uuid.Nil {
			t.Fatalf("event ID was not populated")
		}
		if event.Timestamp.IsZero() {
			t.Fatalf("event timestamp was not populated")
		}
		if event.IdempotencyKey == "" {
			t.Fatalf("event idempotency key was not populated")
		}
		if event.WorkspaceID != workspaceID {
			t.Fatalf("workspace ID = %s, want %s", event.WorkspaceID, workspaceID)
		}
		return nil
	}))
	dispatcher.Register("*", HookHandlerFunc(func(context.Context, HookEvent) error {
		calls++
		return nil
	}))

	if err := dispatcher.Dispatch(context.Background(), HookEvent{Type: HookCallStarted, WorkspaceID: workspaceID}); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("handler calls = %d, want 2", calls)
	}
}

func TestHookDispatcherAggregatesHandlerErrors(t *testing.T) {
	dispatcher := NewHookDispatcher()
	errOne := errors.New("one")
	errTwo := errors.New("two")
	dispatcher.Register(HookRecordingReady, HookHandlerFunc(func(context.Context, HookEvent) error { return errOne }))
	dispatcher.Register(HookRecordingReady, HookHandlerFunc(func(context.Context, HookEvent) error { return errTwo }))

	err := dispatcher.Dispatch(context.Background(), HookEvent{Type: HookRecordingReady})
	if !errors.Is(err, errOne) || !errors.Is(err, errTwo) {
		t.Fatalf("Dispatch error = %v, want joined handler errors", err)
	}
}

func TestRegistryReturnsConfiguredProviders(t *testing.T) {
	ai := fakeAIProvider{}
	registry := NewRegistry(WithAI(ai))

	got, ok := registry.AI()
	if !ok {
		t.Fatalf("AI provider was not configured")
	}
	if got == nil {
		t.Fatalf("AI provider is nil")
	}
	if _, ok := registry.Telephony(); ok {
		t.Fatalf("telephony provider should not be configured")
	}
	if registry.Hooks() == nil {
		t.Fatalf("hook dispatcher is nil")
	}
}

type fakeAIProvider struct{}

func (fakeAIProvider) Transcribe(context.Context, io.Reader, string, string) (*TranscriptionResult, error) {
	return nil, nil
}
func (fakeAIProvider) Summarize(context.Context, string, int) (string, error) { return "", nil }
func (fakeAIProvider) Translate(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (fakeAIProvider) ExtractTasks(context.Context, string) ([]ExtractedTask, error) {
	return nil, nil
}
