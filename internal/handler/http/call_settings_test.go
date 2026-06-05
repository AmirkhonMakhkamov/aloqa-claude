package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/middleware"
	callsvc "aloqa/internal/service/call"
)

func TestCallUpdateSettingsHTTPDecodesAndReturnsUpdatedCall(t *testing.T) {
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	handler, calls := newCallSettingsHTTPHandler(workspaceID, callID, hostID)

	res := httptest.NewRecorder()
	req := callSettingsHTTPRequest(workspaceID, hostID, callID, `{"breakout_rooms":true}`)
	handler.UpdateSettings(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", res.Code, res.Body.String())
	}
	var updated entity.Call
	if err := json.Unmarshal(res.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !updated.Settings.BreakoutRooms {
		t.Fatalf("response breakout_rooms = false, want true")
	}
	if !calls.calls[callID].Settings.BreakoutRooms {
		t.Fatalf("stored breakout_rooms = false, want true")
	}
}

func TestCallUpdateSettingsHTTPRejectsUnknownFields(t *testing.T) {
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	handler, calls := newCallSettingsHTTPHandler(workspaceID, callID, hostID)

	res := httptest.NewRecorder()
	req := callSettingsHTTPRequest(workspaceID, hostID, callID, `{"breakout_rooms":true,"unknown":false}`)
	handler.UpdateSettings(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", res.Code, res.Body.String())
	}
	if calls.calls[callID].Settings.BreakoutRooms {
		t.Fatalf("stored breakout_rooms changed on invalid request")
	}
}

func TestCallUpdateSettingsHTTPDecodesEntryModeAndMuteOnJoin(t *testing.T) {
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	handler, calls := newCallSettingsHTTPHandler(workspaceID, callID, hostID)

	res := httptest.NewRecorder()
	req := callSettingsHTTPRequest(workspaceID, hostID, callID, `{"entry_mode":"manual_admit","mute_on_join":true}`)
	handler.UpdateSettings(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", res.Code, res.Body.String())
	}
	stored := calls.calls[callID].Settings
	if stored.EntryMode != entity.EntryModeManualAdmit {
		t.Fatalf("stored entry_mode = %s, want manual_admit", stored.EntryMode)
	}
	if !stored.WaitingRoom {
		t.Fatalf("stored waiting_room = false, want true (derived from manual_admit)")
	}
	if !stored.MuteOnJoin {
		t.Fatalf("stored mute_on_join = false, want true")
	}
}

func newCallSettingsHTTPHandler(workspaceID, callID, hostID uuid.UUID) (*CallHandler, *httpCallRepo) {
	calls := &httpCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeMeeting,
				Status:      entity.CallStatusActive,
				CreatedBy:   hostID,
				Settings:    entity.CallSettings{BreakoutRooms: false},
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: {
				ID:     uuid.New(),
				CallID: callID,
				UserID: hostID,
				Role:   entity.CallRoleHost,
				Status: entity.ParticipantStatusConnected,
			},
		},
	}
	workspaces := &httpWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, hostID}: {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
	}}
	svc := callsvc.NewService(calls, httpBreakoutRepo{}, httpChannelRepo{}, workspaces, httpNoopPublisher{}, nil, callsvc.MediaConfig{}, nil, nil)
	return NewCallHandler(svc, nil), calls
}

func callSettingsHTTPRequest(workspaceID, userID, callID uuid.UUID, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("callID", callID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, middleware.WorkspaceIDKey, workspaceID)
	ctx = context.WithValue(ctx, middleware.UserIDKey, userID)
	return req.WithContext(ctx)
}
