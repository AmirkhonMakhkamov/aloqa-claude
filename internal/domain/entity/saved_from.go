package entity

import (
	"time"

	"github.com/google/uuid"
)

// SavedFrom is metadata attached to a message copied into Saved Messages.
// It is distinct from forwarded_from; both may coexist on a saved copy.
type SavedFrom struct {
	UserID      uuid.UUID  `json:"user_id"`
	MessageID   uuid.UUID  `json:"message_id"`
	ChannelID   uuid.UUID  `json:"channel_id"`
	WorkspaceID *uuid.UUID `json:"workspace_id,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}
