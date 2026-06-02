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
