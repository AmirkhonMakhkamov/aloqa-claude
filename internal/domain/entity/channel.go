package entity

import (
	"time"

	"github.com/google/uuid"
)

type ChannelType string

const (
	ChannelTypePublic      ChannelType = "public"
	ChannelTypePrivate     ChannelType = "private"
	ChannelTypeDM          ChannelType = "dm"
	ChannelTypeGroupDM     ChannelType = "group_dm"
	ChannelTypeSaved       ChannelType = "saved"
	ChannelTypeSavedGlobal ChannelType = "saved_global"
)

func (t ChannelType) IsSelfChannel() bool {
	return t == ChannelTypeSaved || t == ChannelTypeSavedGlobal
}

type ChannelRole string

const (
	ChannelRoleOwner  ChannelRole = "owner"
	ChannelRoleAdmin  ChannelRole = "admin"
	ChannelRoleMember ChannelRole = "member"
)

type Channel struct {
	ID          uuid.UUID   `json:"id"`
	WorkspaceID *uuid.UUID  `json:"workspace_id"`
	Name        string      `json:"name"`
	Topic       *string     `json:"topic,omitempty"`
	Type        ChannelType `json:"type"`
	CreatedBy   uuid.UUID   `json:"created_by"`
	OwnerUserID *uuid.UUID  `json:"owner_user_id,omitempty"`
	Archived    bool        `json:"archived"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	// Members is populated only for DM and group DM channels so the client can
	// resolve the counterpart user(s) without an extra round-trip per row in
	// the sidebar. Kept omitempty so non-DM channels keep their existing shape.
	Members []uuid.UUID `json:"members,omitempty"`
}

type ChannelMember struct {
	ID         uuid.UUID   `json:"id"`
	ChannelID  uuid.UUID   `json:"channel_id"`
	UserID     uuid.UUID   `json:"user_id"`
	Role       ChannelRole `json:"role"`
	MutedUntil *time.Time  `json:"muted_until,omitempty"`
	LastReadAt time.Time   `json:"last_read_at"`
	JoinedAt   time.Time   `json:"joined_at"`
}
