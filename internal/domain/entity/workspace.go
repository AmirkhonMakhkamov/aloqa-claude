package entity

import (
	"time"

	"github.com/google/uuid"
)

type WorkspaceRole string

const (
	WorkspaceRoleOwner  WorkspaceRole = "owner"
	WorkspaceRoleAdmin  WorkspaceRole = "admin"
	WorkspaceRoleMember WorkspaceRole = "member"
	WorkspaceRoleGuest  WorkspaceRole = "guest"
)

type WorkspaceKind string

const (
	WorkspaceKindPersonal     WorkspaceKind = "personal"
	WorkspaceKindOrganization WorkspaceKind = "organization"
)

type Workspace struct {
	ID        uuid.UUID     `json:"id"`
	Name      string        `json:"name"`
	Slug      string        `json:"slug"`
	Kind      WorkspaceKind `json:"kind,omitempty"`
	AvatarURL string        `json:"avatar_url,omitempty"`
	CreatedBy uuid.UUID     `json:"created_by"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

type WorkspaceMember struct {
	ID          uuid.UUID     `json:"id"`
	WorkspaceID uuid.UUID     `json:"workspace_id"`
	UserID      uuid.UUID     `json:"user_id"`
	Role        WorkspaceRole `json:"role"`
	JoinedAt    time.Time     `json:"joined_at"`
	// User is optionally populated by ListMembers so the admin UI and members
	// store can render display name / email / avatar without a separate
	// per-user lookup. Kept as a pointer so endpoints that don't join don't
	// force callers to deal with a zero-value User.
	User *User `json:"user,omitempty"`
}
