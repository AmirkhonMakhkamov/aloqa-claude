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
	CallID      *uuid.UUID  `json:"call_id,omitempty"` // Set for call-scoped grants (any call type)
	ExpiresAt   time.Time   `json:"expires_at"`
	CreatedAt   time.Time   `json:"created_at"`
}

func (g GuestAccessGrant) IsActive(now time.Time) bool {
	return !now.After(g.ExpiresAt)
}

func (g GuestAccessGrant) AllowsChannel(channelID uuid.UUID) bool {
	if g.CallID != nil {
		return false // call-scoped grants never confer channel access
	}
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

// AllowsCall reports whether this grant is scoped to the given call. Only
// call-scoped grants (CallID != nil) allow a call.
func (g GuestAccessGrant) AllowsCall(callID uuid.UUID) bool {
	return g.CallID != nil && *g.CallID == callID
}
