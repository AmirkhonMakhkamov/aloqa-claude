package http

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	wshandler "aloqa/internal/handler/ws"
	callsvc "aloqa/internal/service/call"
)

// newShareControlHTTPRouter builds a router whose authenticated user is the
// host of a meeting call, with one extra connected participant (the target of
// resolve/revoke/featured actions). actorRole controls the auth user's role so
// non-host RBAC paths can be exercised.
func newShareControlHTTPRouter(
	workspaceID, callID, actorID, targetID uuid.UUID,
	actorRole entity.CallRole,
) http.Handler {
	calls := &httpCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive, CreatedBy: actorID},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, actorID}:  {ID: uuid.New(), CallID: callID, UserID: actorID, Role: actorRole, Status: entity.ParticipantStatusConnected},
			{callID, targetID}: {ID: uuid.New(), CallID: callID, UserID: targetID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
		},
	}
	workspaces := &httpWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, actorID}:  {WorkspaceID: workspaceID, UserID: actorID, Role: entity.WorkspaceRoleMember},
		{workspaceID, targetID}: {WorkspaceID: workspaceID, UserID: targetID, Role: entity.WorkspaceRoleMember},
	}}
	svc := callsvc.NewService(calls, httpBreakoutRepo{}, httpChannelRepo{}, workspaces, httpNoopPublisher{}, nil, callsvc.MediaConfig{}, nil, nil, nil)
	return NewRouter(RouterDeps{
		Auth:             &AuthHandler{},
		Account:          &AccountHandler{},
		Channels:         &ChannelHandler{},
		Messages:         &MessageHandler{},
		Calls:            NewCallHandler(svc, nil),
		Breakout:         &BreakoutHandler{},
		Files:            &FileHandler{},
		Presence:         &PresenceHandler{},
		Recordings:       &RecordingHandler{},
		Notifications:    &NotificationHandler{},
		Search:           &SearchHandler{},
		Admin:            &AdminHandler{},
		Guests:           &GuestHandler{},
		WS:               &wshandler.Handler{},
		Validator:        fakeTokenValidator{userID: actorID},
		PersonalResolver: fakePersonalResolver{workspaceID: workspaceID},
	})
}

func TestResolveShareRequestHTTP(t *testing.T) {
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	targetID := uuid.New()

	t.Run("host approve returns 204", func(t *testing.T) {
		router := newShareControlHTTPRouter(workspaceID, callID, hostID, targetID, entity.CallRoleHost)
		res := performCallInteractionRequest(router, http.MethodPost, workspaceID, callID,
			"/share-requests/"+targetID.String()+"/resolve", `{"approved":true}`, true)
		if res.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204, body=%s", res.Code, res.Body.String())
		}
	})

	t.Run("non-host returns 403", func(t *testing.T) {
		// actor is a plain participant, target is the host slot's peer.
		router := newShareControlHTTPRouter(workspaceID, callID, hostID, targetID, entity.CallRoleParticipant)
		res := performCallInteractionRequest(router, http.MethodPost, workspaceID, callID,
			"/share-requests/"+targetID.String()+"/resolve", `{"approved":true}`, true)
		if res.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403, body=%s", res.Code, res.Body.String())
		}
	})

	t.Run("malformed body returns 400", func(t *testing.T) {
		router := newShareControlHTTPRouter(workspaceID, callID, hostID, targetID, entity.CallRoleHost)
		res := performCallInteractionRequest(router, http.MethodPost, workspaceID, callID,
			"/share-requests/"+targetID.String()+"/resolve", `{"approved":`, true)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400, body=%s", res.Code, res.Body.String())
		}
	})

	t.Run("missing approved field returns 400", func(t *testing.T) {
		router := newShareControlHTTPRouter(workspaceID, callID, hostID, targetID, entity.CallRoleHost)
		res := performCallInteractionRequest(router, http.MethodPost, workspaceID, callID,
			"/share-requests/"+targetID.String()+"/resolve", `{}`, true)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400, body=%s", res.Code, res.Body.String())
		}
	})

	t.Run("unauthenticated returns 401", func(t *testing.T) {
		router := newShareControlHTTPRouter(workspaceID, callID, hostID, targetID, entity.CallRoleHost)
		res := performCallInteractionRequest(router, http.MethodPost, workspaceID, callID,
			"/share-requests/"+targetID.String()+"/resolve", `{"approved":true}`, false)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401, body=%s", res.Code, res.Body.String())
		}
	})
}

func TestRequestScreenShareHTTPReturns204(t *testing.T) {
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	targetID := uuid.New()
	// Auth user is the plain participant requesting permission (self).
	router := newShareControlHTTPRouter(workspaceID, callID, targetID, hostID, entity.CallRoleParticipant)
	res := performCallInteractionRequest(router, http.MethodPost, workspaceID, callID, "/share-requests", "", true)
	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204, body=%s", res.Code, res.Body.String())
	}
}

func TestRevokeScreenShareHTTP(t *testing.T) {
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	targetID := uuid.New()

	t.Run("host revoke returns 204", func(t *testing.T) {
		router := newShareControlHTTPRouter(workspaceID, callID, hostID, targetID, entity.CallRoleHost)
		res := performCallInteractionRequest(router, http.MethodPost, workspaceID, callID,
			"/participants/"+targetID.String()+"/revoke-screen-share", "", true)
		if res.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204, body=%s", res.Code, res.Body.String())
		}
	})

	t.Run("non-host returns 403", func(t *testing.T) {
		router := newShareControlHTTPRouter(workspaceID, callID, hostID, targetID, entity.CallRoleParticipant)
		res := performCallInteractionRequest(router, http.MethodPost, workspaceID, callID,
			"/participants/"+targetID.String()+"/revoke-screen-share", "", true)
		if res.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403, body=%s", res.Code, res.Body.String())
		}
	})
}

func TestSetFeaturedShareHTTP(t *testing.T) {
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	targetID := uuid.New()

	t.Run("host set returns 204", func(t *testing.T) {
		router := newShareControlHTTPRouter(workspaceID, callID, hostID, targetID, entity.CallRoleHost)
		res := performCallInteractionRequest(router, http.MethodPut, workspaceID, callID,
			"/featured-share", `{"user_id":"`+targetID.String()+`"}`, true)
		if res.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204, body=%s", res.Code, res.Body.String())
		}
	})

	t.Run("host clear (null) returns 204", func(t *testing.T) {
		router := newShareControlHTTPRouter(workspaceID, callID, hostID, targetID, entity.CallRoleHost)
		res := performCallInteractionRequest(router, http.MethodPut, workspaceID, callID,
			"/featured-share", `{"user_id":null}`, true)
		if res.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204, body=%s", res.Code, res.Body.String())
		}
	})

	t.Run("non-host returns 403", func(t *testing.T) {
		router := newShareControlHTTPRouter(workspaceID, callID, hostID, targetID, entity.CallRoleParticipant)
		res := performCallInteractionRequest(router, http.MethodPut, workspaceID, callID,
			"/featured-share", `{"user_id":"`+targetID.String()+`"}`, true)
		if res.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403, body=%s", res.Code, res.Body.String())
		}
	})

	t.Run("malformed body returns 400", func(t *testing.T) {
		router := newShareControlHTTPRouter(workspaceID, callID, hostID, targetID, entity.CallRoleHost)
		res := performCallInteractionRequest(router, http.MethodPut, workspaceID, callID,
			"/featured-share", `{"user_id":`, true)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400, body=%s", res.Code, res.Body.String())
		}
	})
}
