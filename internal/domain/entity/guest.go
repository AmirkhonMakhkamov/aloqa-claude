package entity

import (
	"time"

	"github.com/google/uuid"
)

// GuestInviteStatus tracks invite lifecycle.
type GuestInviteStatus string

const (
	GuestInviteStatusActive   GuestInviteStatus = "active"
	GuestInviteStatusUsed     GuestInviteStatus = "used"
	GuestInviteStatusExpired  GuestInviteStatus = "expired"
	GuestInviteStatusRevoked  GuestInviteStatus = "revoked"
)

// GuestInvite is a time-limited, scope-limited invitation for external users.
type GuestInvite struct {
	ID          uuid.UUID         `json:"id"`
	WorkspaceID uuid.UUID         `json:"workspace_id"`
	CreatedBy   uuid.UUID         `json:"created_by"`
	Token       string            `json:"token"`
	Email       string            `json:"email,omitempty"`       // Optional pre-assigned email
	ChannelIDs  []uuid.UUID       `json:"channel_ids,omitempty"` // Channels the guest can access
	MaxUses     int               `json:"max_uses"`              // 0 = unlimited
	UseCount    int               `json:"use_count"`
	Status      GuestInviteStatus `json:"status"`
	ExpiresAt   time.Time         `json:"expires_at"`
	CreatedAt   time.Time         `json:"created_at"`
}

// IsValid checks whether the invite can still be redeemed.
func (g *GuestInvite) IsValid() bool {
	if g.Status != GuestInviteStatusActive {
		return false
	}
	if time.Now().After(g.ExpiresAt) {
		return false
	}
	if g.MaxUses > 0 && g.UseCount >= g.MaxUses {
		return false
	}
	return true
}
