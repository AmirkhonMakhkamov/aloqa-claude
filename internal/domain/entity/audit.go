package entity

import (
	"time"

	"github.com/google/uuid"
)

// AuditAction classifies what happened.
type AuditAction string

const (
	AuditActionUserCreated         AuditAction = "user.created"
	AuditActionUserUpdated         AuditAction = "user.updated"
	AuditActionUserSuspended       AuditAction = "user.suspended"
	AuditActionUserReactivated     AuditAction = "user.reactivated"
	AuditActionUserDeleted         AuditAction = "user.deleted"
	AuditActionLoginSuccess        AuditAction = "auth.login_success"
	AuditActionLoginFailed         AuditAction = "auth.login_failed"
	AuditActionLogout              AuditAction = "auth.logout"
	AuditActionRoleChanged         AuditAction = "member.role_changed"
	AuditActionMemberRemoved       AuditAction = "member.removed"
	AuditActionMemberInvited       AuditAction = "member.invited"
	AuditActionChannelCreated      AuditAction = "channel.created"
	AuditActionChannelArchived     AuditAction = "channel.archived"
	AuditActionChannelDeleted      AuditAction = "channel.deleted"
	AuditActionRecordingStarted    AuditAction = "recording.started"
	AuditActionRecordingStopped    AuditAction = "recording.stopped"
	AuditActionRecordingViewed     AuditAction = "recording.viewed"
	AuditActionRecordingDownloaded AuditAction = "recording.downloaded"
	AuditActionLegalHoldSet        AuditAction = "recording.legal_hold"
	AuditActionWorkspaceUpdated    AuditAction = "workspace.updated"
	AuditActionSettingsChanged     AuditAction = "settings.changed"
	AuditActionSearchPerformed     AuditAction = "search.performed"
)

// AuditEntry is an immutable record of a security-relevant event.
type AuditEntry struct {
	ID          uuid.UUID      `json:"id"`
	WorkspaceID uuid.UUID      `json:"workspace_id"`
	ActorID     uuid.UUID      `json:"actor_id"`
	Action      AuditAction    `json:"action"`
	TargetType  string         `json:"target_type,omitempty"` // "user", "channel", "recording", etc.
	TargetID    string         `json:"target_id,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	IPAddress   string         `json:"ip_address,omitempty"`
	UserAgent   string         `json:"user_agent,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}
