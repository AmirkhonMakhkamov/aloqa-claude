package entity

import (
	"time"

	"github.com/google/uuid"
)

type WorkspaceRoleDefinition struct {
	ID          uuid.UUID     `json:"id"`
	WorkspaceID uuid.UUID     `json:"workspace_id"`
	Name        string        `json:"name"`
	BaseRole    WorkspaceRole `json:"base_role"`
	Permissions []string      `json:"permissions"`
	System      bool          `json:"system"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

type WorkspaceRoleAssignment struct {
	ID          uuid.UUID  `json:"id"`
	WorkspaceID uuid.UUID  `json:"workspace_id"`
	UserID      uuid.UUID  `json:"user_id"`
	RoleID      uuid.UUID  `json:"role_id"`
	AssignedBy  *uuid.UUID `json:"assigned_by,omitempty"`
	AssignedAt  time.Time  `json:"assigned_at"`
}
