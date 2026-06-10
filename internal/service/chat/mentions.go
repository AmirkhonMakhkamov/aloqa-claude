package chat

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
)

// MentionsLister exposes the direct mentions query implemented by the
// postgres repository. The chat service depends on this narrow extension
// interface so it does not need to re-thread DB plumbing.
type MentionsLister interface {
	ListMentions(ctx context.Context, workspaceID, userID uuid.UUID, limit int) ([]entity.MentionRow, error)
}

// ListMentions returns recent @-mentions of the calling user across every
// channel they are a member of inside the workspace.
//
// Handle resolution is done in SQL — the query JOINs `users` to derive both
// supported mention handles: email local-part and display name with whitespace
// replaced by underscores. Self-mentions and tombstoned messages are excluded.
func (s *Service) ListMentions(
	ctx context.Context,
	workspaceID, userID uuid.UUID,
	limit int,
) ([]entity.MentionRow, error) {
	if err := s.CanAccessWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}

	lister, ok := s.messages.(MentionsLister)
	if !ok {
		return nil, cerrors.Internal("mentions lister unavailable", nil)
	}

	if limit <= 0 || limit > 100 {
		limit = 50
	}

	rows, err := lister.ListMentions(ctx, workspaceID, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("chat: list mentions: %w", err)
	}

	if rows == nil {
		return []entity.MentionRow{}, nil
	}
	return rows, nil
}
