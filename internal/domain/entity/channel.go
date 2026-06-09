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
	// LastActivityAt is the created_at of the most recent non-deleted message in
	// the channel, used by the client to order the sidebar by last activity
	// (ALK-837). Populated only by the per-user channel list (ListByUser); other
	// responses leave it nil and omitempty keeps their shape unchanged.
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
	// Members is populated for DM/group DM list rows and selected realtime
	// channel payloads so clients can decide whether the current user is part
	// of a channel without an extra round-trip. Kept omitempty so ordinary
	// non-DM channel responses keep their existing shape.
	Members []uuid.UUID `json:"members,omitempty"`
}

// MentionSuggestion is a channel member surfaced as an @mention autocomplete
// candidate (ALK-838). Username is the local part of the member's email — the
// handle the composer inserts as `@username`.
type MentionSuggestion struct {
	ID          uuid.UUID `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
	Position    *string   `json:"position"`
}

// ArchivedChannelInfo carries the per-row data the Archived Channels list
// view needs (ALK-617): the underlying channel plus the timestamps and
// member count required to render a meaningful row without per-row
// round-trips.
type ArchivedChannelInfo struct {
	Channel
	ArchivedAt     *time.Time `json:"archived_at"`
	MembersCount   int        `json:"members_count"`
	LastActivityAt *time.Time `json:"last_activity_at"`
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
