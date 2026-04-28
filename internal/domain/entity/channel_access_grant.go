package entity

import (
	"time"

	"github.com/google/uuid"
)

type ChannelAccessGrantKind string

const (
	ChannelAccessGrantKindCollaborationDM ChannelAccessGrantKind = "collaboration_dm"
)

type ChannelAccessGrant struct {
	ID                uuid.UUID              `json:"id"`
	ChannelID         uuid.UUID              `json:"channel_id"`
	WorkspaceID       uuid.UUID              `json:"workspace_id"`
	UserID            uuid.UUID              `json:"user_id"`
	SourceUserID      uuid.UUID              `json:"source_user_id"`
	RemoteWorkspaceID uuid.UUID              `json:"remote_workspace_id"`
	Kind              ChannelAccessGrantKind `json:"kind"`
	AllowCalls        bool                   `json:"allow_calls"`
	CreatedAt         time.Time              `json:"created_at"`
}
