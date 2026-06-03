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
			channelID: {ID: channelID, WorkspaceID: &otherWorkspaceID, Type: entity.ChannelTypePublic},
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
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePrivate},
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
			channelID: {ID: channelID, WorkspaceID: &otherWorkspaceID, Type: entity.ChannelTypePublic},
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
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
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

// ALK-700: a call-scoped guest link mints a DISTINCT fresh guest user per redeem
// (no client email needed) and echoes the target call so the FE can route in.
func TestRedeemCallInviteMintsFreshGuestAndEchoesCall(t *testing.T) {
	workspaceID := uuid.New()
	channelID := uuid.New()
	callID := uuid.New()

	invites := &fakeInviteRepo{byToken: map[string]*entity.GuestInvite{
		"call-token": {
			ID:          uuid.New(),
			WorkspaceID: workspaceID,
			Token:       "call-token",
			ChannelIDs:  []uuid.UUID{channelID},
			CallID:      &callID,
			MaxUses:     100,
			Status:      entity.GuestInviteStatusActive,
			ExpiresAt:   time.Now().Add(time.Hour),
		},
	}}
	users := &fakeUserRepo{}
	svc := NewService(
		invites,
		&fakeGuestAccessRepo{},
		users,
		&fakeWorkspaceRepo{},
		&fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		}},
		nil,
	)
	svc.SetCallLookup(&fakeCallLookup{call: &entity.Call{ID: callID, WorkspaceID: workspaceID, Status: entity.CallStatusActive}})

	// No client email supplied — the call-invite path synthesizes one.
	result, err := svc.RedeemInvite(context.Background(), RedeemInviteInput{
		Token:       "call-token",
		DisplayName: "Guest User",
	})
	if err != nil {
		t.Fatalf("RedeemInvite returned error: %v", err)
	}
	if !users.createCalled {
		t.Fatalf("expected a fresh guest user to be created")
	}
	if result.CallID == nil || *result.CallID != callID {
		t.Fatalf("result.CallID = %v, want %s", result.CallID, callID)
	}
	if result.User == nil || result.User.DisplayName != "Guest User" {
		t.Fatalf("expected guest display name to be preserved, got %+v", result.User)
	}
}

// Regression (2026-06-03 prod): a call-scoped guest link carries NO channels, so the
// redeemed grant's channel_ids is built from an empty invite list. The grant must still
// persist a NON-NIL empty slice: guest_access_grants.channel_ids is NOT NULL, and a nil
// []uuid.UUID binds as SQL NULL — which violated the constraint and 500'd every redeem
// (the FE folded the 500 into a "Network error" so guests could never join the call).
func TestRedeemCallInviteWithoutChannelsGrantsNonNilChannelIDs(t *testing.T) {
	workspaceID := uuid.New()
	callID := uuid.New()

	invites := &fakeInviteRepo{byToken: map[string]*entity.GuestInvite{
		"call-token": {
			ID:          uuid.New(),
			WorkspaceID: workspaceID,
			Token:       "call-token",
			ChannelIDs:  nil, // call-scoped link grants access to the call only — no channels
			CallID:      &callID,
			MaxUses:     100,
			Status:      entity.GuestInviteStatusActive,
			ExpiresAt:   time.Now().Add(time.Hour),
		},
	}}
	grants := &fakeGuestAccessRepo{}
	svc := NewService(invites, grants, &fakeUserRepo{}, &fakeWorkspaceRepo{}, &fakeChannelRepo{}, nil)
	svc.SetCallLookup(&fakeCallLookup{call: &entity.Call{ID: callID, WorkspaceID: workspaceID, Status: entity.CallStatusActive}})

	_, err := svc.RedeemInvite(context.Background(), RedeemInviteInput{
		Token:       "call-token",
		DisplayName: "Guest User",
	})
	if err != nil {
		t.Fatalf("RedeemInvite returned error: %v", err)
	}
	if grants.created == nil {
		t.Fatalf("expected a guest access grant to be created")
	}
	if grants.created.ChannelIDs == nil {
		t.Fatalf("grant channel_ids is nil; guest_access_grants.channel_ids is NOT NULL, so a nil slice binds as SQL NULL and 500s the redeem")
	}
	if len(grants.created.ChannelIDs) != 0 {
		t.Fatalf("call-scoped grant should carry no channels, got %v", grants.created.ChannelIDs)
	}
}

// ALK-700: redeeming a call link for an ended call is rejected before any
// user/session/grant is minted.
func TestRedeemCallInviteRejectsEndedCall(t *testing.T) {
	workspaceID := uuid.New()
	channelID := uuid.New()
	callID := uuid.New()

	invites := &fakeInviteRepo{byToken: map[string]*entity.GuestInvite{
		"call-token": {
			ID:          uuid.New(),
			WorkspaceID: workspaceID,
			Token:       "call-token",
			ChannelIDs:  []uuid.UUID{channelID},
			CallID:      &callID,
			MaxUses:     100,
			Status:      entity.GuestInviteStatusActive,
			ExpiresAt:   time.Now().Add(time.Hour),
		},
	}}
	users := &fakeUserRepo{}
	svc := NewService(
		invites,
		&fakeGuestAccessRepo{},
		users,
		&fakeWorkspaceRepo{},
		&fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		}},
		nil,
	)
	svc.SetCallLookup(&fakeCallLookup{call: &entity.Call{ID: callID, WorkspaceID: workspaceID, Status: entity.CallStatusEnded}})

	_, err := svc.RedeemInvite(context.Background(), RedeemInviteInput{
		Token:       "call-token",
		DisplayName: "Guest User",
	})
	requireAppErrorCode(t, err, cerrors.CodeCallEnded)
	if users.createCalled {
		t.Fatalf("ended-call redeem must not create a user")
	}
}

// A call-scoped invite (no channel IDs) redeems into a call-scoped grant: the
// grant carries the target CallID and no channel IDs, so the guest gets access
// to that one call only. (unified guest link)
func TestRedeemCallScopedInviteCreatesCallScopedGrant(t *testing.T) {
	workspaceID := uuid.New()
	callID := uuid.New()

	invites := &fakeInviteRepo{byToken: map[string]*entity.GuestInvite{
		"call-token": {
			ID:          uuid.New(),
			WorkspaceID: workspaceID,
			Token:       "call-token",
			ChannelIDs:  nil, // call-scoped: no channel access
			CallID:      &callID,
			MaxUses:     100,
			Status:      entity.GuestInviteStatusActive,
			ExpiresAt:   time.Now().Add(time.Hour),
		},
	}}
	grants := &fakeGuestAccessRepo{}
	svc := NewService(
		invites,
		grants,
		&fakeUserRepo{},
		&fakeWorkspaceRepo{},
		&fakeChannelRepo{},
		nil,
	)
	svc.SetCallLookup(&fakeCallLookup{call: &entity.Call{ID: callID, WorkspaceID: workspaceID, Status: entity.CallStatusActive}})

	result, err := svc.RedeemInvite(context.Background(), RedeemInviteInput{
		Token:       "call-token",
		DisplayName: "Guest User",
	})
	if err != nil {
		t.Fatalf("RedeemInvite returned error: %v", err)
	}
	if result.CallID == nil || *result.CallID != callID {
		t.Fatalf("result.CallID = %v, want %s", result.CallID, callID)
	}
	if grants.created == nil {
		t.Fatalf("expected a guest access grant to be created")
	}
	if grants.created.CallID == nil || *grants.created.CallID != callID {
		t.Fatalf("grant.CallID = %v, want %s", grants.created.CallID, callID)
	}
	if len(grants.created.ChannelIDs) != 0 {
		t.Fatalf("grant.ChannelIDs = %v, want empty (call-scoped)", grants.created.ChannelIDs)
	}
}

// ResolveInvite returns call info for a valid token, an invalid result (not an
// error) for an expired/used/revoked token, and reports workspace membership
// only when an authenticated member user is supplied. (unified guest link)
func TestResolveInvite(t *testing.T) {
	workspaceID := uuid.New()
	callID := uuid.New()
	memberID := uuid.New()
	expiresAt := time.Now().Add(time.Hour)

	invites := &fakeInviteRepo{byToken: map[string]*entity.GuestInvite{
		"valid-token": {
			ID:          uuid.New(),
			WorkspaceID: workspaceID,
			Token:       "valid-token",
			CallID:      &callID,
			MaxUses:     100,
			Status:      entity.GuestInviteStatusActive,
			ExpiresAt:   expiresAt,
		},
		"revoked-token": {
			ID:          uuid.New(),
			WorkspaceID: workspaceID,
			Token:       "revoked-token",
			CallID:      &callID,
			MaxUses:     100,
			Status:      entity.GuestInviteStatusRevoked,
			ExpiresAt:   expiresAt,
		},
	}}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, memberID}: {WorkspaceID: workspaceID, UserID: memberID, Role: entity.WorkspaceRoleMember},
	}}
	svc := NewService(invites, &fakeGuestAccessRepo{}, &fakeUserRepo{}, workspaces, &fakeChannelRepo{}, nil)
	svc.SetCallLookup(&fakeCallLookup{call: &entity.Call{ID: callID, WorkspaceID: workspaceID, Status: entity.CallStatusActive, Title: "Standup"}})

	t.Run("valid anonymous", func(t *testing.T) {
		res, err := svc.ResolveInvite(context.Background(), "valid-token", uuid.Nil)
		if err != nil {
			t.Fatalf("ResolveInvite returned error: %v", err)
		}
		if !res.Valid {
			t.Fatalf("Valid = false, want true")
		}
		if res.WorkspaceID != workspaceID || res.CallID == nil || *res.CallID != callID {
			t.Fatalf("workspace/call mismatch: %+v", res)
		}
		if res.CallTitle != "Standup" {
			t.Fatalf("CallTitle = %q, want Standup", res.CallTitle)
		}
		if res.IsWorkspaceMember {
			t.Fatalf("IsWorkspaceMember = true for anonymous, want false")
		}
	})

	t.Run("valid member", func(t *testing.T) {
		res, err := svc.ResolveInvite(context.Background(), "valid-token", memberID)
		if err != nil {
			t.Fatalf("ResolveInvite returned error: %v", err)
		}
		if !res.Valid || !res.IsWorkspaceMember {
			t.Fatalf("expected valid + member, got %+v", res)
		}
	})

	t.Run("valid non-member", func(t *testing.T) {
		res, err := svc.ResolveInvite(context.Background(), "valid-token", uuid.New())
		if err != nil {
			t.Fatalf("ResolveInvite returned error: %v", err)
		}
		if res.IsWorkspaceMember {
			t.Fatalf("IsWorkspaceMember = true for non-member, want false")
		}
	})

	t.Run("revoked token is invalid not error", func(t *testing.T) {
		res, err := svc.ResolveInvite(context.Background(), "revoked-token", uuid.Nil)
		if err != nil {
			t.Fatalf("ResolveInvite returned error for revoked token: %v", err)
		}
		if res.Valid {
			t.Fatalf("Valid = true for revoked token, want false")
		}
	})

	t.Run("unknown token is invalid not error", func(t *testing.T) {
		res, err := svc.ResolveInvite(context.Background(), "does-not-exist", uuid.Nil)
		if err != nil {
			t.Fatalf("ResolveInvite returned error for unknown token: %v", err)
		}
		if res.Valid {
			t.Fatalf("Valid = true for unknown token, want false")
		}
	})
}

// A call-scoped invite whose target call has ALREADY ENDED must resolve as
// invalid (mirroring RedeemInvite's hard reject) and leak no call/workspace
// metadata — otherwise the FE shows a joinable state for a dead link. (unified
// guest link)
func TestResolveInviteEndedCallIsInvalid(t *testing.T) {
	workspaceID := uuid.New()
	callID := uuid.New()
	expiresAt := time.Now().Add(time.Hour)

	invites := &fakeInviteRepo{byToken: map[string]*entity.GuestInvite{
		"call-token": {
			ID:          uuid.New(),
			WorkspaceID: workspaceID,
			Token:       "call-token",
			CallID:      &callID,
			MaxUses:     100,
			Status:      entity.GuestInviteStatusActive,
			ExpiresAt:   expiresAt,
		},
	}}
	svc := NewService(invites, &fakeGuestAccessRepo{}, &fakeUserRepo{}, &fakeWorkspaceRepo{}, &fakeChannelRepo{}, nil)
	svc.SetCallLookup(&fakeCallLookup{call: &entity.Call{ID: callID, WorkspaceID: workspaceID, Status: entity.CallStatusEnded, Title: "Standup"}})

	res, err := svc.ResolveInvite(context.Background(), "call-token", uuid.Nil)
	if err != nil {
		t.Fatalf("ResolveInvite returned error for ended call: %v", err)
	}
	if res.Valid {
		t.Fatalf("Valid = true for ended-call invite, want false")
	}
	if res.WorkspaceID != uuid.Nil || res.CallID != nil || res.CallTitle != "" {
		t.Fatalf("ended-call invite leaked metadata: %+v", res)
	}
}

// A call-scoped invite whose target call no longer exists resolves as invalid.
func TestResolveInviteMissingCallIsInvalid(t *testing.T) {
	workspaceID := uuid.New()
	callID := uuid.New()

	invites := &fakeInviteRepo{byToken: map[string]*entity.GuestInvite{
		"call-token": {
			ID:          uuid.New(),
			WorkspaceID: workspaceID,
			Token:       "call-token",
			CallID:      &callID,
			MaxUses:     100,
			Status:      entity.GuestInviteStatusActive,
			ExpiresAt:   time.Now().Add(time.Hour),
		},
	}}
	svc := NewService(invites, &fakeGuestAccessRepo{}, &fakeUserRepo{}, &fakeWorkspaceRepo{}, &fakeChannelRepo{}, nil)
	svc.SetCallLookup(&fakeCallLookup{call: nil}) // GetByID -> NotFound

	res, err := svc.ResolveInvite(context.Background(), "call-token", uuid.Nil)
	if err != nil {
		t.Fatalf("ResolveInvite returned error for missing call: %v", err)
	}
	if res.Valid {
		t.Fatalf("Valid = true for missing-call invite, want false")
	}
}

// A transient / internal GetByToken error (DB down) must surface as an error so
// the handler returns 5xx, NOT be silently collapsed to valid:false (which would
// mask the outage and downgrade a member's good link to the guest form). A
// genuine NotFound still returns valid:false. (unified guest link)
func TestResolveInviteSurfacesTransientLookupError(t *testing.T) {
	workspaceID := uuid.New()

	t.Run("transient GetByToken error surfaces", func(t *testing.T) {
		invites := &fakeInviteRepo{getByTokenErr: cerrors.Internal("db down", nil)}
		svc := NewService(invites, &fakeGuestAccessRepo{}, &fakeUserRepo{}, &fakeWorkspaceRepo{}, &fakeChannelRepo{}, nil)

		_, err := svc.ResolveInvite(context.Background(), "any-token", uuid.Nil)
		requireAppErrorCode(t, err, cerrors.CodeInternal)
	})

	t.Run("not-found GetByToken still resolves invalid", func(t *testing.T) {
		invites := &fakeInviteRepo{byToken: map[string]*entity.GuestInvite{}}
		svc := NewService(invites, &fakeGuestAccessRepo{}, &fakeUserRepo{}, &fakeWorkspaceRepo{}, &fakeChannelRepo{}, nil)

		res, err := svc.ResolveInvite(context.Background(), "missing", uuid.Nil)
		if err != nil {
			t.Fatalf("ResolveInvite returned error for not-found token: %v", err)
		}
		if res.Valid {
			t.Fatalf("Valid = true for not-found token, want false")
		}
		_ = workspaceID
	})
}

type fakeCallLookup struct {
	call *entity.Call
}

func (f *fakeCallLookup) GetByID(context.Context, uuid.UUID) (*entity.Call, error) {
	if f.call == nil {
		return nil, cerrors.NotFound("call not found")
	}
	return f.call, nil
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
	created       *entity.GuestInvite
	byToken       map[string]*entity.GuestInvite
	getByTokenErr error
}

func (r *fakeInviteRepo) Create(_ context.Context, invite *entity.GuestInvite) error {
	r.created = invite
	return nil
}

func (r *fakeInviteRepo) GetByToken(_ context.Context, token string) (*entity.GuestInvite, error) {
	if r.getByTokenErr != nil {
		return nil, r.getByTokenErr
	}
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

func (r *fakeChannelRepo) ListArchivedByUser(context.Context, uuid.UUID, uuid.UUID) ([]entity.ArchivedChannelInfo, error) {
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
