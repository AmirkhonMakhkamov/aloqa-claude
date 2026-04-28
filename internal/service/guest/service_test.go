package guest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
)

func TestCreateInviteRejectsCrossWorkspaceChannel(t *testing.T) {
	workspaceID := uuid.New()
	otherWorkspaceID := uuid.New()
	actorID := uuid.New()
	channelID := uuid.New()

	invites := &fakeInviteRepo{}
	grants := &fakeGuestAccessRepo{}
	svc := NewService(
		invites,
		grants,
		&fakeUserRepo{},
		&fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
			{workspaceID, actorID}: {WorkspaceID: workspaceID, UserID: actorID, Role: entity.WorkspaceRoleMember},
		}},
		&fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: otherWorkspaceID, Type: entity.ChannelTypePublic},
		}},
		nil,
	)

	_, err := svc.CreateInvite(context.Background(), CreateInviteInput{
		WorkspaceID: workspaceID,
		CreatedBy:   actorID,
		ChannelIDs:  []uuid.UUID{channelID},
	})

	requireAppErrorCode(t, err, cerrors.CodeForbidden)
	if invites.created != nil {
		t.Fatalf("invite was persisted despite cross-workspace channel")
	}
}

func TestCreateInviteRequiresPrivateChannelMembership(t *testing.T) {
	workspaceID := uuid.New()
	actorID := uuid.New()
	channelID := uuid.New()

	svc := NewService(
		&fakeInviteRepo{},
		&fakeGuestAccessRepo{},
		&fakeUserRepo{},
		&fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
			{workspaceID, actorID}: {WorkspaceID: workspaceID, UserID: actorID, Role: entity.WorkspaceRoleMember},
		}},
		&fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: workspaceID, Type: entity.ChannelTypePrivate},
		}},
		nil,
	)

	_, err := svc.CreateInvite(context.Background(), CreateInviteInput{
		WorkspaceID: workspaceID,
		CreatedBy:   actorID,
		ChannelIDs:  []uuid.UUID{channelID},
	})

	requireAppErrorCode(t, err, cerrors.CodeForbidden)
}

func TestCreateInviteRejectsNegativeTTLAndMaxUses(t *testing.T) {
	workspaceID := uuid.New()
	actorID := uuid.New()
	svc := NewService(
		&fakeInviteRepo{},
		&fakeGuestAccessRepo{},
		&fakeUserRepo{},
		&fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
			{workspaceID, actorID}: {WorkspaceID: workspaceID, UserID: actorID, Role: entity.WorkspaceRoleMember},
		}},
		&fakeChannelRepo{},
		nil,
	)

	_, err := svc.CreateInvite(context.Background(), CreateInviteInput{
		WorkspaceID: workspaceID,
		CreatedBy:   actorID,
		TTL:         -time.Second,
	})
	requireAppErrorCode(t, err, cerrors.CodeInvalidInput)

	_, err = svc.CreateInvite(context.Background(), CreateInviteInput{
		WorkspaceID: workspaceID,
		CreatedBy:   actorID,
		MaxUses:     -1,
	})
	requireAppErrorCode(t, err, cerrors.CodeInvalidInput)
}

func TestRedeemInviteValidatesChannelsBeforeSideEffects(t *testing.T) {
	workspaceID := uuid.New()
	otherWorkspaceID := uuid.New()
	channelID := uuid.New()
	inviteID := uuid.New()

	invites := &fakeInviteRepo{byToken: map[string]*entity.GuestInvite{
		"invite-token": {
			ID:          inviteID,
			WorkspaceID: workspaceID,
			Token:       "invite-token",
			ChannelIDs:  []uuid.UUID{channelID},
			MaxUses:     1,
			Status:      entity.GuestInviteStatusActive,
			ExpiresAt:   time.Now().Add(time.Hour),
		},
	}}
	users := &fakeUserRepo{}
	workspaces := &fakeWorkspaceRepo{}
	grants := &fakeGuestAccessRepo{}
	svc := NewService(
		invites,
		grants,
		users,
		workspaces,
		&fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: otherWorkspaceID, Type: entity.ChannelTypePublic},
		}},
		nil,
	)

	_, err := svc.RedeemInvite(context.Background(), RedeemInviteInput{
		Token:       "invite-token",
		Email:       "guest@example.com",
		DisplayName: "Guest User",
	})

	requireAppErrorCode(t, err, cerrors.CodeForbidden)
	if users.createCalled {
		t.Fatalf("user was created before invite channels were validated")
	}
	if grants.createCalled {
		t.Fatalf("guest access grant was created before invite channels were validated")
	}
}

func TestRedeemInviteCreatesGuestAccessGrantWithoutWorkspaceMembership(t *testing.T) {
	workspaceID := uuid.New()
	channelID := uuid.New()
	inviteID := uuid.New()

	invites := &fakeInviteRepo{byToken: map[string]*entity.GuestInvite{
		"invite-token": {
			ID:          inviteID,
			WorkspaceID: workspaceID,
			Token:       "invite-token",
			ChannelIDs:  []uuid.UUID{channelID},
			MaxUses:     1,
			Status:      entity.GuestInviteStatusActive,
			ExpiresAt:   time.Now().Add(time.Hour),
		},
	}}
	users := &fakeUserRepo{}
	workspaces := &fakeWorkspaceRepo{}
	grants := &fakeGuestAccessRepo{}
	svc := NewService(
		invites,
		grants,
		users,
		workspaces,
		&fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: workspaceID, Type: entity.ChannelTypePublic},
		}},
		nil,
	)

	result, err := svc.RedeemInvite(context.Background(), RedeemInviteInput{
		Token:       "invite-token",
		Email:       "guest@example.com",
		DisplayName: "Guest User",
	})
	if err != nil {
		t.Fatalf("RedeemInvite returned error: %v", err)
	}
	if result == nil || grants.created == nil {
		t.Fatalf("expected guest access grant to be created")
	}
	if workspaces.addMemberCalled {
		t.Fatalf("guest invite redemption should not create workspace membership")
	}
	if len(grants.created.ChannelIDs) != 1 || grants.created.ChannelIDs[0] != channelID {
		t.Fatalf("grant channels = %v, want [%s]", grants.created.ChannelIDs, channelID)
	}
}

func requireAppErrorCode(t *testing.T, err error, code cerrors.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error, got nil", code)
	}
	appErr, ok := cerrors.AsAppError(err)
	if !ok || appErr.Code != code {
		t.Fatalf("expected %s error, got %v", code, err)
	}
}

type fakeInviteRepo struct {
	created *entity.GuestInvite
	byToken map[string]*entity.GuestInvite
}

func (r *fakeInviteRepo) Create(_ context.Context, invite *entity.GuestInvite) error {
	r.created = invite
	return nil
}

func (r *fakeInviteRepo) GetByToken(_ context.Context, token string) (*entity.GuestInvite, error) {
	if invite := r.byToken[token]; invite != nil {
		return invite, nil
	}
	return nil, cerrors.NotFound("invite not found")
}

func (r *fakeInviteRepo) GetByID(context.Context, uuid.UUID) (*entity.GuestInvite, error) {
	return nil, cerrors.NotFound("invite not found")
}

func (r *fakeInviteRepo) IncrementUseCount(context.Context, uuid.UUID) error {
	return nil
}

func (r *fakeInviteRepo) Revoke(context.Context, uuid.UUID) error {
	return nil
}

func (r *fakeInviteRepo) ListByWorkspace(context.Context, uuid.UUID) ([]entity.GuestInvite, error) {
	return nil, nil
}

type fakeUserRepo struct {
	byEmail      map[string]*entity.User
	createCalled bool
}

func (r *fakeUserRepo) Create(_ context.Context, user *entity.User) error {
	r.createCalled = true
	if r.byEmail == nil {
		r.byEmail = make(map[string]*entity.User)
	}
	r.byEmail[user.Email] = user
	return nil
}

func (r *fakeUserRepo) GetByID(context.Context, uuid.UUID) (*entity.User, error) {
	return nil, cerrors.NotFound("user not found")
}

func (r *fakeUserRepo) GetByEmail(_ context.Context, email string) (*entity.User, error) {
	if user := r.byEmail[email]; user != nil {
		return user, nil
	}
	return nil, cerrors.NotFound("user not found")
}

func (r *fakeUserRepo) Update(context.Context, *entity.User) error {
	return nil
}

type fakeWorkspaceRepo struct {
	members         map[[2]uuid.UUID]*entity.WorkspaceMember
	addMemberCalled bool
	getMemberErr    error
}

func (r *fakeWorkspaceRepo) Create(context.Context, *entity.Workspace) error {
	return nil
}

func (r *fakeWorkspaceRepo) GetByID(context.Context, uuid.UUID) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}

func (r *fakeWorkspaceRepo) GetBySlug(context.Context, string) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}

func (r *fakeWorkspaceRepo) ListByUser(context.Context, uuid.UUID) ([]entity.Workspace, error) {
	return nil, nil
}

func (r *fakeWorkspaceRepo) Update(context.Context, *entity.Workspace) error {
	return nil
}

func (r *fakeWorkspaceRepo) AddMember(context.Context, *entity.WorkspaceMember) error {
	r.addMemberCalled = true
	return nil
}

func (r *fakeWorkspaceRepo) GetMember(_ context.Context, workspaceID, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	if r.getMemberErr != nil {
		return nil, r.getMemberErr
	}
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

func (r *fakeWorkspaceRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

type fakeGuestAccessRepo struct {
	created      *entity.GuestAccessGrant
	createCalled bool
	grants       []entity.GuestAccessGrant
}

func (r *fakeGuestAccessRepo) CreateGrant(_ context.Context, grant *entity.GuestAccessGrant) error {
	r.createCalled = true
	r.created = grant
	r.grants = append(r.grants, *grant)
	return nil
}

func (r *fakeGuestAccessRepo) ListActiveByUserWorkspace(_ context.Context, userID, workspaceID uuid.UUID, now time.Time) ([]entity.GuestAccessGrant, error) {
	var active []entity.GuestAccessGrant
	for _, grant := range r.grants {
		if grant.UserID == userID && grant.WorkspaceID == workspaceID && grant.ExpiresAt.After(now) {
			active = append(active, grant)
		}
	}
	return active, nil
}

type fakeChannelRepo struct {
	channels map[uuid.UUID]*entity.Channel
	members  map[[2]uuid.UUID]*entity.ChannelMember
	addErr   error
}

func (r *fakeChannelRepo) Create(context.Context, *entity.Channel) error {
	return nil
}

func (r *fakeChannelRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Channel, error) {
	if channel := r.channels[id]; channel != nil {
		return channel, nil
	}
	return nil, cerrors.NotFound("channel not found")
}

func (r *fakeChannelRepo) ListByWorkspace(context.Context, uuid.UUID, pagination.Params) ([]entity.Channel, error) {
	return nil, nil
}

func (r *fakeChannelRepo) ListByUser(context.Context, uuid.UUID, uuid.UUID) ([]entity.Channel, error) {
	return nil, nil
}

func (r *fakeChannelRepo) Update(context.Context, *entity.Channel) error {
	return nil
}

func (r *fakeChannelRepo) Archive(context.Context, uuid.UUID) error {
	return nil
}

func (r *fakeChannelRepo) AddMember(context.Context, *entity.ChannelMember) error {
	if r.addErr != nil {
		return r.addErr
	}
	return nil
}

func (r *fakeChannelRepo) GetMember(_ context.Context, channelID, userID uuid.UUID) (*entity.ChannelMember, error) {
	if member := r.members[[2]uuid.UUID{channelID, userID}]; member != nil {
		return member, nil
	}
	return nil, cerrors.NotFound("channel member not found")
}

func (r *fakeChannelRepo) ListMembers(context.Context, uuid.UUID) ([]entity.ChannelMember, error) {
	return nil, nil
}

func (r *fakeChannelRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (r *fakeChannelRepo) UpdateLastRead(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (r *fakeChannelRepo) GetDMChannel(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*entity.Channel, error) {
	return nil, cerrors.NotFound("dm channel not found")
}
