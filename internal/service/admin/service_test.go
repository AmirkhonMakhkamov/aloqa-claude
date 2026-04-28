package admin

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
	"aloqa/internal/security/rbac"
)

func TestInviteMemberAddsExistingUserToWorkspace(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	ownerID := uuid.New()
	targetID := uuid.New()

	users := &fakeUserRepo{
		byID: map[uuid.UUID]*entity.User{
			ownerID:  {ID: ownerID, Email: "owner@example.com", Status: entity.UserStatusActive},
			targetID: {ID: targetID, Email: "member@example.com", Status: entity.UserStatusActive},
		},
		byEmail: map[string]*entity.User{
			"owner@example.com":  {ID: ownerID, Email: "owner@example.com", Status: entity.UserStatusActive},
			"member@example.com": {ID: targetID, Email: "member@example.com", Status: entity.UserStatusActive},
		},
	}
	workspaces := &fakeWorkspaceRepo{
		workspaces: map[uuid.UUID]*entity.Workspace{
			workspaceID: {ID: workspaceID, Slug: "acme", CreatedBy: ownerID},
		},
		members: map[[2]uuid.UUID]*entity.WorkspaceMember{
			{workspaceID, ownerID}: {WorkspaceID: workspaceID, UserID: ownerID, Role: entity.WorkspaceRoleOwner, JoinedAt: time.Now().UTC()},
		},
	}
	audit := &fakeAuditRepo{}
	roles := &fakeWorkspaceRoleRepo{}
	svc := NewService(users, workspaces, roles, &fakeChannelRepo{}, &fakeRecordingRepo{}, audit, rbac.NewChecker(workspaces, roles), nil)

	member, err := svc.InviteMember(ctx, workspaceID, ownerID, InviteMemberInput{
		Email: "member@example.com",
		Role:  entity.WorkspaceRoleMember,
	})
	if err != nil {
		t.Fatalf("InviteMember returned error: %v", err)
	}
	if member.UserID != targetID {
		t.Fatalf("UserID = %s, want %s", member.UserID, targetID)
	}
	if stored := workspaces.members[[2]uuid.UUID{workspaceID, targetID}]; stored == nil {
		t.Fatalf("member was not added to workspace")
	}
	if len(audit.entries) != 1 || audit.entries[0].Action != entity.AuditActionMemberInvited {
		t.Fatalf("audit entries = %+v, want member.invited", audit.entries)
	}
}

func TestAdminCannotInviteAnotherAdmin(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	adminID := uuid.New()
	targetID := uuid.New()

	users := &fakeUserRepo{
		byID: map[uuid.UUID]*entity.User{
			adminID:  {ID: adminID, Email: "admin@example.com", Status: entity.UserStatusActive},
			targetID: {ID: targetID, Email: "other@example.com", Status: entity.UserStatusActive},
		},
		byEmail: map[string]*entity.User{
			"admin@example.com": {ID: adminID, Email: "admin@example.com", Status: entity.UserStatusActive},
			"other@example.com": {ID: targetID, Email: "other@example.com", Status: entity.UserStatusActive},
		},
	}
	workspaces := &fakeWorkspaceRepo{
		workspaces: map[uuid.UUID]*entity.Workspace{
			workspaceID: {ID: workspaceID, Slug: "acme", CreatedBy: adminID},
		},
		members: map[[2]uuid.UUID]*entity.WorkspaceMember{
			{workspaceID, adminID}: {WorkspaceID: workspaceID, UserID: adminID, Role: entity.WorkspaceRoleAdmin, JoinedAt: time.Now().UTC()},
		},
	}
	roles := &fakeWorkspaceRoleRepo{}
	svc := NewService(users, workspaces, roles, &fakeChannelRepo{}, &fakeRecordingRepo{}, &fakeAuditRepo{}, rbac.NewChecker(workspaces, roles), nil)

	if _, err := svc.InviteMember(ctx, workspaceID, adminID, InviteMemberInput{
		Email: "other@example.com",
		Role:  entity.WorkspaceRoleAdmin,
	}); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("InviteMember error = %v, want FORBIDDEN", err)
	}
}

func TestPersonalWorkspaceRejectsAdminOperations(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	ownerID := uuid.New()
	targetID := uuid.New()

	users := &fakeUserRepo{
		byID: map[uuid.UUID]*entity.User{
			ownerID:  {ID: ownerID, Email: "owner@example.com", Status: entity.UserStatusActive},
			targetID: {ID: targetID, Email: "target@example.com", Status: entity.UserStatusActive},
		},
		byEmail: map[string]*entity.User{
			"target@example.com": {ID: targetID, Email: "target@example.com", Status: entity.UserStatusActive},
		},
	}
	workspaces := &fakeWorkspaceRepo{
		workspaces: map[uuid.UUID]*entity.Workspace{
			workspaceID: {ID: workspaceID, Slug: personalWorkspaceSlug(ownerID), CreatedBy: ownerID},
		},
		members: map[[2]uuid.UUID]*entity.WorkspaceMember{
			{workspaceID, ownerID}:  {WorkspaceID: workspaceID, UserID: ownerID, Role: entity.WorkspaceRoleOwner, JoinedAt: time.Now().UTC()},
			{workspaceID, targetID}: {WorkspaceID: workspaceID, UserID: targetID, Role: entity.WorkspaceRoleMember, JoinedAt: time.Now().UTC()},
		},
	}
	roles := &fakeWorkspaceRoleRepo{}
	svc := NewService(users, workspaces, roles, &fakeChannelRepo{}, &fakeRecordingRepo{}, &fakeAuditRepo{}, rbac.NewChecker(workspaces, roles), nil)

	if _, err := svc.InviteMember(ctx, workspaceID, ownerID, InviteMemberInput{
		Email: "target@example.com",
	}); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("InviteMember error = %v, want FORBIDDEN for personal workspace", err)
	}
	if err := svc.SuspendUser(ctx, workspaceID, ownerID, targetID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("SuspendUser error = %v, want FORBIDDEN for personal workspace", err)
	}
}

func TestCustomRoleAllowsMemberInvite(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	ownerID := uuid.New()
	managerID := uuid.New()
	targetID := uuid.New()
	roleID := uuid.New()

	users := &fakeUserRepo{
		byID: map[uuid.UUID]*entity.User{
			ownerID:   {ID: ownerID, Email: "owner@example.com", Status: entity.UserStatusActive},
			managerID: {ID: managerID, Email: "manager@example.com", Status: entity.UserStatusActive},
			targetID:  {ID: targetID, Email: "target@example.com", Status: entity.UserStatusActive},
		},
		byEmail: map[string]*entity.User{
			"owner@example.com":   {ID: ownerID, Email: "owner@example.com", Status: entity.UserStatusActive},
			"manager@example.com": {ID: managerID, Email: "manager@example.com", Status: entity.UserStatusActive},
			"target@example.com":  {ID: targetID, Email: "target@example.com", Status: entity.UserStatusActive},
		},
	}
	workspaces := &fakeWorkspaceRepo{
		workspaces: map[uuid.UUID]*entity.Workspace{
			workspaceID: {ID: workspaceID, Slug: "acme", CreatedBy: ownerID},
		},
		members: map[[2]uuid.UUID]*entity.WorkspaceMember{
			{workspaceID, ownerID}:   {WorkspaceID: workspaceID, UserID: ownerID, Role: entity.WorkspaceRoleOwner, JoinedAt: time.Now().UTC()},
			{workspaceID, managerID}: {WorkspaceID: workspaceID, UserID: managerID, Role: entity.WorkspaceRoleMember, JoinedAt: time.Now().UTC()},
		},
	}
	roles := &fakeWorkspaceRoleRepo{
		definitions: map[uuid.UUID]*entity.WorkspaceRoleDefinition{
			roleID: {
				ID:          roleID,
				WorkspaceID: workspaceID,
				Name:        "member-invite-manager",
				BaseRole:    entity.WorkspaceRoleMember,
				Permissions: []string{string(rbac.PermissionMemberInvite)},
			},
		},
		assigned: map[[2]uuid.UUID][]entity.WorkspaceRoleDefinition{
			{workspaceID, managerID}: {{
				ID:          roleID,
				WorkspaceID: workspaceID,
				Name:        "member-invite-manager",
				BaseRole:    entity.WorkspaceRoleMember,
				Permissions: []string{string(rbac.PermissionMemberInvite)},
			}},
		},
	}
	svc := NewService(users, workspaces, roles, &fakeChannelRepo{}, &fakeRecordingRepo{}, &fakeAuditRepo{}, rbac.NewChecker(workspaces, roles), nil)

	member, err := svc.InviteMember(ctx, workspaceID, managerID, InviteMemberInput{
		Email: "target@example.com",
		Role:  entity.WorkspaceRoleMember,
	})
	if err != nil {
		t.Fatalf("InviteMember returned error: %v", err)
	}
	if member.UserID != targetID {
		t.Fatalf("invited user = %s, want %s", member.UserID, targetID)
	}
}

func hasCode(err error, code cerrors.Code) bool {
	appErr, ok := cerrors.AsAppError(err)
	return ok && appErr.Code == code
}

type fakeUserRepo struct {
	byID    map[uuid.UUID]*entity.User
	byEmail map[string]*entity.User
}

func (r *fakeUserRepo) Create(context.Context, *entity.User) error { return nil }
func (r *fakeUserRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.User, error) {
	if user := r.byID[id]; user != nil {
		return user, nil
	}
	return nil, cerrors.NotFound("user not found")
}
func (r *fakeUserRepo) GetByEmail(_ context.Context, email string) (*entity.User, error) {
	if user := r.byEmail[email]; user != nil {
		return user, nil
	}
	return nil, cerrors.NotFound("user not found")
}
func (r *fakeUserRepo) Update(context.Context, *entity.User) error { return nil }

type fakeWorkspaceRepo struct {
	workspaces map[uuid.UUID]*entity.Workspace
	members    map[[2]uuid.UUID]*entity.WorkspaceMember
}

func (r *fakeWorkspaceRepo) Create(context.Context, *entity.Workspace) error { return nil }
func (r *fakeWorkspaceRepo) GetByID(_ context.Context, workspaceID uuid.UUID) (*entity.Workspace, error) {
	if workspace := r.workspaces[workspaceID]; workspace != nil {
		return workspace, nil
	}
	return nil, cerrors.NotFound("workspace not found")
}
func (r *fakeWorkspaceRepo) GetBySlug(context.Context, string) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}
func (r *fakeWorkspaceRepo) ListByUser(context.Context, uuid.UUID) ([]entity.Workspace, error) {
	return nil, nil
}
func (r *fakeWorkspaceRepo) Update(context.Context, *entity.Workspace) error { return nil }
func (r *fakeWorkspaceRepo) AddMember(_ context.Context, member *entity.WorkspaceMember) error {
	if r.members == nil {
		r.members = map[[2]uuid.UUID]*entity.WorkspaceMember{}
	}
	r.members[[2]uuid.UUID{member.WorkspaceID, member.UserID}] = member
	return nil
}
func (r *fakeWorkspaceRepo) GetMember(_ context.Context, workspaceID, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	if member := r.members[[2]uuid.UUID{workspaceID, userID}]; member != nil {
		return member, nil
	}
	return nil, cerrors.NotFound("workspace member not found")
}
func (r *fakeWorkspaceRepo) ListMembers(_ context.Context, workspaceID uuid.UUID, _ pagination.Params) ([]entity.WorkspaceMember, error) {
	var result []entity.WorkspaceMember
	for key, member := range r.members {
		if key[0] == workspaceID {
			result = append(result, *member)
		}
	}
	return result, nil
}
func (r *fakeWorkspaceRepo) UpdateMemberRole(_ context.Context, workspaceID, userID uuid.UUID, role entity.WorkspaceRole) error {
	member := r.members[[2]uuid.UUID{workspaceID, userID}]
	if member == nil {
		return cerrors.NotFound("workspace member not found")
	}
	member.Role = role
	return nil
}
func (r *fakeWorkspaceRepo) RemoveMember(_ context.Context, workspaceID, userID uuid.UUID) error {
	delete(r.members, [2]uuid.UUID{workspaceID, userID})
	return nil
}

type fakeWorkspaceRoleRepo struct {
	definitions map[uuid.UUID]*entity.WorkspaceRoleDefinition
	assigned    map[[2]uuid.UUID][]entity.WorkspaceRoleDefinition
}

func (r *fakeWorkspaceRoleRepo) CreateDefinition(_ context.Context, role *entity.WorkspaceRoleDefinition) error {
	if r.definitions == nil {
		r.definitions = map[uuid.UUID]*entity.WorkspaceRoleDefinition{}
	}
	r.definitions[role.ID] = role
	return nil
}
func (r *fakeWorkspaceRoleRepo) GetDefinition(_ context.Context, workspaceID, roleID uuid.UUID) (*entity.WorkspaceRoleDefinition, error) {
	role := r.definitions[roleID]
	if role == nil || role.WorkspaceID != workspaceID {
		return nil, cerrors.NotFound("workspace role definition not found")
	}
	return role, nil
}
func (r *fakeWorkspaceRoleRepo) ListDefinitions(_ context.Context, workspaceID uuid.UUID) ([]entity.WorkspaceRoleDefinition, error) {
	var roles []entity.WorkspaceRoleDefinition
	for _, role := range r.definitions {
		if role.WorkspaceID == workspaceID {
			roles = append(roles, *role)
		}
	}
	return roles, nil
}
func (r *fakeWorkspaceRoleRepo) UpdateDefinition(_ context.Context, role *entity.WorkspaceRoleDefinition) error {
	if r.definitions == nil {
		r.definitions = map[uuid.UUID]*entity.WorkspaceRoleDefinition{}
	}
	r.definitions[role.ID] = role
	return nil
}
func (r *fakeWorkspaceRoleRepo) DeleteDefinition(_ context.Context, workspaceID, roleID uuid.UUID) error {
	role := r.definitions[roleID]
	if role == nil || role.WorkspaceID != workspaceID {
		return cerrors.NotFound("workspace role definition not found")
	}
	delete(r.definitions, roleID)
	return nil
}
func (r *fakeWorkspaceRoleRepo) AssignRole(_ context.Context, assignment *entity.WorkspaceRoleAssignment) error {
	if r.assigned == nil {
		r.assigned = map[[2]uuid.UUID][]entity.WorkspaceRoleDefinition{}
	}
	role := r.definitions[assignment.RoleID]
	if role == nil {
		return cerrors.NotFound("workspace role definition not found")
	}
	key := [2]uuid.UUID{assignment.WorkspaceID, assignment.UserID}
	for _, assigned := range r.assigned[key] {
		if assigned.ID == assignment.RoleID {
			return cerrors.AlreadyExists("workspace role is already assigned to member")
		}
	}
	r.assigned[key] = append(r.assigned[key], *role)
	return nil
}
func (r *fakeWorkspaceRoleRepo) UnassignRole(_ context.Context, workspaceID, userID, roleID uuid.UUID) error {
	key := [2]uuid.UUID{workspaceID, userID}
	current := r.assigned[key]
	filtered := current[:0]
	removed := false
	for _, role := range current {
		if role.ID == roleID {
			removed = true
			continue
		}
		filtered = append(filtered, role)
	}
	if !removed {
		return cerrors.NotFound("workspace role assignment not found")
	}
	r.assigned[key] = filtered
	return nil
}
func (r *fakeWorkspaceRoleRepo) ListAssignedDefinitions(_ context.Context, workspaceID, userID uuid.UUID) ([]entity.WorkspaceRoleDefinition, error) {
	return append([]entity.WorkspaceRoleDefinition(nil), r.assigned[[2]uuid.UUID{workspaceID, userID}]...), nil
}

type fakeChannelRepo struct{}

func (r *fakeChannelRepo) Create(context.Context, *entity.Channel) error { return nil }
func (r *fakeChannelRepo) GetByID(context.Context, uuid.UUID) (*entity.Channel, error) {
	return nil, cerrors.NotFound("channel not found")
}
func (r *fakeChannelRepo) ListByWorkspace(context.Context, uuid.UUID, pagination.Params) ([]entity.Channel, error) {
	return nil, nil
}
func (r *fakeChannelRepo) ListByUser(context.Context, uuid.UUID, uuid.UUID) ([]entity.Channel, error) {
	return nil, nil
}
func (r *fakeChannelRepo) Update(context.Context, *entity.Channel) error          { return nil }
func (r *fakeChannelRepo) Archive(context.Context, uuid.UUID) error               { return nil }
func (r *fakeChannelRepo) AddMember(context.Context, *entity.ChannelMember) error { return nil }
func (r *fakeChannelRepo) GetMember(context.Context, uuid.UUID, uuid.UUID) (*entity.ChannelMember, error) {
	return nil, cerrors.NotFound("channel member not found")
}
func (r *fakeChannelRepo) ListMembers(context.Context, uuid.UUID) ([]entity.ChannelMember, error) {
	return nil, nil
}
func (r *fakeChannelRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error   { return nil }
func (r *fakeChannelRepo) UpdateLastRead(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (r *fakeChannelRepo) GetDMChannel(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*entity.Channel, error) {
	return nil, cerrors.NotFound("dm channel not found")
}

type fakeRecordingRepo struct{}

func (r *fakeRecordingRepo) Create(context.Context, *entity.Recording) error { return nil }
func (r *fakeRecordingRepo) GetByID(context.Context, uuid.UUID) (*entity.Recording, error) {
	return nil, cerrors.NotFound("recording not found")
}
func (r *fakeRecordingRepo) ListByCall(context.Context, uuid.UUID) ([]entity.Recording, error) {
	return nil, nil
}
func (r *fakeRecordingRepo) ListByWorkspace(context.Context, uuid.UUID, pagination.Params) ([]entity.Recording, error) {
	return nil, nil
}
func (r *fakeRecordingRepo) ListByStatus(context.Context, entity.RecordingStatus, pagination.Params) ([]entity.Recording, error) {
	return nil, nil
}
func (r *fakeRecordingRepo) ListProcessable(context.Context, time.Time, pagination.Params) ([]entity.Recording, error) {
	return nil, nil
}
func (r *fakeRecordingRepo) ListExpired(context.Context, time.Time, pagination.Params) ([]entity.Recording, error) {
	return nil, nil
}
func (r *fakeRecordingRepo) UpdateStatus(context.Context, uuid.UUID, entity.RecordingStatus) error {
	return nil
}
func (r *fakeRecordingRepo) SetReady(context.Context, *entity.Recording) error {
	return nil
}
func (r *fakeRecordingRepo) MarkProcessingAttempt(context.Context, uuid.UUID, time.Time) (*entity.Recording, error) {
	return nil, cerrors.NotFound("recording not found")
}
func (r *fakeRecordingRepo) MarkFailed(context.Context, uuid.UUID, string, *time.Time) error {
	return nil
}
func (r *fakeRecordingRepo) SetLegalHold(context.Context, uuid.UUID, bool) error { return nil }
func (r *fakeRecordingRepo) Stop(context.Context, uuid.UUID) (*entity.Recording, error) {
	return nil, cerrors.NotFound("recording not found")
}
func (r *fakeRecordingRepo) Delete(context.Context, uuid.UUID) error { return nil }
func (r *fakeRecordingRepo) ReplaceArtifacts(context.Context, uuid.UUID, []entity.RecordingArtifact) error {
	return nil
}
func (r *fakeRecordingRepo) ListArtifacts(context.Context, uuid.UUID) ([]entity.RecordingArtifact, error) {
	return nil, nil
}
func (r *fakeRecordingRepo) GetArtifact(context.Context, uuid.UUID, uuid.UUID) (*entity.RecordingArtifact, error) {
	return nil, cerrors.NotFound("recording artifact not found")
}
func (r *fakeRecordingRepo) WorkspaceStorageUsage(context.Context, uuid.UUID) (int64, error) {
	return 0, nil
}

type fakeAuditRepo struct {
	entries []*entity.AuditEntry
}

func (r *fakeAuditRepo) Create(_ context.Context, entry *entity.AuditEntry) error {
	r.entries = append(r.entries, entry)
	return nil
}
func (r *fakeAuditRepo) List(context.Context, uuid.UUID, pagination.Params) ([]entity.AuditEntry, int, error) {
	return nil, 0, nil
}
func (r *fakeAuditRepo) ListByActor(context.Context, uuid.UUID, uuid.UUID, pagination.Params) ([]entity.AuditEntry, error) {
	return nil, nil
}
func (r *fakeAuditRepo) ListByAction(context.Context, uuid.UUID, entity.AuditAction, pagination.Params) ([]entity.AuditEntry, error) {
	return nil, nil
}
