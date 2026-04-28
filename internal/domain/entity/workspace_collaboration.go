package entity

import (
	"time"

	"github.com/google/uuid"
)

type WorkspaceConnectionStatus string

const (
	WorkspaceConnectionPending WorkspaceConnectionStatus = "pending"
	WorkspaceConnectionActive  WorkspaceConnectionStatus = "active"
	WorkspaceConnectionPaused  WorkspaceConnectionStatus = "paused"
	WorkspaceConnectionRevoked WorkspaceConnectionStatus = "revoked"
)

type WorkspaceDirectoryVisibility string

const (
	DirectoryVisibilityNone      WorkspaceDirectoryVisibility = "none"
	DirectoryVisibilityBasic     WorkspaceDirectoryVisibility = "basic"
	DirectoryVisibilityDirectory WorkspaceDirectoryVisibility = "directory"
)

type WorkspaceContactPolicy string

const (
	ContactPolicyNone        WorkspaceContactPolicy = "none"
	ContactPolicyRoleBased   WorkspaceContactPolicy = "role_based"
	ContactPolicyMutualAllow WorkspaceContactPolicy = "mutual_allow"
	ContactPolicyOpen        WorkspaceContactPolicy = "open"
)

type WorkspaceConnectionPolicy struct {
	DirectoryVisibility WorkspaceDirectoryVisibility `json:"directory_visibility"`
	ContactPolicy       WorkspaceContactPolicy       `json:"contact_policy"`
	AllowedSourceRoles  []WorkspaceRole              `json:"allowed_source_roles,omitempty"`
	AllowedTargetRoles  []WorkspaceRole              `json:"allowed_target_roles,omitempty"`
	SharedChannels      bool                         `json:"shared_channels"`
	SharedCalls         bool                         `json:"shared_calls"`
	ExpiresAt           *time.Time                   `json:"expires_at,omitempty"`
}

type WorkspaceConnection struct {
	ID                uuid.UUID                 `json:"id"`
	SourceWorkspaceID uuid.UUID                 `json:"source_workspace_id"`
	TargetWorkspaceID uuid.UUID                 `json:"target_workspace_id"`
	Status            WorkspaceConnectionStatus `json:"status"`
	Policy            WorkspaceConnectionPolicy `json:"policy"`
	CreatedBy         uuid.UUID                 `json:"created_by"`
	ApprovedBy        *uuid.UUID                `json:"approved_by,omitempty"`
	CreatedAt         time.Time                 `json:"created_at"`
	UpdatedAt         time.Time                 `json:"updated_at"`
}

func (p WorkspaceConnectionPolicy) CanViewDirectory() bool {
	return p.DirectoryVisibility == DirectoryVisibilityBasic || p.DirectoryVisibility == DirectoryVisibilityDirectory
}

func (p WorkspaceConnectionPolicy) CanContact(sourceRole, targetRole WorkspaceRole) bool {
	switch p.ContactPolicy {
	case ContactPolicyOpen:
		return true
	case ContactPolicyRoleBased:
		return roleAllowed(sourceRole, p.AllowedSourceRoles) && roleAllowed(targetRole, p.AllowedTargetRoles)
	case ContactPolicyMutualAllow, ContactPolicyNone, "":
		return false
	default:
		return false
	}
}

func roleAllowed(role WorkspaceRole, allowed []WorkspaceRole) bool {
	if len(allowed) == 0 {
		return false
	}
	for _, candidate := range allowed {
		if role == candidate {
			return true
		}
	}
	return false
}
