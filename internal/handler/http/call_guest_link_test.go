package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	wshandler "aloqa/internal/handler/ws"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
	callsvc "aloqa/internal/service/call"
	guestsvc "aloqa/internal/service/guest"
)

// captureInviteRepo records the last created guest invite so the test can
// assert the minted scope.
type captureInviteRepo struct {
	created *entity.GuestInvite
}

func (r *captureInviteRepo) Create(_ context.Context, invite *entity.GuestInvite) error {
	r.created = invite
	return nil
}
func (r *captureInviteRepo) GetByToken(context.Context, string) (*entity.GuestInvite, error) {
	return nil, cerrors.NotFound("invite not found")
}
func (r *captureInviteRepo) GetByID(context.Context, uuid.UUID) (*entity.GuestInvite, error) {
	return nil, cerrors.NotFound("invite not found")
}
func (r *captureInviteRepo) IncrementUseCount(context.Context, uuid.UUID) error { return nil }
func (r *captureInviteRepo) Revoke(context.Context, uuid.UUID) error            { return nil }
func (r *captureInviteRepo) ListByWorkspace(context.Context, uuid.UUID) ([]entity.GuestInvite, error) {
	return nil, nil
}

type guestLinkUserRepo struct{}

func (guestLinkUserRepo) Create(context.Context, *entity.User) error { return nil }
func (guestLinkUserRepo) GetByID(context.Context, uuid.UUID) (*entity.User, error) {
	return nil, cerrors.NotFound("user not found")
}
func (guestLinkUserRepo) GetByEmail(context.Context, string) (*entity.User, error) {
	return nil, cerrors.NotFound("user not found")
}
func (guestLinkUserRepo) Update(context.Context, *entity.User) error { return nil }

type guestLinkGrantRepo struct{}

func (guestLinkGrantRepo) CreateGrant(context.Context, *entity.GuestAccessGrant) error { return nil }
func (guestLinkGrantRepo) ListActiveByUserWorkspace(context.Context, uuid.UUID, uuid.UUID, time.Time) ([]entity.GuestAccessGrant, error) {
	return nil, nil
}

// newGuestLinkRouter builds an HTTP router whose authenticated user is the host
// of an active call of the given type/channel, wired with a real guest service
// so CreateGuestLink mints through the full path.
func newGuestLinkRouter(
	t *testing.T,
	workspaceID, callID, hostID uuid.UUID,
	channelID *uuid.UUID,
	callType entity.CallType,
) (http.Handler, *captureInviteRepo) {
	t.Helper()

	calls := &httpCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, ChannelID: channelID, Type: callType, Status: entity.CallStatusActive, CreatedBy: hostID},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: {ID: uuid.New(), CallID: callID, UserID: hostID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
		},
	}
	workspaces := &httpWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, hostID}: {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
	}}

	channelRepo := newGuestLinkChannelRepo(channelID, workspaceID)
	callService := callsvc.NewService(calls, httpBreakoutRepo{}, channelRepo, workspaces, httpNoopPublisher{}, nil, callsvc.MediaConfig{}, nil, nil)

	inviteRepo := &captureInviteRepo{}
	guestService := guestsvc.NewService(
		inviteRepo,
		guestLinkGrantRepo{},
		guestLinkUserRepo{},
		workspaces,
		channelRepo,
		nil,
	)

	router := NewRouter(RouterDeps{
		Auth:             &AuthHandler{},
		Account:          &AccountHandler{},
		Channels:         &ChannelHandler{},
		Messages:         &MessageHandler{},
		Calls:            NewCallHandler(callService, guestService),
		Breakout:         &BreakoutHandler{},
		Files:            &FileHandler{},
		Presence:         &PresenceHandler{},
		Recordings:       &RecordingHandler{},
		Notifications:    &NotificationHandler{},
		Search:           &SearchHandler{},
		Admin:            &AdminHandler{},
		Guests:           NewGuestHandler(guestService),
		WS:               &wshandler.Handler{},
		Validator:        fakeTokenValidator{userID: hostID},
		PersonalResolver: fakePersonalResolver{workspaceID: workspaceID},
	})
	return router, inviteRepo
}

// guestLinkChannelRepo serves an optional channel by ID so channel-anchored
// calls pass requireChannelAccess (public channel + workspace member host).
type guestLinkChannelRepo struct {
	channels map[uuid.UUID]*entity.Channel
}

func newGuestLinkChannelRepo(channelID *uuid.UUID, workspaceID uuid.UUID) *guestLinkChannelRepo {
	channels := map[uuid.UUID]*entity.Channel{}
	if channelID != nil {
		channels[*channelID] = &entity.Channel{ID: *channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic}
	}
	return &guestLinkChannelRepo{channels: channels}
}

func (r *guestLinkChannelRepo) Create(context.Context, *entity.Channel) error { return nil }
func (r *guestLinkChannelRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Channel, error) {
	if ch := r.channels[id]; ch != nil {
		return ch, nil
	}
	return nil, cerrors.NotFound("channel not found")
}
func (r *guestLinkChannelRepo) ListByWorkspace(context.Context, uuid.UUID, pagination.Params) ([]entity.Channel, error) {
	return nil, nil
}
func (r *guestLinkChannelRepo) ListByUser(context.Context, uuid.UUID, uuid.UUID) ([]entity.Channel, error) {
	return nil, nil
}
func (r *guestLinkChannelRepo) ListArchivedByUser(context.Context, uuid.UUID, uuid.UUID) ([]entity.ArchivedChannelInfo, error) {
	return nil, nil
}
func (r *guestLinkChannelRepo) Update(context.Context, *entity.Channel) error { return nil }
func (r *guestLinkChannelRepo) Archive(context.Context, uuid.UUID) error      { return nil }
func (r *guestLinkChannelRepo) AddMember(context.Context, *entity.ChannelMember) error {
	return nil
}
func (r *guestLinkChannelRepo) GetMember(context.Context, uuid.UUID, uuid.UUID) (*entity.ChannelMember, error) {
	return nil, cerrors.NotFound("channel member not found")
}
func (r *guestLinkChannelRepo) ListMembers(context.Context, uuid.UUID) ([]entity.ChannelMember, error) {
	return nil, nil
}
func (r *guestLinkChannelRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (r *guestLinkChannelRepo) UpdateLastRead(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *guestLinkChannelRepo) GetDMChannel(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*entity.Channel, error) {
	return nil, cerrors.NotFound("dm channel not found")
}

var _ repository.ChannelRepository = (*guestLinkChannelRepo)(nil)

func performGuestLinkRequest(router http.Handler, workspaceID, callID uuid.UUID) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/workspaces/"+workspaceID.String()+"/calls/"+callID.String()+"/guest-link",
		strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer test-token")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	return res
}

// The reported regression: a channel-less group/meeting call now mints a guest
// link (was 400 "guest links are only available for calls in a channel").
func TestCreateGuestLinkChannelLessCallReturns201(t *testing.T) {
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()

	router, inviteRepo := newGuestLinkRouter(t, workspaceID, callID, hostID, nil, entity.CallTypeGroup)
	res := performGuestLinkRequest(router, workspaceID, callID)
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", res.Code, res.Body.String())
	}

	var body guestLinkResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Token == "" {
		t.Fatalf("expected a non-empty token, body=%s", res.Body.String())
	}

	if inviteRepo.created == nil {
		t.Fatalf("expected an invite to be minted")
	}
	if inviteRepo.created.CallID == nil || *inviteRepo.created.CallID != callID {
		t.Fatalf("invite.CallID = %v, want %s", inviteRepo.created.CallID, callID)
	}
	if len(inviteRepo.created.ChannelIDs) != 0 {
		t.Fatalf("invite.ChannelIDs = %v, want empty (call-scoped)", inviteRepo.created.ChannelIDs)
	}
}

// resolveInviteRepo serves a fixed set of invites by token for resolve tests.
type resolveInviteRepo struct {
	byToken map[string]*entity.GuestInvite
}

func (r *resolveInviteRepo) Create(context.Context, *entity.GuestInvite) error { return nil }
func (r *resolveInviteRepo) GetByToken(_ context.Context, token string) (*entity.GuestInvite, error) {
	if inv := r.byToken[token]; inv != nil {
		return inv, nil
	}
	return nil, cerrors.NotFound("invite not found")
}
func (r *resolveInviteRepo) GetByID(context.Context, uuid.UUID) (*entity.GuestInvite, error) {
	return nil, cerrors.NotFound("invite not found")
}
func (r *resolveInviteRepo) IncrementUseCount(context.Context, uuid.UUID) error { return nil }
func (r *resolveInviteRepo) Revoke(context.Context, uuid.UUID) error            { return nil }
func (r *resolveInviteRepo) ListByWorkspace(context.Context, uuid.UUID) ([]entity.GuestInvite, error) {
	return nil, nil
}

type resolveCallLookup struct {
	call *entity.Call
}

func (l resolveCallLookup) GetByID(_ context.Context, id uuid.UUID) (*entity.Call, error) {
	if l.call != nil && l.call.ID == id {
		return l.call, nil
	}
	return nil, cerrors.NotFound("call not found")
}

// fakeSessionResolver maps session-cookie values to a user ID, mirroring the
// read-only Redis session lookup the real auth.Service performs. An unknown
// session resolves to uuid.Nil (anonymous), matching the redis.Nil branch.
type fakeSessionResolver struct {
	bySession map[string]uuid.UUID
}

func (f fakeSessionResolver) UserIDForSession(_ context.Context, sessionID string) (uuid.UUID, error) {
	return f.bySession[sessionID], nil
}

func newResolveRouter(workspaceID, callID, memberID uuid.UUID) http.Handler {
	return newResolveRouterWithSessions(workspaceID, callID, memberID, nil)
}

// newResolveRouterWithSessions builds the resolve router with an optional
// session-cookie resolver wired, so cookie-based member detection (no Bearer)
// can be exercised end to end through the public /api/v1/invites/{token} route.
func newResolveRouterWithSessions(workspaceID, callID, memberID uuid.UUID, sessions SessionUserResolver) http.Handler {
	invites := &resolveInviteRepo{byToken: map[string]*entity.GuestInvite{
		"valid-token": {
			ID:          uuid.New(),
			WorkspaceID: workspaceID,
			Token:       "valid-token",
			CallID:      &callID,
			MaxUses:     100,
			Status:      entity.GuestInviteStatusActive,
			ExpiresAt:   time.Now().Add(time.Hour),
		},
		"revoked-token": {
			ID:          uuid.New(),
			WorkspaceID: workspaceID,
			Token:       "revoked-token",
			CallID:      &callID,
			MaxUses:     100,
			Status:      entity.GuestInviteStatusRevoked,
			ExpiresAt:   time.Now().Add(time.Hour),
		},
	}}
	workspaces := &httpWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, memberID}: {WorkspaceID: workspaceID, UserID: memberID, Role: entity.WorkspaceRoleMember},
	}}
	guestService := guestsvc.NewService(invites, guestLinkGrantRepo{}, guestLinkUserRepo{}, workspaces, newGuestLinkChannelRepo(nil, workspaceID), nil)
	guestService.SetCallLookup(resolveCallLookup{call: &entity.Call{ID: callID, WorkspaceID: workspaceID, Status: entity.CallStatusActive, Title: "Standup"}})

	callService := callsvc.NewService(&httpCallRepo{calls: map[uuid.UUID]*entity.Call{}, participants: map[[2]uuid.UUID]*entity.CallParticipant{}}, httpBreakoutRepo{}, httpChannelRepo{}, workspaces, httpNoopPublisher{}, nil, callsvc.MediaConfig{}, nil, nil)

	return NewRouter(RouterDeps{
		Auth:             &AuthHandler{},
		Account:          &AccountHandler{},
		Channels:         &ChannelHandler{},
		Messages:         &MessageHandler{},
		Calls:            NewCallHandler(callService, guestService),
		Breakout:         &BreakoutHandler{},
		Files:            &FileHandler{},
		Presence:         &PresenceHandler{},
		Recordings:       &RecordingHandler{},
		Notifications:    &NotificationHandler{},
		Search:           &SearchHandler{},
		Admin:            &AdminHandler{},
		Guests:           NewGuestHandler(guestService),
		WS:               &wshandler.Handler{},
		Validator:        fakeTokenValidator{userID: memberID},
		PersonalResolver: fakePersonalResolver{workspaceID: workspaceID},
		SessionResolver:  sessions,
	})
}

func performResolveRequest(router http.Handler, token string, bearer bool) *httptest.ResponseRecorder {
	return performResolveRequestWithCookie(router, token, bearer, "")
}

// performResolveRequestWithCookie issues the resolve request optionally carrying
// a Bearer header and/or an `aloqa_session` cookie, exercising both
// optional-auth paths of the public endpoint.
func performResolveRequestWithCookie(router http.Handler, token string, bearer bool, sessionCookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/invites/"+token, nil)
	if bearer {
		req.Header.Set("Authorization", "Bearer member-token")
	}
	if sessionCookie != "" {
		req.AddCookie(&http.Cookie{Name: "aloqa_session", Value: sessionCookie})
	}
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	return res
}

func decodeResolve(t *testing.T, res *httptest.ResponseRecorder) resolveInviteResponse {
	t.Helper()
	var body resolveInviteResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode resolve response: %v (body=%s)", err, res.Body.String())
	}
	return body
}

func TestResolveGuestLinkHTTP(t *testing.T) {
	workspaceID := uuid.New()
	callID := uuid.New()
	memberID := uuid.New()

	t.Run("valid token anonymous returns call info", func(t *testing.T) {
		router := newResolveRouter(workspaceID, callID, memberID)
		res := performResolveRequest(router, "valid-token", false)
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", res.Code, res.Body.String())
		}
		body := decodeResolve(t, res)
		if !body.Valid {
			t.Fatalf("valid = false, want true")
		}
		if body.WorkspaceID == nil || *body.WorkspaceID != workspaceID {
			t.Fatalf("workspace_id = %v, want %s", body.WorkspaceID, workspaceID)
		}
		if body.CallID == nil || *body.CallID != callID {
			t.Fatalf("call_id = %v, want %s", body.CallID, callID)
		}
		if body.CallTitle != "Standup" {
			t.Fatalf("call_title = %q, want Standup", body.CallTitle)
		}
		if body.IsWorkspaceMember {
			t.Fatalf("is_workspace_member = true for anonymous, want false")
		}
	})

	t.Run("valid token with member session", func(t *testing.T) {
		router := newResolveRouter(workspaceID, callID, memberID)
		res := performResolveRequest(router, "valid-token", true)
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", res.Code, res.Body.String())
		}
		body := decodeResolve(t, res)
		if !body.Valid || !body.IsWorkspaceMember {
			t.Fatalf("expected valid + member, got %+v", body)
		}
	})

	t.Run("session cookie for member sets is_workspace_member", func(t *testing.T) {
		sessions := fakeSessionResolver{bySession: map[string]uuid.UUID{"sess-member": memberID}}
		router := newResolveRouterWithSessions(workspaceID, callID, memberID, sessions)
		// No Bearer header — only the session cookie, like a Next.js server page.
		res := performResolveRequestWithCookie(router, "valid-token", false, "sess-member")
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", res.Code, res.Body.String())
		}
		body := decodeResolve(t, res)
		if !body.Valid || !body.IsWorkspaceMember {
			t.Fatalf("expected valid + member from cookie, got %+v", body)
		}
	})

	t.Run("session cookie for non-member stays not a member", func(t *testing.T) {
		nonMemberID := uuid.New()
		sessions := fakeSessionResolver{bySession: map[string]uuid.UUID{"sess-guest": nonMemberID}}
		router := newResolveRouterWithSessions(workspaceID, callID, memberID, sessions)
		res := performResolveRequestWithCookie(router, "valid-token", false, "sess-guest")
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", res.Code, res.Body.String())
		}
		body := decodeResolve(t, res)
		if !body.Valid {
			t.Fatalf("valid = false, want true")
		}
		if body.IsWorkspaceMember {
			t.Fatalf("is_workspace_member = true for non-member session, want false")
		}
	})

	t.Run("unknown session cookie stays anonymous", func(t *testing.T) {
		sessions := fakeSessionResolver{bySession: map[string]uuid.UUID{}}
		router := newResolveRouterWithSessions(workspaceID, callID, memberID, sessions)
		res := performResolveRequestWithCookie(router, "valid-token", false, "sess-unknown")
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", res.Code, res.Body.String())
		}
		body := decodeResolve(t, res)
		if body.IsWorkspaceMember {
			t.Fatalf("is_workspace_member = true for unknown session, want false")
		}
	})

	t.Run("no cookie and no bearer stays anonymous", func(t *testing.T) {
		sessions := fakeSessionResolver{bySession: map[string]uuid.UUID{"sess-member": memberID}}
		router := newResolveRouterWithSessions(workspaceID, callID, memberID, sessions)
		res := performResolveRequestWithCookie(router, "valid-token", false, "")
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", res.Code, res.Body.String())
		}
		body := decodeResolve(t, res)
		if body.IsWorkspaceMember {
			t.Fatalf("is_workspace_member = true with no cookie/bearer, want false")
		}
	})

	t.Run("revoked token returns valid false at 200", func(t *testing.T) {
		router := newResolveRouter(workspaceID, callID, memberID)
		res := performResolveRequest(router, "revoked-token", false)
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", res.Code, res.Body.String())
		}
		body := decodeResolve(t, res)
		if body.Valid {
			t.Fatalf("valid = true for revoked token, want false")
		}
	})

	t.Run("unknown token returns valid false at 200", func(t *testing.T) {
		router := newResolveRouter(workspaceID, callID, memberID)
		res := performResolveRequest(router, "no-such-token", false)
		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", res.Code, res.Body.String())
		}
		body := decodeResolve(t, res)
		if body.Valid {
			t.Fatalf("valid = true for unknown token, want false")
		}
	})
}

// A channel call is also minted call-scoped (no channel grant) per the unified
// guest-link decision #3.
func TestCreateGuestLinkChannelCallIsCallScoped(t *testing.T) {
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	channelID := uuid.New()

	router, inviteRepo := newGuestLinkRouter(t, workspaceID, callID, hostID, &channelID, entity.CallTypeMeeting)
	res := performGuestLinkRequest(router, workspaceID, callID)
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", res.Code, res.Body.String())
	}
	if inviteRepo.created == nil {
		t.Fatalf("expected an invite to be minted")
	}
	if inviteRepo.created.CallID == nil || *inviteRepo.created.CallID != callID {
		t.Fatalf("invite.CallID = %v, want %s", inviteRepo.created.CallID, callID)
	}
	if len(inviteRepo.created.ChannelIDs) != 0 {
		t.Fatalf("invite.ChannelIDs = %v, want empty (call-scoped, no channel grant)", inviteRepo.created.ChannelIDs)
	}
}
