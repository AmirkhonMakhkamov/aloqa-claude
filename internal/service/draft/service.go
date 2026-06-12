// Package draft implements server-backed message drafts so a user's unsent
// composer text follows them across devices (ALOQA-247 draft sync). Drafts are
// strictly per-user and keyed by channel and (optionally) thread root.
package draft

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/pkg/cerrors"
)

// maxContentBytes caps a stored draft payload (composer state JSON) to keep
// unsent content small and bound abuse.
const maxContentBytes = 64 * 1024

// Draft is a user's unsent composer state for a channel or a thread root.
type Draft struct {
	WorkspaceID     uuid.UUID       `json:"workspace_id"`
	ChannelID       uuid.UUID       `json:"channel_id"`
	UserID          uuid.UUID       `json:"-"`
	ParentMessageID *uuid.UUID      `json:"parent_message_id,omitempty"`
	Content         json.RawMessage `json:"content"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// Store is the persistence contract for drafts.
type Store interface {
	Upsert(ctx context.Context, d *Draft) error
	ListByWorkspaceUser(ctx context.Context, workspaceID, userID uuid.UUID) ([]Draft, error)
	Delete(ctx context.Context, userID, channelID uuid.UUID, parentMessageID *uuid.UUID) error
}

// Service orchestrates draft persistence.
type Service struct {
	store Store
}

// NewService creates a new draft service.
func NewService(store Store) *Service {
	return &Service{store: store}
}

// Upsert stores (or replaces) the caller's draft for a channel/thread. The
// server stamps UpdatedAt, giving last-write-wins ordering that is free of
// client clock skew; the client uses it to avoid clobbering locally-newer text.
func (s *Service) Upsert(
	ctx context.Context,
	workspaceID, channelID, userID uuid.UUID,
	parentMessageID *uuid.UUID,
	content json.RawMessage,
) (*Draft, error) {
	if len(content) == 0 {
		return nil, cerrors.InvalidInput("draft content is required")
	}
	if len(content) > maxContentBytes {
		return nil, cerrors.InvalidInput("draft content is too large")
	}
	if !json.Valid(content) {
		return nil, cerrors.InvalidInput("draft content must be valid JSON")
	}

	d := &Draft{
		WorkspaceID:     workspaceID,
		ChannelID:       channelID,
		UserID:          userID,
		ParentMessageID: parentMessageID,
		Content:         content,
		UpdatedAt:       time.Now().UTC(),
	}
	if err := s.store.Upsert(ctx, d); err != nil {
		return nil, cerrors.Internal("failed to save draft", err)
	}
	return d, nil
}

// List returns all of the caller's drafts in a workspace, for hydration.
func (s *Service) List(ctx context.Context, workspaceID, userID uuid.UUID) ([]Draft, error) {
	drafts, err := s.store.ListByWorkspaceUser(ctx, workspaceID, userID)
	if err != nil {
		return nil, cerrors.Internal("failed to list drafts", err)
	}
	return drafts, nil
}

// Delete removes the caller's draft for a channel/thread (e.g. on send or clear).
func (s *Service) Delete(
	ctx context.Context,
	channelID, userID uuid.UUID,
	parentMessageID *uuid.UUID,
) error {
	if err := s.store.Delete(ctx, userID, channelID, parentMessageID); err != nil {
		return cerrors.Internal("failed to delete draft", err)
	}
	return nil
}
