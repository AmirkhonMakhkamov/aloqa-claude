package auth

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
)

func TestUpdateProfileAllowsPlatformUserWithoutWorkspace(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()
	users := &fakeUserRepo{
		users: map[uuid.UUID]*entity.User{
			userID: {
				ID:          userID,
				Email:       "user@example.com",
				DisplayName: "Before",
				Status:      entity.UserStatusActive,
				Locale:      "en",
			},
		},
	}
	workspaces := &fakeWorkspaceRepo{
		workspaces: map[uuid.UUID]*entity.Workspace{},
		bySlug:     map[string]*entity.Workspace{},
		members:    map[[2]uuid.UUID]*entity.WorkspaceMember{},
	}
	svc := NewService(users, workspaces, nil, []byte("01234567890123456789012345678901"), time.Minute, time.Hour, nil)

	name := "After"
	locale := "uz"
	user, err := svc.UpdateProfile(ctx, userID, UpdateProfileInput{
		DisplayName: &name,
		Locale:      &locale,
	})
	if err != nil {
		t.Fatalf("UpdateProfile returned error: %v", err)
	}
	if user.DisplayName != name {
		t.Fatalf("DisplayName = %q, want %q", user.DisplayName, name)
	}
	if user.Locale != locale {
		t.Fatalf("Locale = %q, want %q", user.Locale, locale)
	}
}

func TestCreateWorkspaceCreatesOwnerMembership(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()
	users := &fakeUserRepo{
		users: map[uuid.UUID]*entity.User{
			userID: {
				ID:          userID,
				Email:       "owner@example.com",
				DisplayName: "Owner",
				Status:      entity.UserStatusActive,
				Locale:      "en",
			},
		},
	}
	workspaces := &fakeWorkspaceRepo{
		workspaces: map[uuid.UUID]*entity.Workspace{},
		bySlug:     map[string]*entity.Workspace{},
		members:    map[[2]uuid.UUID]*entity.WorkspaceMember{},
	}
	svc := NewService(users, workspaces, nil, []byte("01234567890123456789012345678901"), time.Minute, time.Hour, nil)

	workspace, err := svc.CreateWorkspace(ctx, userID, CreateWorkspaceInput{Name: "Acme HQ"})
	if err != nil {
		t.Fatalf("CreateWorkspace returned error: %v", err)
	}
	if workspace.CreatedBy != userID {
		t.Fatalf("CreatedBy = %s, want %s", workspace.CreatedBy, userID)
	}
	if workspace.Slug != "acme-hq" {
		t.Fatalf("Slug = %q, want acme-hq", workspace.Slug)
	}
	if workspace.Kind != entity.WorkspaceKindOrganization {
		t.Fatalf("Kind = %q, want organization", workspace.Kind)
	}
	member := workspaces.members[[2]uuid.UUID{workspace.ID, userID}]
	if member == nil {
		t.Fatalf("workspace owner membership was not created")
	}
	if member.Role != entity.WorkspaceRoleOwner {
		t.Fatalf("member role = %q, want owner", member.Role)
	}
}

func TestCreateWorkspaceGeneratesUniqueSlug(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()
	existingWorkspaceID := uuid.New()
	users := &fakeUserRepo{
		users: map[uuid.UUID]*entity.User{
			userID: {
				ID:          userID,
				Email:       "owner@example.com",
				DisplayName: "Owner",
				Status:      entity.UserStatusActive,
				Locale:      "en",
			},
		},
	}
	workspaces := &fakeWorkspaceRepo{
		workspaces: map[uuid.UUID]*entity.Workspace{
			existingWorkspaceID: {ID: existingWorkspaceID, Name: "Acme HQ", Slug: "acme-hq"},
		},
		bySlug: map[string]*entity.Workspace{
			"acme-hq": {ID: existingWorkspaceID, Name: "Acme HQ", Slug: "acme-hq"},
		},
		members: map[[2]uuid.UUID]*entity.WorkspaceMember{},
	}
	svc := NewService(users, workspaces, nil, []byte("01234567890123456789012345678901"), time.Minute, time.Hour, nil)

	workspace, err := svc.CreateWorkspace(ctx, userID, CreateWorkspaceInput{Name: "Acme HQ"})
	if err != nil {
		t.Fatalf("CreateWorkspace returned error: %v", err)
	}
	if workspace.Slug != "acme-hq-2" {
		t.Fatalf("Slug = %q, want acme-hq-2", workspace.Slug)
	}
}

func TestGetWorkspaceRequiresMembership(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	workspaces := &fakeWorkspaceRepo{
		workspaces: map[uuid.UUID]*entity.Workspace{
			workspaceID: {ID: workspaceID, Name: "Acme HQ", Slug: "acme-hq"},
		},
		bySlug:  map[string]*entity.Workspace{},
		members: map[[2]uuid.UUID]*entity.WorkspaceMember{},
	}
	svc := NewService(&fakeUserRepo{}, workspaces, nil, []byte("01234567890123456789012345678901"), time.Minute, time.Hour, nil)

	if _, err := svc.GetWorkspace(ctx, workspaceID, userID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("GetWorkspace error = %v, want FORBIDDEN", err)
	}
}

func TestGetOrCreatePersonalWorkspaceCreatesOwnerPersonalScope(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()
	users := &fakeUserRepo{
		users: map[uuid.UUID]*entity.User{
			userID: {
				ID:          userID,
				Email:       "user@example.com",
				DisplayName: "Solo User",
				Status:      entity.UserStatusActive,
				Locale:      "en",
			},
		},
	}
	workspaces := &fakeWorkspaceRepo{
		workspaces: map[uuid.UUID]*entity.Workspace{},
		bySlug:     map[string]*entity.Workspace{},
		members:    map[[2]uuid.UUID]*entity.WorkspaceMember{},
	}
	svc := NewService(users, workspaces, nil, []byte("01234567890123456789012345678901"), time.Minute, time.Hour, nil)

	workspace, err := svc.GetOrCreatePersonalWorkspace(ctx, userID)
	if err != nil {
		t.Fatalf("GetOrCreatePersonalWorkspace returned error: %v", err)
	}
	if workspace.Kind != entity.WorkspaceKindPersonal {
		t.Fatalf("Kind = %q, want personal", workspace.Kind)
	}
	if workspace.Slug != personalWorkspaceSlug(userID) {
		t.Fatalf("Slug = %q, want %q", workspace.Slug, personalWorkspaceSlug(userID))
	}
	if member := workspaces.members[[2]uuid.UUID{workspace.ID, userID}]; member == nil || member.Role != entity.WorkspaceRoleOwner {
		t.Fatalf("personal workspace owner membership missing or incorrect")
	}
}

func TestListWorkspacesEnsuresPersonalWorkspace(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()
	orgWorkspaceID := uuid.New()
	users := &fakeUserRepo{
		users: map[uuid.UUID]*entity.User{
			userID: {
				ID:          userID,
				Email:       "user@example.com",
				DisplayName: "Solo User",
				Status:      entity.UserStatusActive,
				Locale:      "en",
			},
		},
	}
	workspaces := &fakeWorkspaceRepo{
		workspaces: map[uuid.UUID]*entity.Workspace{
			orgWorkspaceID: {ID: orgWorkspaceID, Name: "Acme HQ", Slug: "acme-hq", CreatedBy: userID},
		},
		bySlug: map[string]*entity.Workspace{
			"acme-hq": {ID: orgWorkspaceID, Name: "Acme HQ", Slug: "acme-hq", CreatedBy: userID},
		},
		members: map[[2]uuid.UUID]*entity.WorkspaceMember{
			{orgWorkspaceID, userID}: {WorkspaceID: orgWorkspaceID, UserID: userID, Role: entity.WorkspaceRoleOwner},
		},
	}
	svc := NewService(users, workspaces, nil, []byte("01234567890123456789012345678901"), time.Minute, time.Hour, nil)

	list, err := svc.ListWorkspaces(ctx, userID)
	if err != nil {
		t.Fatalf("ListWorkspaces returned error: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListWorkspaces length = %d, want 2", len(list))
	}

	personalFound := false
	orgFound := false
	for _, ws := range list {
		if ws.Kind == entity.WorkspaceKindPersonal {
			personalFound = true
		}
		if ws.Slug == "acme-hq" && ws.Kind == entity.WorkspaceKindOrganization {
			orgFound = true
		}
	}
	if !personalFound {
		t.Fatalf("personal workspace missing from list")
	}
	if !orgFound {
		t.Fatalf("organization workspace missing or untyped")
	}
}

func TestRegisterRunsNewUserSeeder(t *testing.T) {
	ctx := context.Background()
	users := &fakeUserRepo{
		users: map[uuid.UUID]*entity.User{},
	}
	workspaces := &fakeWorkspaceRepo{
		workspaces: map[uuid.UUID]*entity.Workspace{},
		bySlug:     map[string]*entity.Workspace{},
		members:    map[[2]uuid.UUID]*entity.WorkspaceMember{},
	}
	seeder := &fakeNewUserSeeder{}
	svc := NewService(users, workspaces, nil, []byte("01234567890123456789012345678901"), time.Minute, time.Hour, nil)
	svc.SetNewUserSeeder(seeder)

	user, err := svc.Register(ctx, "new@example.com", "Password1!", "New User")
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if !seeder.called {
		t.Fatalf("new user seeder was not called")
	}
	if seeder.userID != user.ID {
		t.Fatalf("seeded user ID = %s, want %s", seeder.userID, user.ID)
	}
}

func TestLoginMasksInactiveStatusUntilPasswordVerified(t *testing.T) {
	ctx := context.Background()
	env := newServiceSessionTestEnv(t)
	password := "Password1!"
	activeID := env.addCredentialedUser(t, "active@example.com", password, entity.UserStatusActive)
	deactivatedID := env.addCredentialedUser(t, "deactivated@example.com", password, entity.UserStatusDeactivated)
	suspendedID := env.addCredentialedUser(t, "suspended@example.com", password, entity.UserStatusSuspended)

	if _, err := env.svc.Login(ctx, "deactivated@example.com", "wrong", "ua", "127.0.0.1"); !hasCode(err, cerrors.CodeUnauthorized) {
		t.Fatalf("deactivated wrong-password Login error = %v, want UNAUTHORIZED", err)
	}
	if _, err := env.svc.Login(ctx, "suspended@example.com", "wrong", "ua", "127.0.0.1"); !hasCode(err, cerrors.CodeUnauthorized) {
		t.Fatalf("suspended wrong-password Login error = %v, want UNAUTHORIZED", err)
	}
	if _, err := env.svc.Login(ctx, "deactivated@example.com", password, "ua", "127.0.0.1"); !hasCode(err, cerrors.CodeAccountDeactivated) {
		t.Fatalf("deactivated Login error = %v, want ACCOUNT_DEACTIVATED", err)
	}
	if _, err := env.svc.Login(ctx, "suspended@example.com", password, "ua", "127.0.0.1"); !hasCode(err, cerrors.CodeAccountSuspended) {
		t.Fatalf("suspended Login error = %v, want ACCOUNT_SUSPENDED", err)
	}
	if result, err := env.svc.Login(ctx, "active@example.com", password, "ua", "127.0.0.1"); err != nil || result == nil {
		t.Fatalf("active Login result=%v error=%v, want success for %s", result, err, activeID)
	}
	if env.users.users[deactivatedID].Status != entity.UserStatusDeactivated {
		t.Fatalf("deactivated user status changed during Login")
	}
	if env.users.users[suspendedID].Status != entity.UserStatusSuspended {
		t.Fatalf("suspended user status changed during Login")
	}
}

func TestDeactivateRevokesSessionsAndSetsStatus(t *testing.T) {
	ctx := context.Background()
	env := newServiceSessionTestEnv(t)
	userID := env.addCredentialedUser(t, "user@example.com", "Password1!", entity.UserStatusActive)
	result, err := env.svc.CreateSessionForUser(ctx, userID, "ua", "127.0.0.1")
	if err != nil {
		t.Fatalf("CreateSessionForUser returned error: %v", err)
	}

	if err := env.svc.Deactivate(ctx, userID); err != nil {
		t.Fatalf("Deactivate returned error: %v", err)
	}

	user := env.users.users[userID]
	if user.Status != entity.UserStatusDeactivated {
		t.Fatalf("Status = %q, want deactivated", user.Status)
	}
	if user.DeactivatedAt == nil {
		t.Fatalf("DeactivatedAt = nil, want timestamp")
	}
	sessions, err := env.svc.ListSessions(ctx, userID.String())
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	for _, session := range sessions {
		if session.ID == result.SessionID {
			t.Fatalf("deactivated session %s is still listed", result.SessionID)
		}
	}
}

func TestDeactivateIdempotentAndRejectsSuspended(t *testing.T) {
	ctx := context.Background()
	env := newServiceSessionTestEnv(t)
	deactivatedID := env.addCredentialedUser(t, "deactivated@example.com", "Password1!", entity.UserStatusDeactivated)
	suspendedID := env.addCredentialedUser(t, "suspended@example.com", "Password1!", entity.UserStatusSuspended)

	if err := env.svc.Deactivate(ctx, deactivatedID); err != nil {
		t.Fatalf("Deactivate already-deactivated returned error: %v", err)
	}
	if err := env.svc.Deactivate(ctx, suspendedID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("Deactivate suspended error = %v, want FORBIDDEN", err)
	}
}

func TestDeactivateRevokeFailureDoesNotFlipStatus(t *testing.T) {
	ctx := context.Background()
	env := newServiceSessionTestEnv(t)
	userID := env.addCredentialedUser(t, "user@example.com", "Password1!", entity.UserStatusActive)
	env.svc.SetSessionOperationTimeout(20 * time.Millisecond)
	env.redis.Close()

	if err := env.svc.Deactivate(ctx, userID); !hasCode(err, cerrors.CodeInternal) {
		t.Fatalf("Deactivate error = %v, want INTERNAL", err)
	}
	user := env.users.users[userID]
	if user.Status != entity.UserStatusActive {
		t.Fatalf("Status = %q, want active after revoke failure", user.Status)
	}
	if user.DeactivatedAt != nil {
		t.Fatalf("DeactivatedAt = %v, want nil after revoke failure", user.DeactivatedAt)
	}
}

func TestReactivateAndLoginStatusMatrix(t *testing.T) {
	ctx := context.Background()
	env := newServiceSessionTestEnv(t)
	password := "Password1!"
	deactivatedID := env.addCredentialedUser(t, "deactivated@example.com", password, entity.UserStatusDeactivated)
	now := time.Now().UTC()
	env.users.users[deactivatedID].DeactivatedAt = &now
	env.addCredentialedUser(t, "active@example.com", password, entity.UserStatusActive)
	env.addCredentialedUser(t, "suspended@example.com", password, entity.UserStatusSuspended)

	if _, err := env.svc.ReactivateAndLogin(ctx, "deactivated@example.com", "wrong", "ua", "127.0.0.1"); !hasCode(err, cerrors.CodeUnauthorized) {
		t.Fatalf("wrong-password ReactivateAndLogin error = %v, want UNAUTHORIZED", err)
	}
	if _, err := env.svc.ReactivateAndLogin(ctx, "active@example.com", password, "ua", "127.0.0.1"); !hasCode(err, cerrors.CodeNotDeactivated) {
		t.Fatalf("active ReactivateAndLogin error = %v, want NOT_DEACTIVATED", err)
	}
	if _, err := env.svc.ReactivateAndLogin(ctx, "suspended@example.com", password, "ua", "127.0.0.1"); !hasCode(err, cerrors.CodeAccountSuspended) {
		t.Fatalf("suspended ReactivateAndLogin error = %v, want ACCOUNT_SUSPENDED", err)
	}

	result, err := env.svc.ReactivateAndLogin(ctx, "deactivated@example.com", password, "ua", "127.0.0.1")
	if err != nil {
		t.Fatalf("ReactivateAndLogin returned error: %v", err)
	}
	if result == nil || result.AccessToken == "" || result.RefreshToken == "" || result.SessionID == "" {
		t.Fatalf("ReactivateAndLogin result missing tokens: %+v", result)
	}
	user := env.users.users[deactivatedID]
	if user.Status != entity.UserStatusActive {
		t.Fatalf("Status = %q, want active", user.Status)
	}
	if user.DeactivatedAt != nil {
		t.Fatalf("DeactivatedAt = %v, want nil", user.DeactivatedAt)
	}
}

func TestReactivateAndLoginRollsBackWhenSessionCreationFails(t *testing.T) {
	ctx := context.Background()
	env := newServiceSessionTestEnv(t)
	userID := env.addCredentialedUser(t, "deactivated@example.com", "Password1!", entity.UserStatusDeactivated)
	original := time.Now().Add(-time.Hour).UTC()
	env.users.users[userID].DeactivatedAt = &original
	env.svc.SetSessionOperationTimeout(20 * time.Millisecond)
	env.redis.Close()

	if _, err := env.svc.ReactivateAndLogin(ctx, "deactivated@example.com", "Password1!", "ua", "127.0.0.1"); !hasCode(err, cerrors.CodeInternal) {
		t.Fatalf("ReactivateAndLogin error = %v, want INTERNAL", err)
	}

	user := env.users.users[userID]
	if user.Status != entity.UserStatusDeactivated {
		t.Fatalf("Status = %q, want deactivated after rollback", user.Status)
	}
	if user.DeactivatedAt == nil {
		t.Fatalf("DeactivatedAt = nil, want rollback timestamp")
	}
}

func hasCode(err error, code cerrors.Code) bool {
	appErr, ok := cerrors.AsAppError(err)
	return ok && appErr.Code == code
}

type serviceSessionTestEnv struct {
	svc   *Service
	users *fakeUserRepo
	redis *miniredis.Miniredis
	rdb   *redis.Client
}

func newServiceSessionTestEnv(t *testing.T) *serviceSessionTestEnv {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	users := &fakeUserRepo{users: map[uuid.UUID]*entity.User{}}
	svc := NewService(users, &fakeWorkspaceRepo{}, rdb, []byte("01234567890123456789012345678901"), time.Minute, time.Hour, nil)
	svc.SetSessionOperationTimeout(500 * time.Millisecond)
	t.Cleanup(func() {
		_ = rdb.Close()
		mr.Close()
	})
	return &serviceSessionTestEnv{svc: svc, users: users, redis: mr, rdb: rdb}
}

func (e *serviceSessionTestEnv) addCredentialedUser(t *testing.T, email, password string, status entity.UserStatus) uuid.UUID {
	t.Helper()
	passwordHash, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hashPassword returned error: %v", err)
	}
	userID := uuid.New()
	e.users.users[userID] = &entity.User{
		ID:           userID,
		Email:        email,
		DisplayName:  "Test User",
		PasswordHash: passwordHash,
		Status:       status,
		Locale:       "en",
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	return userID
}

type fakeNewUserSeeder struct {
	called bool
	userID uuid.UUID
}

func (s *fakeNewUserSeeder) SeedNewUser(_ context.Context, user *entity.User) error {
	s.called = true
	s.userID = user.ID
	return nil
}

type fakeUserRepo struct {
	users map[uuid.UUID]*entity.User
}

func (r *fakeUserRepo) Create(_ context.Context, user *entity.User) error {
	if r.users == nil {
		r.users = map[uuid.UUID]*entity.User{}
	}
	r.users[user.ID] = user
	return nil
}

func (r *fakeUserRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.User, error) {
	if user := r.users[id]; user != nil {
		return user, nil
	}
	return nil, cerrors.NotFound("user not found")
}

func (r *fakeUserRepo) GetByEmail(_ context.Context, email string) (*entity.User, error) {
	for _, user := range r.users {
		if user.Email == email {
			return user, nil
		}
	}
	return nil, cerrors.NotFound("user not found")
}

func (r *fakeUserRepo) Update(_ context.Context, user *entity.User) error {
	if r.users == nil {
		r.users = map[uuid.UUID]*entity.User{}
	}
	r.users[user.ID] = user
	return nil
}

type fakeWorkspaceRepo struct {
	workspaces map[uuid.UUID]*entity.Workspace
	bySlug     map[string]*entity.Workspace
	members    map[[2]uuid.UUID]*entity.WorkspaceMember
}

func (r *fakeWorkspaceRepo) Create(_ context.Context, ws *entity.Workspace) error {
	if r.workspaces == nil {
		r.workspaces = map[uuid.UUID]*entity.Workspace{}
	}
	if r.bySlug == nil {
		r.bySlug = map[string]*entity.Workspace{}
	}
	r.workspaces[ws.ID] = ws
	r.bySlug[ws.Slug] = ws
	return nil
}

func (r *fakeWorkspaceRepo) CreateWithOwner(ctx context.Context, ws *entity.Workspace, owner *entity.WorkspaceMember) error {
	if err := r.Create(ctx, ws); err != nil {
		return err
	}
	return r.AddMember(ctx, owner)
}

func (r *fakeWorkspaceRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Workspace, error) {
	if ws := r.workspaces[id]; ws != nil {
		return ws, nil
	}
	return nil, cerrors.NotFound("workspace not found")
}

func (r *fakeWorkspaceRepo) GetBySlug(_ context.Context, slug string) (*entity.Workspace, error) {
	if ws := r.bySlug[slug]; ws != nil {
		return ws, nil
	}
	return nil, cerrors.NotFound("workspace not found")
}

func (r *fakeWorkspaceRepo) ListByUser(_ context.Context, userID uuid.UUID) ([]entity.Workspace, error) {
	var result []entity.Workspace
	for key := range r.members {
		if key[1] != userID {
			continue
		}
		if ws := r.workspaces[key[0]]; ws != nil {
			result = append(result, *ws)
		}
	}
	return result, nil
}

func (r *fakeWorkspaceRepo) Update(_ context.Context, ws *entity.Workspace) error {
	if r.workspaces == nil {
		r.workspaces = map[uuid.UUID]*entity.Workspace{}
	}
	r.workspaces[ws.ID] = ws
	return nil
}

func (r *fakeWorkspaceRepo) AddMember(_ context.Context, m *entity.WorkspaceMember) error {
	if r.members == nil {
		r.members = map[[2]uuid.UUID]*entity.WorkspaceMember{}
	}
	r.members[[2]uuid.UUID{m.WorkspaceID, m.UserID}] = m
	return nil
}

func (r *fakeWorkspaceRepo) GetMember(_ context.Context, workspaceID, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	if member := r.members[[2]uuid.UUID{workspaceID, userID}]; member != nil {
		return member, nil
	}
	return nil, cerrors.NotFound("workspace member not found")
}

func (r *fakeWorkspaceRepo) ListMembers(_ context.Context, workspaceID uuid.UUID, _ pagination.Params) ([]entity.WorkspaceMember, error) {
	var members []entity.WorkspaceMember
	for key, member := range r.members {
		if key[0] == workspaceID {
			members = append(members, *member)
		}
	}
	return members, nil
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
