package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
	callsvc "aloqa/internal/service/call"
)

func TestCallMessagePostCreated(t *testing.T) {
	f := newCallMessageHTTPFixture()
	res := f.serve(http.MethodPost, "/calls/"+f.callID.String()+"/messages", `{"body":" hello "}`)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", res.Code, res.Body.String())
	}
	var msg entity.CallMessage
	if err := json.Unmarshal(res.Body.Bytes(), &msg); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if msg.Body != " hello " || msg.CallID != f.callID || msg.SenderID != f.userID {
		t.Fatalf("message = %+v, want verbatim body", msg)
	}
}

func TestCallMessagePostBadRequests(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{"body":`},
		{name: "empty body", body: `{"body":"   "}`},
		{name: "oversize body", body: `{"body":"` + strings.Repeat("a", 2001) + `"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCallMessageHTTPFixture()
			res := f.serve(http.MethodPost, "/calls/"+f.callID.String()+"/messages", tt.body)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", res.Code, res.Body.String())
			}
		})
	}
}

func TestCallMessageUnauthenticated(t *testing.T) {
	router := NewRouter(RouterDeps{Calls: &CallHandler{}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+uuid.NewString()+"/calls/"+uuid.NewString()+"/messages", strings.NewReader(`{"body":"hello"}`))
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", res.Code, res.Body.String())
	}
}

func TestCallMessagePostForbiddenWhenNotInCall(t *testing.T) {
	f := newCallMessageHTTPFixture()
	delete(f.calls.participants, [2]uuid.UUID{f.callID, f.userID})

	res := f.serve(http.MethodPost, "/calls/"+f.callID.String()+"/messages", `{"body":"hello"}`)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", res.Code, res.Body.String())
	}
}

func TestCallMessageGetCursorPagination(t *testing.T) {
	f := newCallMessageHTTPFixture()
	for i := 1; i <= 5; i++ {
		msg := &entity.CallMessage{
			ID:       callMessageHTTPTestUUID(i),
			CallID:   f.callID,
			SenderID: f.userID,
			Body:     fmt.Sprintf("message-%d", i),
		}
		f.messages.messages[msg.ID] = msg
	}

	first := f.serve(http.MethodGet, "/calls/"+f.callID.String()+"/messages?limit=2", "")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200; body=%s", first.Code, first.Body.String())
	}
	var firstPage pagination.Page[entity.CallMessage]
	if err := json.Unmarshal(first.Body.Bytes(), &firstPage); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if !firstPage.HasMore || firstPage.NextCursor == "" || len(firstPage.Items) != 2 {
		t.Fatalf("first page = %+v, want 2 items with next cursor", firstPage)
	}
	if firstPage.Items[0].ID != callMessageHTTPTestUUID(5) || firstPage.Items[1].ID != callMessageHTTPTestUUID(4) {
		t.Fatalf("first page items = %+v, want newest first", firstPage.Items)
	}

	second := f.serve(http.MethodGet, "/calls/"+f.callID.String()+"/messages?limit=2&cursor="+firstPage.NextCursor, "")
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, want 200; body=%s", second.Code, second.Body.String())
	}
	var secondPage pagination.Page[entity.CallMessage]
	if err := json.Unmarshal(second.Body.Bytes(), &secondPage); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if !secondPage.HasMore || len(secondPage.Items) != 2 {
		t.Fatalf("second page = %+v, want 2 items with another page", secondPage)
	}
	if secondPage.Items[0].ID != callMessageHTTPTestUUID(3) || secondPage.Items[1].ID != callMessageHTTPTestUUID(2) {
		t.Fatalf("second page items = %+v, want cursor to round-trip", secondPage.Items)
	}
}

func TestCallMessageDeleteNoContent(t *testing.T) {
	f := newCallMessageHTTPFixture()
	msg := &entity.CallMessage{ID: uuid.New(), CallID: f.callID, SenderID: f.userID, Body: "delete me"}
	f.messages.messages[msg.ID] = msg

	res := f.serve(http.MethodDelete, "/calls/"+f.callID.String()+"/messages/"+msg.ID.String(), "")
	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", res.Code, res.Body.String())
	}
	if f.messages.messages[msg.ID].DeletedAt == nil {
		t.Fatalf("message was not soft-deleted")
	}
}

type callMessageHTTPFixture struct {
	workspaceID uuid.UUID
	callID      uuid.UUID
	userID      uuid.UUID
	calls       *fakeHTTPCallRepo
	messages    *fakeHTTPCallMessageRepo
	router      *chi.Mux
}

func newCallMessageHTTPFixture() callMessageHTTPFixture {
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()
	calls := &fakeHTTPCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive, Settings: entity.CallSettings{Chat: true}},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {ID: uuid.New(), CallID: callID, UserID: userID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
		},
	}
	workspaces := &fakeHTTPWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	messages := &fakeHTTPCallMessageRepo{messages: map[uuid.UUID]*entity.CallMessage{}}
	svc := callsvc.NewService(calls, fakeHTTPBreakoutRepo{}, fakeHTTPChannelRepo{}, workspaces, noopHTTPPublisher{}, nil, callsvc.MediaConfig{TokenSecret: []byte("01234567890123456789012345678901")}, nil, nil)
	svc.SetCallMessageRepo(messages)
	handler := NewCallHandler(svc)

	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), middleware.WorkspaceIDKey, workspaceID)
			ctx = context.WithValue(ctx, middleware.UserIDKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	router.Route("/calls/{callID}/messages", func(r chi.Router) {
		r.Post("/", handler.SendCallMessage)
		r.Get("/", handler.ListCallMessages)
		r.Delete("/{messageID}", handler.DeleteCallMessage)
	})

	return callMessageHTTPFixture{
		workspaceID: workspaceID,
		callID:      callID,
		userID:      userID,
		calls:       calls,
		messages:    messages,
		router:      router,
	}
}

func (f callMessageHTTPFixture) serve(method, path, body string) *httptest.ResponseRecorder {
	reader := strings.NewReader(body)
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	f.router.ServeHTTP(res, req)
	return res
}

type noopHTTPPublisher struct{}

func (noopHTTPPublisher) Publish(context.Context, string, []byte) error { return nil }

type fakeHTTPWorkspaceRepo struct {
	members map[[2]uuid.UUID]*entity.WorkspaceMember
}

func (r *fakeHTTPWorkspaceRepo) Create(context.Context, *entity.Workspace) error { return nil }
func (r *fakeHTTPWorkspaceRepo) GetByID(context.Context, uuid.UUID) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}
func (r *fakeHTTPWorkspaceRepo) GetBySlug(context.Context, string) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}
func (r *fakeHTTPWorkspaceRepo) ListByUser(context.Context, uuid.UUID) ([]entity.Workspace, error) {
	return nil, nil
}
func (r *fakeHTTPWorkspaceRepo) Update(context.Context, *entity.Workspace) error { return nil }
func (r *fakeHTTPWorkspaceRepo) AddMember(context.Context, *entity.WorkspaceMember) error {
	return nil
}
func (r *fakeHTTPWorkspaceRepo) GetMember(_ context.Context, workspaceID, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	if member := r.members[[2]uuid.UUID{workspaceID, userID}]; member != nil {
		return member, nil
	}
	return nil, cerrors.NotFound("workspace member not found")
}
func (r *fakeHTTPWorkspaceRepo) ListMembers(context.Context, uuid.UUID, pagination.Params) ([]entity.WorkspaceMember, error) {
	return nil, nil
}
func (r *fakeHTTPWorkspaceRepo) UpdateMemberRole(context.Context, uuid.UUID, uuid.UUID, entity.WorkspaceRole) error {
	return nil
}
func (r *fakeHTTPWorkspaceRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type fakeHTTPChannelRepo struct{}

func (fakeHTTPChannelRepo) Create(context.Context, *entity.Channel) error { return nil }
func (fakeHTTPChannelRepo) GetByID(context.Context, uuid.UUID) (*entity.Channel, error) {
	return nil, cerrors.NotFound("channel not found")
}
func (fakeHTTPChannelRepo) ListByWorkspace(context.Context, uuid.UUID, pagination.Params) ([]entity.Channel, error) {
	return nil, nil
}
func (fakeHTTPChannelRepo) ListByUser(context.Context, uuid.UUID, uuid.UUID) ([]entity.Channel, error) {
	return nil, nil
}
func (fakeHTTPChannelRepo) Update(context.Context, *entity.Channel) error          { return nil }
func (fakeHTTPChannelRepo) Archive(context.Context, uuid.UUID) error               { return nil }
func (fakeHTTPChannelRepo) AddMember(context.Context, *entity.ChannelMember) error { return nil }
func (fakeHTTPChannelRepo) GetMember(context.Context, uuid.UUID, uuid.UUID) (*entity.ChannelMember, error) {
	return nil, cerrors.NotFound("channel member not found")
}
func (fakeHTTPChannelRepo) ListMembers(context.Context, uuid.UUID) ([]entity.ChannelMember, error) {
	return nil, nil
}
func (fakeHTTPChannelRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (fakeHTTPChannelRepo) UpdateLastRead(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (fakeHTTPChannelRepo) GetDMChannel(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*entity.Channel, error) {
	return nil, cerrors.NotFound("dm channel not found")
}

type fakeHTTPCallRepo struct {
	calls        map[uuid.UUID]*entity.Call
	participants map[[2]uuid.UUID]*entity.CallParticipant
}

func (r *fakeHTTPCallRepo) Create(_ context.Context, call *entity.Call) error {
	r.calls[call.ID] = call
	return nil
}
func (r *fakeHTTPCallRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Call, error) {
	if call := r.calls[id]; call != nil {
		return call, nil
	}
	return nil, cerrors.NotFound("call not found")
}
func (r *fakeHTTPCallRepo) ListActiveByWorkspace(context.Context, uuid.UUID) ([]entity.Call, error) {
	return nil, nil
}
func (r *fakeHTTPCallRepo) UpdateStatus(context.Context, uuid.UUID, entity.CallStatus) error {
	return nil
}
func (r *fakeHTTPCallRepo) End(context.Context, uuid.UUID) error { return nil }
func (r *fakeHTTPCallRepo) AddParticipant(_ context.Context, p *entity.CallParticipant) error {
	r.participants[[2]uuid.UUID{p.CallID, p.UserID}] = p
	return nil
}
func (r *fakeHTTPCallRepo) AddParticipantIfCapacity(ctx context.Context, p *entity.CallParticipant, _ int) error {
	return r.AddParticipant(ctx, p)
}
func (r *fakeHTTPCallRepo) GetParticipant(_ context.Context, callID, userID uuid.UUID) (*entity.CallParticipant, error) {
	if p := r.participants[[2]uuid.UUID{callID, userID}]; p != nil {
		return p, nil
	}
	return nil, cerrors.NotFound("call participant not found")
}
func (r *fakeHTTPCallRepo) ListParticipants(_ context.Context, callID uuid.UUID) ([]entity.CallParticipant, error) {
	var participants []entity.CallParticipant
	for key, p := range r.participants {
		if key[0] == callID {
			participants = append(participants, *p)
		}
	}
	return participants, nil
}
func (r *fakeHTTPCallRepo) UpdateParticipantStatus(context.Context, uuid.UUID, entity.ParticipantStatus) error {
	return nil
}
func (r *fakeHTTPCallRepo) UpdateParticipantRole(context.Context, uuid.UUID, entity.CallRole) error {
	return nil
}
func (r *fakeHTTPCallRepo) UpdateParticipantMedia(context.Context, uuid.UUID, bool, bool, bool) error {
	return nil
}
func (r *fakeHTTPCallRepo) RemoveParticipant(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type fakeHTTPCallMessageRepo struct {
	messages map[uuid.UUID]*entity.CallMessage
}

func (r *fakeHTTPCallMessageRepo) Create(_ context.Context, msg *entity.CallMessage) error {
	copy := *msg
	r.messages[msg.ID] = &copy
	return nil
}

func (r *fakeHTTPCallMessageRepo) ListByCall(_ context.Context, callID uuid.UUID, p pagination.Params) ([]entity.CallMessage, error) {
	var items []entity.CallMessage
	for _, msg := range r.messages {
		if msg.CallID != callID || msg.DeletedAt != nil {
			continue
		}
		if p.Cursor != uuid.Nil && msg.ID.String() >= p.Cursor.String() {
			continue
		}
		items = append(items, *msg)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].ID.String() > items[j].ID.String()
	})
	if p.Limit <= 0 {
		p.Limit = pagination.DefaultLimit
	}
	if len(items) > p.Limit+1 {
		items = items[:p.Limit+1]
	}
	return items, nil
}

func (r *fakeHTTPCallMessageRepo) SoftDelete(_ context.Context, id, callID uuid.UUID) error {
	if msg := r.messages[id]; msg != nil && msg.CallID == callID {
		now := time.Now().UTC()
		msg.DeletedAt = &now
	}
	return nil
}

func (r *fakeHTTPCallMessageRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.CallMessage, error) {
	if msg := r.messages[id]; msg != nil {
		copy := *msg
		return &copy, nil
	}
	return nil, cerrors.NotFound("call message not found")
}

type fakeHTTPBreakoutRepo struct{}

func (fakeHTTPBreakoutRepo) Create(context.Context, *entity.BreakoutRoom) error { return nil }
func (fakeHTTPBreakoutRepo) GetByID(context.Context, uuid.UUID) (*entity.BreakoutRoom, error) {
	return nil, cerrors.NotFound("breakout room not found")
}
func (fakeHTTPBreakoutRepo) ListByCall(context.Context, uuid.UUID) ([]entity.BreakoutRoom, error) {
	return nil, nil
}
func (fakeHTTPBreakoutRepo) Close(context.Context, uuid.UUID) error { return nil }
func (fakeHTTPBreakoutRepo) CloseAllByCall(context.Context, uuid.UUID) error {
	return nil
}
func (fakeHTTPBreakoutRepo) AssignParticipant(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return nil
}
func (fakeHTTPBreakoutRepo) UnassignParticipant(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (fakeHTTPBreakoutRepo) UnassignAllByRoom(context.Context, uuid.UUID) error { return nil }
func (fakeHTTPBreakoutRepo) ListParticipants(context.Context, uuid.UUID) ([]entity.CallParticipant, error) {
	return nil, nil
}

func callMessageHTTPTestUUID(n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012d", n))
}
