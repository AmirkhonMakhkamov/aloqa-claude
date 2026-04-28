package rbac

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
)

type Checker interface {
	Require(ctx context.Context, workspaceID, userID uuid.UUID, permission Permission) error
	HasPermission(ctx context.Context, workspaceID, userID uuid.UUID, permission Permission) (bool, error)
	Resolve(ctx context.Context, workspaceID, userID uuid.UUID) (RoleDefinition, error)
}

type PermissionChecker struct {
	members repository.WorkspaceRepository
	roles   repository.WorkspaceRoleRepository
}

func NewChecker(members repository.WorkspaceRepository, roles repository.WorkspaceRoleRepository) *PermissionChecker {
	return &PermissionChecker{
		members: members,
		roles:   roles,
	}
}

func (c *PermissionChecker) Require(ctx context.Context, workspaceID, userID uuid.UUID, permission Permission) error {
	allowed, err := c.HasPermission(ctx, workspaceID, userID, permission)
	if err != nil {
		return err
	}
	if !allowed {
		return cerrors.Forbidden("workspace role does not have required permission")
	}
	return nil
}

func (c *PermissionChecker) HasPermission(ctx context.Context, workspaceID, userID uuid.UUID, permission Permission) (bool, error) {
	role, err := c.Resolve(ctx, workspaceID, userID)
	if err != nil {
		return false, err
	}
	return role.Has(permission), nil
}

func (c *PermissionChecker) Resolve(ctx context.Context, workspaceID, userID uuid.UUID) (RoleDefinition, error) {
	member, err := c.members.GetMember(ctx, workspaceID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return RoleDefinition{}, cerrors.Forbidden("user is not a workspace member")
		}
		return RoleDefinition{}, cerrors.Internal("failed to verify workspace membership", err)
	}

	effective := DefaultWorkspaceRole(member.Role)
	if c.roles == nil {
		return effective, nil
	}

	definitions, err := c.roles.ListAssignedDefinitions(ctx, workspaceID, userID)
	if err != nil {
		return RoleDefinition{}, cerrors.Internal("failed to load assigned workspace roles", err)
	}
	for _, definition := range definitions {
		if definition.BaseRole != member.Role {
			continue
		}
		overlay, err := RoleDefinitionFromEntity(definition)
		if err != nil {
			return RoleDefinition{}, cerrors.Internal("failed to resolve workspace permissions", fmt.Errorf("role %s: %w", definition.ID, err))
		}
		effective = Merge(effective, overlay)
	}

	return effective, nil
}

var _ Checker = (*PermissionChecker)(nil)

func EffectiveBaseRole(member *entity.WorkspaceMember) entity.WorkspaceRole {
	if member == nil {
		return ""
	}
	return member.Role
}
