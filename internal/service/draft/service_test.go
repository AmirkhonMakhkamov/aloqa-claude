package draft

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

type fakeStore struct {
	upserted     *Draft
	deleteCalled bool
}

func (f *fakeStore) Upsert(_ context.Context, d *Draft) error {
	f.upserted = d
	return nil
}

func (f *fakeStore) ListByWorkspaceUser(_ context.Context, _, _ uuid.UUID) ([]Draft, error) {
	return nil, nil
}

func (f *fakeStore) Delete(_ context.Context, _, _ uuid.UUID, _ *uuid.UUID) error {
	f.deleteCalled = true
	return nil
}

func TestUpsertRejectsInvalidContent(t *testing.T) {
	svc := NewService(&fakeStore{})
	ctx := context.Background()
	ws, ch, user := uuid.New(), uuid.New(), uuid.New()

	if _, err := svc.Upsert(ctx, ws, ch, user, nil, nil); err == nil {
		t.Fatal("empty content should be rejected")
	}
	if _, err := svc.Upsert(ctx, ws, ch, user, nil, json.RawMessage("{not json")); err == nil {
		t.Fatal("invalid JSON content should be rejected")
	}
}

func TestUpsertStampsAndStores(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store)
	parent := uuid.New()

	d, err := svc.Upsert(
		context.Background(),
		uuid.New(), uuid.New(), uuid.New(),
		&parent,
		json.RawMessage(`{"text":"hi"}`),
	)
	if err != nil {
		t.Fatalf("Upsert returned error: %v", err)
	}
	if store.upserted == nil {
		t.Fatal("draft was not persisted")
	}
	if d.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt was not stamped server-side")
	}
	if store.upserted.ParentMessageID == nil || *store.upserted.ParentMessageID != parent {
		t.Fatalf("ParentMessageID = %v, want %s", store.upserted.ParentMessageID, parent)
	}
}

func TestDeleteDelegates(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store)
	if err := svc.Delete(context.Background(), uuid.New(), uuid.New(), nil); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if !store.deleteCalled {
		t.Fatal("Delete did not reach the store")
	}
}
