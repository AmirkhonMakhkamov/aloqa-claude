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
	wshandler "aloqa/internal/handler/ws"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
	callsvc "aloqa/internal/service/call"
)

func TestCallInteractionHTTPHappyPaths(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "raise hand", method: http.MethodPost, path: "/hand"},
		{name: "lower hand", method: http.MethodDelete, path: "/hand"},
		{name: "reaction", method: http.MethodPost, path: "/reactions", body: `{"emoji":"👍"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			userID := uuid.New()
			workspaceID := uuid.New()
			callID := uuid.New()
			router, _ := newCallInteractionHTTPRouter(workspaceID, callID, userID, entity.CallStatusActive, true)

			res := performCallInteractionRequest(router, tc.method, workspaceID, callID, tc.path, tc.body, true)
			if res.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204, body=%s", res.Code, res.Body.String())
			}
		})
	}
}

func TestCallInteractionHTTPAuthAndForbidden(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		userID := uuid.New()
		workspaceID := uuid.New()
		callID := uuid.New()
		router, _ := newCallInteractionHTTPRouter(workspaceID, callID, userID, entity.CallStatusActive, true)

		res := performCallInteractionRequest(router, http.MethodPost, workspaceID, callID, "/hand", "", false)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401, body=%s", res.Code, res.Body.String())
		}
	})

	t.Run("not in call", func(t *testing.T) {
		userID := uuid.New()
		workspaceID := uuid.New()
		callID := uuid.New()
		router, _ := newCallInteractionHTTPRouter(workspaceID, callID, userID, entity.CallStatusActive, false)

		res := performCallInteractionRequest(router, http.MethodPost, workspaceID, callID, "/hand", "", true)
		if res.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403, body=%s", res.Code, res.Body.String())
		}
	})

	t.Run("ended call", func(t *testing.T) {
		userID := uuid.New()
		workspaceID := uuid.New()
		callID := uuid.New()
		router, _ := newCallInteractionHTTPRouter(workspaceID, callID, userID, entity.CallStatusEnded, true)

		res := performCallInteractionRequest(router, http.MethodPost, workspaceID, callID, "/hand", "", true)
		if res.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403, body=%s", res.Code, res.Body.String())
		}
	})
}

func TestCallReactionHTTPBadRequests(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "empty emoji", body: `{"emoji":""}`},
		{name: "oversize emoji", body: `{"emoji":"` + strings.Repeat("a", 33) + `"}`},
		{name: "invalid utf8", body: string([]byte{'{', '"', 'e', 'm', 'o', 'j', 'i', '"', ':', '"', 0xff, '"', '}'})},
		{name: "malformed json", body: `{"emoji":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			userID := uuid.New()
			workspaceID := uuid.New()
			callID := uuid.New()
			router, _ := newCallInteractionHTTPRouter(workspaceID, callID, userID, entity.CallStatusActive, true)

			res := performCallInteractionRequest(router, http.MethodPost, workspaceID, callID, "/reactions", tc.body, true)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body=%s", res.Code, res.Body.String())
			}
		})
	}
}

func TestCallTurnCredentialsHTTP(t *testing.T) {
	userID := uuid.New()
	workspaceID := uuid.New()
	callID := uuid.New()
	router, _ := newCallInteractionHTTPRouterWithMediaConfig(workspaceID, callID, userID, entity.CallStatusActive, true, callsvc.MediaConfig{
		TURNURLs:           []string{"turn:turn.example.test:3478", "turns:turn.example.test:5349"},
		TURNUsername:       "turn-user",
		TURNCredential:     "turn-secret",
		TURNCredentialsTTL: 10 * time.Minute,
	})

	res := performCallInteractionRequest(router, http.MethodPost, workspaceID, callID, "/turn-credentials", "", true)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", res.Code, res.Body.String())
	}

	var body callsvc.TurnCredentials
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.URLs) != 2 || body.URLs[0] != "turn:turn.example.test:3478" || body.URLs[1] != "turns:turn.example.test:5349" {
		t.Fatalf("urls = %+v, want configured urls", body.URLs)
	}
	if body.Username != "turn-user" || body.Credential != "turn-secret" || body.TTL != 600 {
		t.Fatalf("credentials = %+v, want configured values", body)
	}
}

func TestCallTurnCredentialsHTTPStunOnlyWhenTurnUnconfigured(t *testing.T) {
	userID := uuid.New()
	workspaceID := uuid.New()
	callID := uuid.New()
	// TURN intentionally unconfigured; a connected participant should get a 200
	// with STUN-only servers instead of a 503 (ALK-639).
	router, _ := newCallInteractionHTTPRouterWithMediaConfig(workspaceID, callID, userID, entity.CallStatusActive, true, callsvc.MediaConfig{
		STUNURLs: []string{"stun:stun.l.google.com:19302"},
	})

	res := performCallInteractionRequest(router, http.MethodPost, workspaceID, callID, "/turn-credentials", "", true)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (STUN-only fallback), body=%s", res.Code, res.Body.String())
	}

	var body callsvc.TurnCredentials
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.URLs) != 1 || body.URLs[0] != "stun:stun.l.google.com:19302" {
		t.Fatalf("urls = %+v, want STUN-only", body.URLs)
	}
	if body.Username != "" || body.Credential != "" || body.TTL != 0 {
		t.Fatalf("expected no credentials/ttl for STUN-only, got %+v", body)
	}
}

func TestCallTurnCredentialsHTTPForbidsNonParticipantEvenWhenTurnUnconfigured(t *testing.T) {
	userID := uuid.New()
	workspaceID := uuid.New()
	callID := uuid.New()
	// Access checks must run BEFORE the TURN-config branch: a non-participant
	// gets 403, not a STUN-only 200 (ALK-639 reorder regression guard).
	router, _ := newCallInteractionHTTPRouterWithMediaConfig(workspaceID, callID, userID, entity.CallStatusActive, false, callsvc.MediaConfig{
		STUNURLs: []string{"stun:stun.l.google.com:19302"},
	})

	res := performCallInteractionRequest(router, http.MethodPost, workspaceID, callID, "/turn-credentials", "", true)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for non-participant, body=%s", res.Code, res.Body.String())
	}
}

func TestCallLeaveHTTPReturnsJSONBody(t *testing.T) {
	userID := uuid.New()
	workspaceID := uuid.New()
	callID := uuid.New()
	router, _ := newCallInteractionHTTPRouter(workspaceID, callID, userID, entity.CallStatusActive, true)

	res := performCallInteractionRequest(router, http.MethodPost, workspaceID, callID, "/leave", "", true)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", res.Code, res.Body.String())
	}

	var body callsvc.LeaveCallResult
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.AlreadyLeft {
		t.Fatalf("already_left = true, want false")
	}
}

func TestCallCancelHTTPReturnsJSONBody(t *testing.T) {
	userID := uuid.New()
	workspaceID := uuid.New()
	callID := uuid.New()
	router, _ := newCallInteractionHTTPRouter(workspaceID, callID, userID, entity.CallStatusRinging, true)

	res := performCallInteractionRequest(router, http.MethodPost, workspaceID, callID, "/cancel", "", true)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", res.Code, res.Body.String())
	}

	var body callsvc.CancelCallResult
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Ended {
		t.Fatalf("ended = false, want true")
	}
}

func newCallInteractionHTTPRouter(workspaceID, callID, userID uuid.UUID, status entity.CallStatus, includeParticipant bool) (http.Handler, *httpCallRepo) {
	return newCallInteractionHTTPRouterWithMediaConfig(workspaceID, callID, userID, status, includeParticipant, callsvc.MediaConfig{})
}

func newCallInteractionHTTPRouterWithMediaConfig(workspaceID, callID, userID uuid.UUID, status entity.CallStatus, includeParticipant bool, media callsvc.MediaConfig) (http.Handler, *httpCallRepo) {
	calls := &httpCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: status, CreatedBy: userID},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{},
	}
	if includeParticipant {
		calls.participants[[2]uuid.UUID{callID, userID}] = &entity.CallParticipant{
			ID:     uuid.New(),
			CallID: callID,
			UserID: userID,
			Role:   entity.CallRoleHost,
			Status: entity.ParticipantStatusConnected,
		}
	}

	workspaces := &httpWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	svc := callsvc.NewService(calls, httpBreakoutRepo{}, httpChannelRepo{}, workspaces, httpNoopPublisher{}, nil, media, nil, nil)
	router := NewRouter(RouterDeps{
		Auth:             &AuthHandler{},
		Account:          &AccountHandler{},
		Channels:         &ChannelHandler{},
		Messages:         &MessageHandler{},
		Calls:            NewCallHandler(svc),
		Breakout:         &BreakoutHandler{},
		Files:            &FileHandler{},
		Presence:         &PresenceHandler{},
		Recordings:       &RecordingHandler{},
		Notifications:    &NotificationHandler{},
		Search:           &SearchHandler{},
		Admin:            &AdminHandler{},
		Guests:           &GuestHandler{},
		WS:               &wshandler.Handler{},
		Validator:        fakeTokenValidator{userID: userID},
		PersonalResolver: fakePersonalResolver{workspaceID: workspaceID},
	})
	return router, calls
}

func performCallInteractionRequest(router http.Handler, method string, workspaceID, callID uuid.UUID, suffix, body string, authenticated bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/v1/workspaces/"+workspaceID.String()+"/calls/"+callID.String()+suffix, strings.NewReader(body))
	if authenticated {
		req.Header.Set("Authorization", "Bearer test-token")
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	return res
}

type httpNoopPublisher struct{}

func (httpNoopPublisher) Publish(context.Context, string, []byte) error { return nil }

type httpWorkspaceRepo struct {
	members map[[2]uuid.UUID]*entity.WorkspaceMember
}

func (r *httpWorkspaceRepo) Create(context.Context, *entity.Workspace) error { return nil }
func (r *httpWorkspaceRepo) GetByID(context.Context, uuid.UUID) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}
func (r *httpWorkspaceRepo) GetBySlug(context.Context, string) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}
func (r *httpWorkspaceRepo) ListByUser(context.Context, uuid.UUID) ([]entity.Workspace, error) {
	return nil, nil
}
func (r *httpWorkspaceRepo) Update(context.Context, *entity.Workspace) error { return nil }
func (r *httpWorkspaceRepo) AddMember(context.Context, *entity.WorkspaceMember) error {
	return nil
}
func (r *httpWorkspaceRepo) GetMember(_ context.Context, workspaceID, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	if member := r.members[[2]uuid.UUID{workspaceID, userID}]; member != nil {
		return member, nil
	}
	return nil, cerrors.NotFound("workspace member not found")
}
func (r *httpWorkspaceRepo) ListMembers(context.Context, uuid.UUID, pagination.Params) ([]entity.WorkspaceMember, error) {
	return nil, nil
}
func (r *httpWorkspaceRepo) UpdateMemberRole(context.Context, uuid.UUID, uuid.UUID, entity.WorkspaceRole) error {
	return nil
}
func (r *httpWorkspaceRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

type httpChannelRepo struct{}

func (httpChannelRepo) Create(context.Context, *entity.Channel) error { return nil }
func (httpChannelRepo) GetByID(context.Context, uuid.UUID) (*entity.Channel, error) {
	return nil, cerrors.NotFound("channel not found")
}
func (httpChannelRepo) ListByWorkspace(context.Context, uuid.UUID, pagination.Params) ([]entity.Channel, error) {
	return nil, nil
}
func (httpChannelRepo) ListByUser(context.Context, uuid.UUID, uuid.UUID) ([]entity.Channel, error) {
	return nil, nil
}
func (httpChannelRepo) ListArchivedByUser(context.Context, uuid.UUID, uuid.UUID) ([]entity.ArchivedChannelInfo, error) {
	return nil, nil
}
func (httpChannelRepo) Update(context.Context, *entity.Channel) error { return nil }
func (httpChannelRepo) Archive(context.Context, uuid.UUID) error      { return nil }
func (httpChannelRepo) AddMember(context.Context, *entity.ChannelMember) error {
	return nil
}
func (httpChannelRepo) GetMember(context.Context, uuid.UUID, uuid.UUID) (*entity.ChannelMember, error) {
	return nil, cerrors.NotFound("channel member not found")
}
func (httpChannelRepo) ListMembers(context.Context, uuid.UUID) ([]entity.ChannelMember, error) {
	return nil, nil
}
func (httpChannelRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (httpChannelRepo) UpdateLastRead(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (httpChannelRepo) GetDMChannel(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*entity.Channel, error) {
	return nil, cerrors.NotFound("dm channel not found")
}

type httpCallRepo struct {
	calls        map[uuid.UUID]*entity.Call
	participants map[[2]uuid.UUID]*entity.CallParticipant
}

func (r *httpCallRepo) Create(_ context.Context, call *entity.Call) error {
	r.calls[call.ID] = call
	return nil
}
func (r *httpCallRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Call, error) {
	if call := r.calls[id]; call != nil {
		return call, nil
	}
	return nil, cerrors.NotFound("call not found")
}
func (r *httpCallRepo) ListActiveByWorkspace(context.Context, uuid.UUID) ([]entity.Call, error) {
	return nil, nil
}
func (r *httpCallRepo) UpdateStatus(context.Context, uuid.UUID, entity.CallStatus) error {
	return nil
}
func (r *httpCallRepo) End(context.Context, uuid.UUID) error { return nil }
func (r *httpCallRepo) EndWithReason(_ context.Context, id uuid.UUID, reason entity.CallEndReason) error {
	_, err := r.EndWithReasonIfNotEnded(context.Background(), id, reason)
	return err
}
func (r *httpCallRepo) EndWithReasonIfNotEnded(_ context.Context, id uuid.UUID, reason entity.CallEndReason) (bool, error) {
	call := r.calls[id]
	if call == nil {
		return false, cerrors.NotFound("call not found")
	}
	if call.Status == entity.CallStatusEnded {
		return false, nil
	}
	now := time.Now().UTC()
	call.Status = entity.CallStatusEnded
	call.EndedAt = &now
	call.EndReason = reason
	return true, nil
}
func (r *httpCallRepo) CancelRingingWithReason(_ context.Context, id uuid.UUID, reason entity.CallEndReason) (bool, error) {
	call := r.calls[id]
	if call == nil {
		return false, cerrors.NotFound("call not found")
	}
	if call.Status != entity.CallStatusRinging {
		return false, nil
	}
	now := time.Now().UTC()
	call.Status = entity.CallStatusEnded
	call.EndedAt = &now
	call.EndReason = reason
	return true, nil
}
func (r *httpCallRepo) ActivateRinging(_ context.Context, id uuid.UUID) (bool, error) {
	call := r.calls[id]
	if call == nil {
		return false, cerrors.NotFound("call not found")
	}
	if call.Status != entity.CallStatusRinging {
		return false, nil
	}
	call.Status = entity.CallStatusActive
	return true, nil
}
func (r *httpCallRepo) AddParticipant(_ context.Context, p *entity.CallParticipant) error {
	r.participants[[2]uuid.UUID{p.CallID, p.UserID}] = p
	return nil
}
func (r *httpCallRepo) AddParticipantIfCapacity(ctx context.Context, p *entity.CallParticipant, _ int) error {
	return r.AddParticipant(ctx, p)
}
func (r *httpCallRepo) GetParticipant(_ context.Context, callID, userID uuid.UUID) (*entity.CallParticipant, error) {
	if p := r.participants[[2]uuid.UUID{callID, userID}]; p != nil {
		return p, nil
	}
	return nil, cerrors.NotFound("call participant not found")
}
func (r *httpCallRepo) ListParticipants(_ context.Context, callID uuid.UUID) ([]entity.CallParticipant, error) {
	var participants []entity.CallParticipant
	for key, p := range r.participants {
		if key[0] == callID {
			participants = append(participants, *p)
		}
	}
	return participants, nil
}
func (r *httpCallRepo) UpdateParticipantStatus(ctx context.Context, id uuid.UUID, status entity.ParticipantStatus) error {
	return r.UpdateParticipantStatusWithReason(ctx, id, status, "")
}
func (r *httpCallRepo) UpdateParticipantStatusWithReason(_ context.Context, id uuid.UUID, status entity.ParticipantStatus, reason entity.ParticipantLeftReason) error {
	for _, p := range r.participants {
		if p.ID == id {
			now := time.Now().UTC()
			p.Status = status
			if status == entity.ParticipantStatusDisconnected {
				p.LeftAt = &now
				p.LeftReason = reason
			}
			if status == entity.ParticipantStatusConnected {
				p.JoinedAt = &now
				p.LeftAt = nil
				p.LeftReason = ""
			}
			return nil
		}
	}
	return cerrors.NotFound("call participant not found")
}
func (r *httpCallRepo) UpdateParticipantRole(context.Context, uuid.UUID, entity.CallRole) error {
	return nil
}
func (r *httpCallRepo) UpdateParticipantMedia(context.Context, uuid.UUID, bool, bool, bool) error {
	return nil
}
func (r *httpCallRepo) RemoveParticipant(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type httpBreakoutRepo struct{}

func (httpBreakoutRepo) Create(context.Context, *entity.BreakoutRoom) error { return nil }
func (httpBreakoutRepo) GetByID(context.Context, uuid.UUID) (*entity.BreakoutRoom, error) {
	return nil, cerrors.NotFound("breakout room not found")
}
func (httpBreakoutRepo) ListByCall(context.Context, uuid.UUID) ([]entity.BreakoutRoom, error) {
	return nil, nil
}
func (httpBreakoutRepo) Close(context.Context, uuid.UUID) error { return nil }
func (httpBreakoutRepo) CloseAllByCall(context.Context, uuid.UUID) error {
	return nil
}
func (httpBreakoutRepo) AssignParticipant(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return nil
}
func (httpBreakoutRepo) UnassignParticipant(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (httpBreakoutRepo) UnassignAllByRoom(context.Context, uuid.UUID) error {
	return nil
}
func (httpBreakoutRepo) ListParticipants(context.Context, uuid.UUID) ([]entity.CallParticipant, error) {
	return nil, nil
}
