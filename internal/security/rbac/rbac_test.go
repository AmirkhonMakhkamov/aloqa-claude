package rbac

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
)

func TestDefaultWorkspacePermissions(t *testing.T) {
	if !Has(entity.WorkspaceRoleOwner, PermissionWorkspaceManage) {
		t.Fatalf("owner should manage workspace")
	}
	if !Has(entity.WorkspaceRoleAdmin, PermissionCollaborationManage) {
		t.Fatalf("admin should manage collaboration")
	}
	if Has(entity.WorkspaceRoleMember, PermissionMemberManage) {
		t.Fatalf("member should not manage members by default")
	}
	if Has(entity.WorkspaceRoleGuest, PermissionDirectoryReadShared) {
		t.Fatalf("guest should not read shared directories by default")
	}
}

func TestCustomRoleDefinition(t *testing.T) {
	role := RoleDefinition{
		Name:     "subsidiary-recruiter",
		BaseRole: entity.WorkspaceRoleMember,
		Permissions: map[Permission]bool{
			PermissionDirectoryReadShared: true,
			PermissionDirectMessageShared: false,
		},
	}

	if !role.Has(PermissionDirectoryReadShared) {
		t.Fatalf("custom role should read shared directory")
	}
	if role.Has(PermissionDirectMessageShared) {
		t.Fatalf("custom role should not start shared DMs")
	}
}

func TestPermissionCheckerMergesAssignedRolePermissions(t *testing.T) {
	workspaceID := uuid.New()
	userID := uuid.New()
	checker := NewChecker(
		&fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
			{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
		}},
		&fakeWorkspaceRoleRepo{assigned: map[[2]uuid.UUID][]entity.WorkspaceRoleDefinition{
			{workspaceID, userID}: {{
				ID:          uuid.New(),
				WorkspaceID: workspaceID,
				Name:        "collaboration-manager",
				BaseRole:    entity.WorkspaceRoleMember,
				Permissions: []string{string(PermissionCollaborationManage)},
			}},
		}},
	)

	allowed, err := checker.HasPermission(context.Background(), workspaceID, userID, PermissionCollaborationManage)
	if err != nil {
		t.Fatalf("HasPermission returned error: %v", err)
	}
	if !allowed {
		t.Fatalf("expected assigned custom role to grant collaboration.manage")
	}
}

type fakeWorkspaceRepo struct {
	members map[[2]uuid.UUID]*entity.WorkspaceMember
}

func (r *fakeWorkspaceRepo) Create(context.Context, *entity.Workspace) error { return nil }
func (r *fakeWorkspaceRepo) GetByID(context.Context, uuid.UUID) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}
func (r *fakeWorkspaceRepo) GetBySlug(context.Context, string) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}
func (r *fakeWorkspaceRepo) ListByUser(context.Context, uuid.UUID) ([]entity.Workspace, error) {
	return nil, nil
}
func (r *fakeWorkspaceRepo) Update(context.Context, *entity.Workspace) error { return nil }
func (r *fakeWorkspaceRepo) AddMember(context.Context, *entity.WorkspaceMember) error {
	return nil
}
func (r *fakeWorkspaceRepo) GetMember(_ context.Context, workspaceID, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	if member := r.members[[2]uuid.UUID{workspaceID, userID}]; member != nil {
		return member, nil
	}
	return nil, cerrors.NotFound("workspace member not found")
}
func (r *fakeWorkspaceRepo) ListMembers(context.Context, uuid.UUID, pagination.Params) ([]entity.WorkspaceMember, error) {
	return nil, nil
}
func (r *fakeWorkspaceRepo) UpdateMemberRole(context.Context, uuid.UUID, uuid.UUID, entity.WorkspaceRole) error {
	return nil
}
func (r *fakeWorkspaceRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type fakeWorkspaceRoleRepo struct {
	assigned map[[2]uuid.UUID][]entity.WorkspaceRoleDefinition
}

func (r *fakeWorkspaceRoleRepo) CreateDefinition(context.Context, *entity.WorkspaceRoleDefinition) error {
	return nil
}
func (r *fakeWorkspaceRoleRepo) GetDefinition(context.Context, uuid.UUID, uuid.UUID) (*entity.WorkspaceRoleDefinition, error) {
	return nil, cerrors.NotFound("role definition not found")
}
func (r *fakeWorkspaceRoleRepo) ListDefinitions(context.Context, uuid.UUID) ([]entity.WorkspaceRoleDefinition, error) {
	return nil, nil
}
func (r *fakeWorkspaceRoleRepo) UpdateDefinition(context.Context, *entity.WorkspaceRoleDefinition) error {
	return nil
}
func (r *fakeWorkspaceRoleRepo) DeleteDefinition(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *fakeWorkspaceRoleRepo) AssignRole(context.Context, *entity.WorkspaceRoleAssignment) error {
	return nil
}
func (r *fakeWorkspaceRoleRepo) UnassignRole(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *fakeWorkspaceRoleRepo) ListAssignedDefinitions(_ context.Context, workspaceID, userID uuid.UUID) ([]entity.WorkspaceRoleDefinition, error) {
	return r.assigned[[2]uuid.UUID{workspaceID, userID}], nil
}
