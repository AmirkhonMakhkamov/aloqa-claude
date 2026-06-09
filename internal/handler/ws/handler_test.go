package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
	platformws "aloqa/internal/platform/ws"
	calldomain "aloqa/internal/service/call"
	chatdomain "aloqa/internal/service/chat"
)

func TestCanSubscribeAuthorizesKnownRoomTypes(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	privateChannelID := uuid.New()
	userID := uuid.New()
	otherUserID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			privateChannelID: {ID: privateChannelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePrivate},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	chatSvc := chatdomain.NewService(channels, nil, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)
	handler := NewHandler(nil, chatSvc, nil, nil, nil, 0)
	client := &platformws.Client{ID: uuid.New(), UserID: userID}

	if handler.canSubscribe(ctx, client, "random-room") {
		t.Fatalf("random room subscription was allowed")
	}
	if !handler.canSubscribe(ctx, client, "aloqa.ws."+workspaceID.String()) {
		t.Fatalf("workspace room subscription was denied for a workspace member")
	}
	if handler.canSubscribe(ctx, client, "aloqa.signal."+otherUserID.String()) {
		t.Fatalf("signal room subscription for another user was allowed")
	}
	if !handler.canSubscribe(ctx, client, "aloqa.signal."+userID.String()) {
		t.Fatalf("own signal room subscription was denied")
	}
	if !handler.canSubscribe(ctx, client, "aloqa.ws."+userID.String()+".events") {
		t.Fatalf("own user events subscription was denied")
	}
	if handler.canSubscribe(ctx, client, "aloqa.ws."+otherUserID.String()+".events") {
		t.Fatalf("user events subscription for another user was allowed")
	}
	if handler.canSubscribe(ctx, client, "channel:"+privateChannelID.String()) {
		t.Fatalf("private channel subscription without channel membership was allowed")
	}

	channels.members[[2]uuid.UUID{privateChannelID, userID}] = &entity.ChannelMember{ChannelID: privateChannelID, UserID: userID}
	if !handler.canSubscribe(ctx, client, "channel:"+privateChannelID.String()) {
		t.Fatalf("private channel subscription was denied for a channel member")
	}
}

func TestCanSubscribeAuthorizesSavedChannelOnlyForOwnerMember(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	savedChannelID := uuid.New()
	userID := uuid.New()
	otherUserID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			savedChannelID: {
				ID:          savedChannelID,
				WorkspaceID: &workspaceID,
				Type:        entity.ChannelTypeSaved,
				OwnerUserID: &userID,
			},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{savedChannelID, userID}: {ChannelID: savedChannelID, UserID: userID},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}:      {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
		{workspaceID, otherUserID}: {WorkspaceID: workspaceID, UserID: otherUserID, Role: entity.WorkspaceRoleMember},
	}}
	chatSvc := chatdomain.NewService(channels, nil, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)
	handler := NewHandler(nil, chatSvc, nil, nil, nil, 0)

	if !handler.canSubscribe(ctx, &platformws.Client{ID: uuid.New(), UserID: userID}, "aloqa.chat."+savedChannelID.String()) {
		t.Fatalf("saved channel subscription was denied for owner member")
	}
	if handler.canSubscribe(ctx, &platformws.Client{ID: uuid.New(), UserID: otherUserID}, "aloqa.chat."+savedChannelID.String()) {
		t.Fatalf("saved channel subscription was allowed for a different user")
	}
}

func TestCanSubscribeFailsClosedWithoutChatService(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	handler := NewHandler(nil, nil, nil, nil, nil, 0)
	client := &platformws.Client{ID: uuid.New(), UserID: userID}

	if handler.canSubscribe(ctx, client, "aloqa.ws."+workspaceID.String()) {
		t.Fatalf("workspace subscription was allowed without a chat service")
	}
	if handler.canSubscribe(ctx, client, "channel:"+uuid.New().String()) {
		t.Fatalf("channel subscription was allowed without a chat service")
	}
	if !handler.canSubscribe(ctx, client, "aloqa.signal."+userID.String()) {
		t.Fatalf("own signal subscription should not require chat service")
	}
	if !handler.canSubscribe(ctx, client, "aloqa.ws."+userID.String()+".events") {
		t.Fatalf("own user events subscription should not require chat service")
	}
}

func TestServeHTTPReturnsUnavailableWhenDependenciesMissing(t *testing.T) {
	handler := NewHandler(nil, nil, nil, nil, nil, 0)
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	userID := uuid.New()
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserIDKey, userID))
	req = req.WithContext(context.WithValue(req.Context(), middleware.SessionIDKey, "session-test"))
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected %d, got %d", http.StatusServiceUnavailable, rr.Code)
	}
}

func TestRestoreSubscriptionsReplaysMissedEvents(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	room := "aloqa.ws." + workspaceID.String()

	channels := &fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{}, members: map[[2]uuid.UUID]*entity.ChannelMember{}}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	chatSvc := chatdomain.NewService(channels, nil, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)

	state := &fakeStateStore{
		rooms: map[string][]string{"resume-key": {room}},
		seq:   map[string]int64{"resume-key:" + room: 10},
	}
	replayer := fakeRoomReplayer{events: []event.Event{{
		ID:               uuid.New(),
		Version:          event.CurrentVersion,
		Sequence:         11,
		Type:             event.TypeMessageCreated,
		Subject:          room,
		WorkspaceID:      workspaceID,
		UserID:           userID,
		DeliverySemantic: event.DeliveryAtLeastOnce,
		Replayable:       true,
		Timestamp:        time.Now().UTC(),
		Payload:          map[string]any{"message": "replayed"},
	}}}
	hub := platformws.NewHub(state)
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	go hub.Run(hubCtx)

	client := &platformws.Client{ID: uuid.New(), UserID: userID, SessionID: "session-1", ResumeKey: "resume-key", Send: make(chan []byte, 8)}
	hub.Register(client)
	defer hub.Unregister(client)

	handler := NewHandler(hub, chatSvc, nil, state, replayer, 10)
	handler.restoreSubscriptions(ctx, client)

	select {
	case data := <-client.Send:
		var evt event.Event
		if err := json.Unmarshal(data, &evt); err != nil {
			t.Fatalf("failed to unmarshal replayed event: %v", err)
		}
		if evt.Sequence != 11 {
			t.Fatalf("replayed sequence = %d, want 11", evt.Sequence)
		}
	case <-time.After(time.Second):
		t.Fatalf("expected replayed event to be delivered")
	}
}

func newCallTypingHandler(t *testing.T, workspaceID, callID, userID uuid.UUID, member bool) (*Handler, *platformws.Client) {
	t.Helper()

	calls := &fakeCallRepo{calls: map[uuid.UUID]*entity.Call{
		callID: {ID: callID, WorkspaceID: workspaceID, ChannelID: nil},
	}}
	workspaceMembers := map[[2]uuid.UUID]*entity.WorkspaceMember{}
	if member {
		workspaceMembers[[2]uuid.UUID{workspaceID, userID}] = &entity.WorkspaceMember{
			WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember,
		}
	}
	workspaces := &fakeWorkspaceRepo{members: workspaceMembers}
	callSvc := calldomain.NewService(calls, nil, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, calldomain.MediaConfig{}, nil, nil)
	chatSvc := chatdomain.NewService(
		&fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{}, members: map[[2]uuid.UUID]*entity.ChannelMember{}},
		nil, workspaces, nil, noopPublisher{}, nil, nil, nil, nil,
	)

	state := &fakeStateStore{rooms: map[string][]string{}, seq: map[string]int64{}}
	hub := platformws.NewHub(state)
	hubCtx, hubCancel := context.WithCancel(context.Background())
	go hub.Run(hubCtx)
	t.Cleanup(hubCancel)

	client := &platformws.Client{
		ID: uuid.New(), UserID: userID, SessionID: "session-1", ResumeKey: "resume-key",
		Send: make(chan []byte, 8),
	}
	hub.Register(client)
	hub.Subscribe(client.ID.String(), "aloqa.ws."+workspaceID.String())

	handler := NewHandler(hub, chatSvc, callSvc, nil, nil, 0)
	return handler, client
}

func TestHandleCallTypingBroadcastsWhenAccessGranted(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()

	handler, client := newCallTypingHandler(t, workspaceID, callID, userID, true)

	payload := json.RawMessage(`{"workspace_id":"` + workspaceID.String() + `","call_id":"` + callID.String() + `"}`)
	handler.handleMessage(ctx, client, ClientMessage{Type: "call_typing", Payload: payload})

	select {
	case data := <-client.Send:
		var msg ServerMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("failed to unmarshal broadcast: %v", err)
		}
		if msg.Type != string(event.TypeCallTypingStarted) {
			t.Fatalf("broadcast type = %q, want %q", msg.Type, event.TypeCallTypingStarted)
		}
		raw, err := json.Marshal(msg.Payload)
		if err != nil {
			t.Fatalf("failed to re-marshal payload: %v", err)
		}
		var p event.CallTypingPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("failed to unmarshal call typing payload: %v", err)
		}
		if p.CallID != callID {
			t.Fatalf("payload call_id = %s, want %s", p.CallID, callID)
		}
		if p.UserID != userID {
			t.Fatalf("payload user_id = %s, want %s", p.UserID, userID)
		}
	case <-time.After(time.Second):
		t.Fatalf("expected call.typing.started broadcast")
	}
}

func TestHandleCallTypingDeniedWhenNoAccess(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()

	handler, client := newCallTypingHandler(t, workspaceID, callID, userID, false)

	payload := json.RawMessage(`{"workspace_id":"` + workspaceID.String() + `","call_id":"` + callID.String() + `"}`)
	handler.handleMessage(ctx, client, ClientMessage{Type: "call_typing", Payload: payload})

	select {
	case data := <-client.Send:
		var msg ServerMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("failed to unmarshal client message: %v", err)
		}
		if msg.Type != "error" {
			t.Fatalf("expected error message when access denied, got type %q", msg.Type)
		}
	case <-time.After(time.Second):
		t.Fatalf("expected an error message to be sent when access is denied")
	}
}

type noopPublisher struct{}

func (noopPublisher) Publish(context.Context, string, []byte) error { return nil }

type fakeCallRepo struct {
	calls map[uuid.UUID]*entity.Call
}

func (r *fakeCallRepo) Create(context.Context, *entity.Call) error { return nil }
func (r *fakeCallRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Call, error) {
	if call := r.calls[id]; call != nil {
		return call, nil
	}
	return nil, cerrors.NotFound("call not found")
}
func (r *fakeCallRepo) ListActiveByWorkspace(context.Context, uuid.UUID) ([]entity.Call, error) {
	return nil, nil
}
func (r *fakeCallRepo) UpdateStatus(context.Context, uuid.UUID, entity.CallStatus) error { return nil }
func (r *fakeCallRepo) UpdateSettings(context.Context, uuid.UUID, entity.CallSettings) error {
	return nil
}
func (r *fakeCallRepo) End(context.Context, uuid.UUID) error                          { return nil }
func (r *fakeCallRepo) AddParticipant(context.Context, *entity.CallParticipant) error { return nil }
func (r *fakeCallRepo) AddParticipantIfCapacity(context.Context, *entity.CallParticipant, int) error {
	return nil
}
func (r *fakeCallRepo) GetParticipant(context.Context, uuid.UUID, uuid.UUID) (*entity.CallParticipant, error) {
	return nil, cerrors.NotFound("participant not found")
}
func (r *fakeCallRepo) ListParticipants(context.Context, uuid.UUID) ([]entity.CallParticipant, error) {
	return nil, nil
}
func (r *fakeCallRepo) UpdateParticipantStatus(context.Context, uuid.UUID, entity.ParticipantStatus) error {
	return nil
}
func (r *fakeCallRepo) UpdateParticipantRole(context.Context, uuid.UUID, entity.CallRole) error {
	return nil
}
func (r *fakeCallRepo) TransferHost(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}
func (r *fakeCallRepo) UpdateParticipantMedia(context.Context, uuid.UUID, bool, bool, bool) error {
	return nil
}
func (r *fakeCallRepo) SetCanScreenShare(context.Context, uuid.UUID, bool) error { return nil }
func (r *fakeCallRepo) SetFeaturedShareUserID(context.Context, uuid.UUID, *uuid.UUID) error {
	return nil
}
func (r *fakeCallRepo) SetPinnedParticipantUserID(context.Context, uuid.UUID, *uuid.UUID) error {
	return nil
}
func (r *fakeCallRepo) RemoveParticipant(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type fakeStateStore struct {
	rooms map[string][]string
	seq   map[string]int64
}

func (f *fakeStateStore) AddSubscription(_ context.Context, resumeKey, room string) error {
	f.rooms[resumeKey] = append(f.rooms[resumeKey], room)
	return nil
}
func (f *fakeStateStore) RemoveSubscription(context.Context, string, string) error { return nil }
func (f *fakeStateStore) ListSubscriptions(_ context.Context, resumeKey string) ([]string, error) {
	return append([]string(nil), f.rooms[resumeKey]...), nil
}
func (f *fakeStateStore) LastDeliveredSequence(_ context.Context, resumeKey, room string) (int64, error) {
	return f.seq[resumeKey+":"+room], nil
}
func (f *fakeStateStore) RecordDeliveredSequence(_ context.Context, resumeKey, room string, sequence int64) error {
	if f.seq == nil {
		f.seq = map[string]int64{}
	}
	f.seq[resumeKey+":"+room] = sequence
	return nil
}

type fakeRoomReplayer struct {
	events []event.Event
}

func (f fakeRoomReplayer) ReplayRoom(context.Context, string, int64, int) ([]event.Event, error) {
	return f.events, nil
}

type fakeWorkspaceRepo struct {
	members map[[2]uuid.UUID]*entity.WorkspaceMember
}

func (r *fakeWorkspaceRepo) Create(context.Context, *entity.Workspace) error { return nil }
func (r *fakeWorkspaceRepo) GetByID(context.Context, uuid.UUID) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}
func (r *fakeWorkspaceRepo) GetBySlug(context.Context, string) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}
func (r *fakeWorkspaceRepo) ListByUser(context.Context, uuid.UUID) ([]entity.Workspace, error) {
	return nil, nil
}
func (r *fakeWorkspaceRepo) Update(context.Context, *entity.Workspace) error          { return nil }
func (r *fakeWorkspaceRepo) AddMember(context.Context, *entity.WorkspaceMember) error { return nil }
func (r *fakeWorkspaceRepo) GetMember(_ context.Context, workspaceID, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	if member := r.members[[2]uuid.UUID{workspaceID, userID}]; member != nil {
		return member, nil
	}
	return nil, cerrors.NotFound("workspace member not found")
}
func (r *fakeWorkspaceRepo) ListMembers(context.Context, uuid.UUID, pagination.Params, string) ([]entity.WorkspaceMember, error) {
	return nil, nil
}
func (r *fakeWorkspaceRepo) UpdateMemberRole(context.Context, uuid.UUID, uuid.UUID, entity.WorkspaceRole) error {
	return nil
}
func (r *fakeWorkspaceRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type fakeChannelRepo struct {
	channels map[uuid.UUID]*entity.Channel
	members  map[[2]uuid.UUID]*entity.ChannelMember
}

func (r *fakeChannelRepo) Create(context.Context, *entity.Channel) error { return nil }
func (r *fakeChannelRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Channel, error) {
	if ch := r.channels[id]; ch != nil {
		return ch, nil
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
func (r *fakeChannelRepo) Update(context.Context, *entity.Channel) error          { return nil }
func (r *fakeChannelRepo) Archive(context.Context, uuid.UUID) error               { return nil }
func (r *fakeChannelRepo) AddMember(context.Context, *entity.ChannelMember) error { return nil }
func (r *fakeChannelRepo) GetMember(_ context.Context, channelID, userID uuid.UUID) (*entity.ChannelMember, error) {
	if member := r.members[[2]uuid.UUID{channelID, userID}]; member != nil {
		return member, nil
	}
	return nil, cerrors.NotFound("channel member not found")
}
func (r *fakeChannelRepo) ListMembers(context.Context, uuid.UUID) ([]entity.ChannelMember, error) {
	return nil, nil
}
func (r *fakeChannelRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (r *fakeChannelRepo) UpdateLastRead(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *fakeChannelRepo) GetDMChannel(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*entity.Channel, error) {
	return nil, cerrors.NotFound("dm channel not found")
}
