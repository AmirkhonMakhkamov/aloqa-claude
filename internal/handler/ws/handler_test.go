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
			privateChannelID: {ID: privateChannelID, WorkspaceID: workspaceID, Type: entity.ChannelTypePrivate},
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
	if handler.canSubscribe(ctx, client, "channel:"+privateChannelID.String()) {
		t.Fatalf("private channel subscription without channel membership was allowed")
	}

	channels.members[[2]uuid.UUID{privateChannelID, userID}] = &entity.ChannelMember{ChannelID: privateChannelID, UserID: userID}
	if !handler.canSubscribe(ctx, client, "channel:"+privateChannelID.String()) {
		t.Fatalf("private channel subscription was denied for a channel member")
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

type noopPublisher struct{}

func (noopPublisher) Publish(context.Context, string, []byte) error { return nil }

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
func (r *fakeWorkspaceRepo) ListMembers(context.Context, uuid.UUID, pagination.Params) ([]entity.WorkspaceMember, error) {
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
