package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	callsvc "aloqa/internal/service/call"
)

func TestBreakoutListHTTPReturnsEmptyArray(t *testing.T) {
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()

	handler := newBreakoutHTTPHandler(workspaceID, callID, userID, &breakoutHTTPRepo{})

	res := httptest.NewRecorder()
	req := breakoutHTTPRequest(http.MethodGet, workspaceID, userID, callID, uuid.Nil)
	handler.List(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", res.Code, res.Body.String())
	}
	if got := strings.TrimSpace(res.Body.String()); got != "[]" {
		t.Fatalf("body = %q, want []", got)
	}
}

func TestBreakoutJoinReturnHTTPUnconfiguredLiveKitReturnsUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method func(*BreakoutHandler, http.ResponseWriter, *http.Request)
	}{
		{name: "join", method: (*BreakoutHandler).Join},
		{name: "return", method: (*BreakoutHandler).Return},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspaceID := uuid.New()
			callID := uuid.New()
			userID := uuid.New()
			roomID := uuid.New()

			repo := &breakoutHTTPRepo{rooms: map[uuid.UUID]*entity.BreakoutRoom{
				roomID: {ID: roomID, CallID: callID, Name: "Room A", Status: entity.BreakoutRoomStatusActive},
			}}
			handler := newBreakoutHTTPHandler(workspaceID, callID, userID, repo)

			res := httptest.NewRecorder()
			req := breakoutHTTPRequest(http.MethodPost, workspaceID, userID, callID, roomID)
			tc.method(handler, res, req)

			if res.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503, body=%s", res.Code, res.Body.String())
			}
			var body errorBody
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Code != string(cerrors.CodeUnavailable) {
				t.Fatalf("error code = %q, want %q", body.Error.Code, cerrors.CodeUnavailable)
			}
		})
	}
}

func newBreakoutHTTPHandler(workspaceID, callID, userID uuid.UUID, repo *breakoutHTTPRepo) *BreakoutHandler {
	breakoutRoomID := uuid.Nil
	if repo != nil {
		for id := range repo.rooms {
			breakoutRoomID = id
			break
		}
	}
	participant := &entity.CallParticipant{
		ID:     uuid.New(),
		CallID: callID,
		UserID: userID,
		Role:   entity.CallRoleHost,
		Status: entity.ParticipantStatusConnected,
	}
	if breakoutRoomID != uuid.Nil {
		participant.BreakoutRoomID = &breakoutRoomID
	}
	calls := &httpCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeMeeting,
				Status:      entity.CallStatusActive,
				CreatedBy:   userID,
				Settings:    entity.CallSettings{BreakoutRooms: true},
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: participant,
		},
	}
	workspaces := &httpWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	svc := callsvc.NewService(calls, repo, httpChannelRepo{}, workspaces, httpNoopPublisher{}, nil, callsvc.MediaConfig{}, nil, nil, nil)
	return NewBreakoutHandler(svc)
}

func breakoutHTTPRequest(method string, workspaceID, userID, callID, breakoutRoomID uuid.UUID) *http.Request {
	req := httptest.NewRequest(method, "/", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("callID", callID.String())
	if breakoutRoomID != uuid.Nil {
		rctx.URLParams.Add("breakoutRoomID", breakoutRoomID.String())
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, middleware.WorkspaceIDKey, workspaceID)
	ctx = context.WithValue(ctx, middleware.UserIDKey, userID)
	return req.WithContext(ctx)
}

type breakoutHTTPRepo struct {
	rooms map[uuid.UUID]*entity.BreakoutRoom
}

func (r *breakoutHTTPRepo) Create(_ context.Context, room *entity.BreakoutRoom) error {
	if r.rooms == nil {
		r.rooms = map[uuid.UUID]*entity.BreakoutRoom{}
	}
	r.rooms[room.ID] = room
	return nil
}

func (r *breakoutHTTPRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.BreakoutRoom, error) {
	if r != nil {
		if room := r.rooms[id]; room != nil {
			return room, nil
		}
	}
	return nil, cerrors.NotFound("breakout room not found")
}

func (r *breakoutHTTPRepo) ListByCall(_ context.Context, callID uuid.UUID) ([]entity.BreakoutRoom, error) {
	rooms := make([]entity.BreakoutRoom, 0)
	if r == nil {
		return rooms, nil
	}
	for _, room := range r.rooms {
		if room.CallID == callID {
			rooms = append(rooms, *room)
		}
	}
	return rooms, nil
}

func (r *breakoutHTTPRepo) ListCallsWithExpiredActiveBreakouts(context.Context, time.Time, int) ([]uuid.UUID, error) {
	return nil, nil
}

func (r *breakoutHTTPRepo) Close(context.Context, uuid.UUID) error { return nil }
func (r *breakoutHTTPRepo) CloseAllByCall(context.Context, uuid.UUID) error {
	return nil
}
func (r *breakoutHTTPRepo) AssignParticipant(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *breakoutHTTPRepo) UnassignParticipant(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *breakoutHTTPRepo) UnassignAllByRoom(context.Context, uuid.UUID) error {
	return nil
}
func (r *breakoutHTTPRepo) ListParticipants(context.Context, uuid.UUID) ([]entity.CallParticipant, error) {
	return make([]entity.CallParticipant, 0), nil
}
