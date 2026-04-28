package entity

import (
	"time"

	"github.com/google/uuid"
)

type GuestAccessGrant struct {
	ID          uuid.UUID   `json:"id"`
	InviteID    uuid.UUID   `json:"invite_id"`
	WorkspaceID uuid.UUID   `json:"workspace_id"`
	UserID      uuid.UUID   `json:"user_id"`
	ChannelIDs  []uuid.UUID `json:"channel_ids,omitempty"`
	ExpiresAt   time.Time   `json:"expires_at"`
	CreatedAt   time.Time   `json:"created_at"`
}

func (g GuestAccessGrant) IsActive(now time.Time) bool {
	return !now.After(g.ExpiresAt)
}

func (g GuestAccessGrant) AllowsChannel(channelID uuid.UUID) bool {
	if len(g.ChannelIDs) == 0 {
		return true
	}
	for _, allowed := range g.ChannelIDs {
		if allowed == channelID {
			return true
		}
	}
	return false
}
