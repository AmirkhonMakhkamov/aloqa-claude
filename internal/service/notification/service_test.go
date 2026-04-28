package notification

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"aloqa/internal/pkg/cerrors"
)

func TestSendReturnsStoreSaveError(t *testing.T) {
	storeErr := errors.New("store unavailable")
	svc := NewService(&fakeStore{saveErr: storeErr})

	err := svc.Send(context.Background(), uuid.New(), uuid.New(), TypeMention, "title", "body", "resource")
	if err == nil {
		t.Fatalf("expected send to fail when in-app notification persistence fails")
	}
	appErr, ok := cerrors.AsAppError(err)
	if !ok || appErr.Code != cerrors.CodeInternal {
		t.Fatalf("expected internal app error, got %v", err)
	}
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected wrapped store error, got %v", err)
	}
}

type fakeStore struct {
	saveErr error
}

func (s *fakeStore) Save(context.Context, *Notification) error {
	return s.saveErr
}

func (s *fakeStore) ListByUser(context.Context, uuid.UUID, uuid.UUID, int) ([]Notification, error) {
	return nil, nil
}

func (s *fakeStore) MarkRead(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (s *fakeStore) MarkAllRead(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (s *fakeStore) CountUnread(context.Context, uuid.UUID, uuid.UUID) (int, error) {
	return 0, nil
}
