package collaboration

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
	"aloqa/internal/security/rbac"
)

func TestCanContactRequiresActivePolicyAndRoles(t *testing.T) {
	ctx := context.Background()
	sourceWorkspaceID := uuid.New()
	targetWorkspaceID := uuid.New()
	adminID := uuid.New()
	ceoID := uuid.New()
	internID := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{sourceWorkspaceID, adminID}:  {WorkspaceID: sourceWorkspaceID, UserID: adminID, Role: entity.WorkspaceRoleAdmin},
		{sourceWorkspaceID, internID}: {WorkspaceID: sourceWorkspaceID, UserID: internID, Role: entity.WorkspaceRoleGuest},
		{targetWorkspaceID, ceoID}:    {WorkspaceID: targetWorkspaceID, UserID: ceoID, Role: entity.WorkspaceRoleOwner},
	}}
	connections := &fakeCollaborationRepo{connection: &entity.WorkspaceConnection{
		ID:                uuid.New(),
		SourceWorkspaceID: sourceWorkspaceID,
		TargetWorkspaceID: targetWorkspaceID,
		Status:            entity.WorkspaceConnectionActive,
		Policy: entity.WorkspaceConnectionPolicy{
			DirectoryVisibility: entity.DirectoryVisibilityDirectory,
			ContactPolicy:       entity.ContactPolicyRoleBased,
			AllowedSourceRoles:  []entity.WorkspaceRole{entity.WorkspaceRoleAdmin, entity.WorkspaceRoleOwner},
			AllowedTargetRoles:  []entity.WorkspaceRole{entity.WorkspaceRoleAdmin, entity.WorkspaceRoleOwner},
		},
	}}
	roles := &fakeWorkspaceRoleRepo{}
	svc := NewService(workspaces, connections, rbac.NewChecker(workspaces, roles))

	if err := svc.CanContact(ctx, sourceWorkspaceID, targetWorkspaceID, adminID, ceoID); err != nil {
		t.Fatalf("CanContact admin->ceo returned error: %v", err)
	}
	if err := svc.CanContact(ctx, sourceWorkspaceID, targetWorkspaceID, internID, ceoID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("CanContact intern->ceo error = %v, want FORBIDDEN", err)
	}
}

func TestApproveConnectionRequiresTargetWorkspacePermission(t *testing.T) {
	ctx := context.Background()
	sourceWorkspaceID := uuid.New()
	targetWorkspaceID := uuid.New()
	memberID := uuid.New()
	ownerID := uuid.New()
	connectionID := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{targetWorkspaceID, memberID}: {WorkspaceID: targetWorkspaceID, UserID: memberID, Role: entity.WorkspaceRoleMember},
		{targetWorkspaceID, ownerID}:  {WorkspaceID: targetWorkspaceID, UserID: ownerID, Role: entity.WorkspaceRoleOwner},
	}}
	connections := &fakeCollaborationRepo{connection: &entity.WorkspaceConnection{
		ID:                connectionID,
		SourceWorkspaceID: sourceWorkspaceID,
		TargetWorkspaceID: targetWorkspaceID,
		Status:            entity.WorkspaceConnectionPending,
	}}
	roles := &fakeWorkspaceRoleRepo{}
	svc := NewService(workspaces, connections, rbac.NewChecker(workspaces, roles))

	if err := svc.ApproveConnection(ctx, sourceWorkspaceID, targetWorkspaceID, memberID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("ApproveConnection member error = %v, want FORBIDDEN", err)
	}
	if err := svc.ApproveConnection(ctx, sourceWorkspaceID, targetWorkspaceID, ownerID); err != nil {
		t.Fatalf("ApproveConnection owner returned error: %v", err)
	}
	if connections.connection.Status != entity.WorkspaceConnectionActive {
		t.Fatalf("connection status = %q, want active", connections.connection.Status)
	}
}

func TestCustomRoleGrantsCollaborationPermission(t *testing.T) {
	ctx := context.Background()
	sourceWorkspaceID := uuid.New()
	targetWorkspaceID := uuid.New()
	memberID := uuid.New()
	roleID := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{sourceWorkspaceID, memberID}: {WorkspaceID: sourceWorkspaceID, UserID: memberID, Role: entity.WorkspaceRoleMember},
	}}
	connections := &fakeCollaborationRepo{}
	roles := &fakeWorkspaceRoleRepo{assigned: map[[2]uuid.UUID][]entity.WorkspaceRoleDefinition{
		{sourceWorkspaceID, memberID}: {{
			ID:          roleID,
			WorkspaceID: sourceWorkspaceID,
			Name:        "subsidiary-connector",
			BaseRole:    entity.WorkspaceRoleMember,
			Permissions: []string{string(rbac.PermissionCollaborationManage)},
		}},
	}}
	svc := NewService(workspaces, connections, rbac.NewChecker(workspaces, roles))

	connection, err := svc.RequestConnection(ctx, sourceWorkspaceID, targetWorkspaceID, memberID, entity.WorkspaceConnectionPolicy{
		ContactPolicy: entity.ContactPolicyOpen,
	})
	if err != nil {
		t.Fatalf("RequestConnection returned error: %v", err)
	}
	if connection == nil || connections.connection == nil {
		t.Fatalf("expected connection to be created")
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
func (r *fakeWorkspaceRepo) Update(context.Context, *entity.Workspace) error          { return nil }
func (r *fakeWorkspaceRepo) AddMember(context.Context, *entity.WorkspaceMember) error { return nil }
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

type fakeCollaborationRepo struct {
	connection *entity.WorkspaceConnection
}

func (r *fakeCollaborationRepo) CreateConnection(_ context.Context, connection *entity.WorkspaceConnection) error {
	r.connection = connection
	return nil
}
func (r *fakeCollaborationRepo) GetConnection(_ context.Context, sourceWorkspaceID, targetWorkspaceID uuid.UUID) (*entity.WorkspaceConnection, error) {
	if r.connection != nil && r.connection.SourceWorkspaceID == sourceWorkspaceID && r.connection.TargetWorkspaceID == targetWorkspaceID {
		return r.connection, nil
	}
	return nil, cerrors.NotFound("workspace connection not found")
}
func (r *fakeCollaborationRepo) ListConnections(context.Context, uuid.UUID, pagination.Params) ([]entity.WorkspaceConnection, error) {
	if r.connection == nil {
		return nil, nil
	}
	return []entity.WorkspaceConnection{*r.connection}, nil
}
func (r *fakeCollaborationRepo) UpdateConnectionPolicy(_ context.Context, id uuid.UUID, policy entity.WorkspaceConnectionPolicy) error {
	if r.connection == nil || r.connection.ID != id {
		return cerrors.NotFound("workspace connection not found")
	}
	r.connection.Policy = policy
	return nil
}
func (r *fakeCollaborationRepo) UpdateConnectionStatus(_ context.Context, id uuid.UUID, status entity.WorkspaceConnectionStatus, approvedBy *uuid.UUID) error {
	if r.connection == nil || r.connection.ID != id {
		return cerrors.NotFound("workspace connection not found")
	}
	r.connection.Status = status
	r.connection.ApprovedBy = approvedBy
	return nil
}

type fakeWorkspaceRoleRepo struct {
	assigned map[[2]uuid.UUID][]entity.WorkspaceRoleDefinition
}

func (r *fakeWorkspaceRoleRepo) CreateDefinition(context.Context, *entity.WorkspaceRoleDefinition) error {
	return nil
}
func (r *fakeWorkspaceRoleRepo) GetDefinition(context.Context, uuid.UUID, uuid.UUID) (*entity.WorkspaceRoleDefinition, error) {
	return nil, cerrors.NotFound("workspace role definition not found")
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
	return append([]entity.WorkspaceRoleDefinition(nil), r.assigned[[2]uuid.UUID{workspaceID, userID}]...), nil
}

func hasCode(err error, code cerrors.Code) bool {
	appErr, ok := cerrors.AsAppError(err)
	return ok && appErr.Code == code
}
