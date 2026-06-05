package entity

import (
	"time"

	"github.com/google/uuid"
)

// SavedFrom is metadata attached to a message copied into Saved Messages.
// It is distinct from forwarded_from; both may coexist on a saved copy.
type SavedFrom struct {
	UserID       uuid.UUID  `json:"user_id"`
	DisplayName  string     `json:"display_name"`
	AvatarColor  string     `json:"avatar_color"`
	Department   *string    `json:"department,omitempty"`
	Position     *string    `json:"position,omitempty"`
	MessageID    uuid.UUID  `json:"message_id"`
	ChannelID    uuid.UUID  `json:"channel_id"`
	WorkspaceID  *uuid.UUID `json:"workspace_id,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}
