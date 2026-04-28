package entity

import (
	"time"

	"github.com/google/uuid"
)

// ChannelAccessState stores per-channel state for users who can participate in
// a channel without being a native channel member, such as guests and approved
// cross-workspace collaborators.
type ChannelAccessState struct {
	ChannelID   uuid.UUID `json:"channel_id"`
	UserID      uuid.UUID `json:"user_id"`
	LastReadAt  time.Time `json:"last_read_at"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
