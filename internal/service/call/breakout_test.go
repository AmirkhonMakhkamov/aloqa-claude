package call

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/media/sfu"
	"aloqa/internal/pkg/cerrors"
)

// stubBreakoutRepo is a stateful in-memory BreakoutRoomRepository used by the
// breakout LiveKit tests. The package-level fakeBreakoutRepo is intentionally
// stateless (GetByID always NotFound), which is unsuitable for asserting
// assignment / lifecycle behavior.
type stubBreakoutRepo struct {
	rooms                  map[uuid.UUID]*entity.BreakoutRoom
	assignments            []breakoutAssignment
	closeAllByCallCount    int
	unassignAllByRoomCount int
}

type breakoutAssignment struct {
	callID         uuid.UUID
	userID         uuid.UUID
	breakoutRoomID uuid.UUID
}

func newStubBreakoutRepo() *stubBreakoutRepo {
	return &stubBreakoutRepo{rooms: map[uuid.UUID]*entity.BreakoutRoom{}}
}

func (r *stubBreakoutRepo) Create(_ context.Context, room *entity.BreakoutRoom) error {
	r.rooms[room.ID] = room
	return nil
}

func (r *stubBreakoutRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.BreakoutRoom, error) {
	if room, ok := r.rooms[id]; ok {
		return room, nil
	}
	return nil, cerrors.NotFound("breakout room not found")
}

func (r *stubBreakoutRepo) ListByCall(_ context.Context, callID uuid.UUID) ([]entity.BreakoutRoom, error) {
	out := make([]entity.BreakoutRoom, 0)
	for _, room := range r.rooms {
		if room.CallID == callID {
			out = append(out, *room)
		}
	}
	return out, nil
}

func (r *stubBreakoutRepo) Close(_ context.Context, id uuid.UUID) error {
	if room, ok := r.rooms[id]; ok {
		room.Status = entity.BreakoutRoomStatusClosed
	}
	return nil
}

func (r *stubBreakoutRepo) CloseAllByCall(_ context.Context, callID uuid.UUID) error {
	r.closeAllByCallCount++
	for _, room := range r.rooms {
		if room.CallID == callID {
			room.Status = entity.BreakoutRoomStatusClosed
		}
	}
	return nil
}

func (r *stubBreakoutRepo) AssignParticipant(_ context.Context, callID, userID, breakoutRoomID uuid.UUID) error {
	r.assignments = append(r.assignments, breakoutAssignment{callID: callID, userID: userID, breakoutRoomID: breakoutRoomID})
	return nil
}

func (r *stubBreakoutRepo) UnassignParticipant(_ context.Context, callID, userID uuid.UUID) error {
	r.assignments = append(r.assignments, breakoutAssignment{callID: callID, userID: userID, breakoutRoomID: uuid.Nil})
	return nil
}

func (r *stubBreakoutRepo) UnassignAllByRoom(context.Context, uuid.UUID) error {
	r.unassignAllByRoomCount++
	return nil
}

func (r *stubBreakoutRepo) ListParticipants(context.Context, uuid.UUID) ([]entity.CallParticipant, error) {
	return nil, nil
}

func newBreakoutSFU(t *testing.T) *sfu.SFU {
	t.Helper()
	server, err := sfu.NewSFU(sfu.Config{})
	if err != nil {
		t.Fatalf("NewSFU returned error: %v", err)
	}
	return server
}

func breakoutTestLiveKit() LiveKitSettings {
	return LiveKitSettings{
		URL:       "ws://livekit.test",
		APIKey:    "APItest",
		APISecret: "aloqa-livekit-test-secret",
		TokenTTL:  time.Hour,
	}
}

// countBreakoutEvents returns how many captured publishes carry the given event type.
func countBreakoutEvents(t *testing.T, captures []capturedPublish, evtType event.Type) int {
	t.Helper()
	count := 0
	for _, c := range captures {
		var env struct {
			Type event.Type `json:"type"`
		}
		if err := json.Unmarshal(c.body, &env); err != nil {
			t.Fatalf("unmarshal captured event: %v", err)
		}
		if env.Type == evtType {
			count++
		}
	}
	return count
}

func breakoutCall(callID, workspaceID, hostID uuid.UUID) *entity.Call {
	return &entity.Call{
		ID:          callID,
		WorkspaceID: workspaceID,
		Type:        entity.CallTypeMeeting,
		Status:      entity.CallStatusActive,
		CreatedBy:   hostID,
		Settings:    entity.CallSettings{BreakoutRooms: true},
	}
}

func TestListBreakoutRoomsReturnsEmptySlice(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()

	calls := &fakeCallRepo{calls: map[uuid.UUID]*entity.Call{
		callID: breakoutCall(callID, workspaceID, hostID),
	}}
	svc := NewService(calls, newStubBreakoutRepo(), &fakeChannelRepo{}, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	rooms, err := svc.ListBreakoutRooms(ctx, callID)
	if err != nil {
		t.Fatalf("ListBreakoutRooms returned error: %v", err)
	}
	if rooms == nil {
		t.Fatalf("ListBreakoutRooms returned nil slice, want empty slice")
	}
	if len(rooms) != 0 {
		t.Fatalf("ListBreakoutRooms len = %d, want 0", len(rooms))
	}
}

func TestListBreakoutRoomParticipantsReturnsEmptySlice(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	roomID := uuid.New()

	repo := newStubBreakoutRepo()
	repo.rooms[roomID] = &entity.BreakoutRoom{
		ID:     roomID,
		CallID: callID,
		Name:   "Room A",
		Status: entity.BreakoutRoomStatusActive,
	}
	calls := &fakeCallRepo{calls: map[uuid.UUID]*entity.Call{
		callID: breakoutCall(callID, workspaceID, hostID),
	}}
	svc := NewService(calls, repo, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	participants, err := svc.ListBreakoutRoomParticipants(ctx, callID, roomID)
	if err != nil {
		t.Fatalf("ListBreakoutRoomParticipants returned error: %v", err)
	}
	if participants == nil {
		t.Fatalf("ListBreakoutRoomParticipants returned nil slice, want empty slice")
	}
	if len(participants) != 0 {
		t.Fatalf("ListBreakoutRoomParticipants len = %d, want 0", len(participants))
	}
}

func connectedParticipant(callID, userID uuid.UUID, role entity.CallRole) *entity.CallParticipant {
	return &entity.CallParticipant{
		ID:     uuid.New(),
		CallID: callID,
		UserID: userID,
		Role:   role,
		Status: entity.ParticipantStatusConnected,
	}
}

func TestCreateBreakoutRoomsAssignsConnectedParticipants(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	connected := uuid.New()
	pending := uuid.New() // exists but not connected → skipped

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: breakoutCall(callID, workspaceID, hostID)},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}:    connectedParticipant(callID, hostID, entity.CallRoleHost),
			{callID, connected}: connectedParticipant(callID, connected, entity.CallRoleParticipant),
			{callID, pending}:   {ID: uuid.New(), CallID: callID, UserID: pending, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusWaiting},
		},
	}
	repo := newStubBreakoutRepo()
	pub := &capturingPublisher{}
	roomClient := &fakeLiveKitRoomClient{}
	svc := NewService(calls, repo, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, newBreakoutSFU(t), mediaTestConfig(), nil, nil)
	svc.SetLiveKit(breakoutTestLiveKit())
	svc.SetLiveKitRoomClient(roomClient)

	stranger := uuid.New() // not a participant at all → skipped
	rooms, err := svc.CreateBreakoutRooms(ctx, callID, hostID, []CreateBreakoutRoomInput{
		{Name: "Room A", ParticipantUserIDs: []uuid.UUID{connected, pending, hostID, stranger}},
	})
	if err != nil {
		t.Fatalf("CreateBreakoutRooms returned error: %v", err)
	}
	if len(rooms) != 1 {
		t.Fatalf("rooms = %d, want 1", len(rooms))
	}
	room := rooms[0]

	// LiveKit breakout room ensured under the breakout naming scheme.
	wantName := breakoutLiveKitRoomName(callID, room.ID)
	foundEnsure := false
	for _, n := range roomClient.ensuredRoomNames {
		if n == wantName {
			foundEnsure = true
		}
	}
	if !foundEnsure {
		t.Fatalf("ensured room names = %v, want to contain %s", roomClient.ensuredRoomNames, wantName)
	}

	// Only the single connected participant is assigned (host/pending/stranger skipped).
	if len(repo.assignments) != 1 {
		t.Fatalf("assignments = %d, want 1 (only the connected participant)", len(repo.assignments))
	}
	if got := repo.assignments[0]; got.userID != connected || got.breakoutRoomID != room.ID {
		t.Fatalf("assignment = %+v, want user %s -> room %s", got, connected, room.ID)
	}

	// One moved event per assignee.
	if got := countBreakoutEvents(t, pub.captures, event.TypeBreakoutParticipantMoved); got != 1 {
		t.Fatalf("participant.moved events = %d, want 1", got)
	}
	if got := countBreakoutEvents(t, pub.captures, event.TypeBreakoutRoomCreated); got != 1 {
		t.Fatalf("room.created events = %d, want 1", got)
	}
}

func TestCreateBreakoutRoomsRejectsNonHost(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	plebID := uuid.New()

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: breakoutCall(callID, workspaceID, hostID)},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, plebID}: connectedParticipant(callID, plebID, entity.CallRoleParticipant),
		},
	}
	svc := NewService(calls, newStubBreakoutRepo(), &fakeChannelRepo{}, &fakeWorkspaceRepo{}, noopPublisher{}, newBreakoutSFU(t), mediaTestConfig(), nil, nil)

	_, err := svc.CreateBreakoutRooms(ctx, callID, plebID, []CreateBreakoutRoomInput{{Name: "Room A"}})
	if !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("CreateBreakoutRooms non-host error = %v, want FORBIDDEN", err)
	}
}

func TestJoinBreakoutRoomReturnsLiveKitJoinInfo(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	userID := uuid.New()

	repo := newStubBreakoutRepo()
	room := &entity.BreakoutRoom{ID: uuid.New(), CallID: callID, Name: "Room A", Status: entity.BreakoutRoomStatusActive}
	repo.rooms[room.ID] = room

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: breakoutCall(callID, workspaceID, hostID)},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: connectedParticipant(callID, userID, entity.CallRoleParticipant),
		},
	}
	pub := &capturingPublisher{}
	roomClient := &fakeLiveKitRoomClient{}
	svc := NewService(calls, repo, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, newBreakoutSFU(t), mediaTestConfig(), nil, nil)
	svc.SetLiveKit(breakoutTestLiveKit())
	svc.SetLiveKitRoomClient(roomClient)

	info, err := svc.JoinBreakoutRoom(ctx, callID, userID, room.ID)
	if err != nil {
		t.Fatalf("JoinBreakoutRoom returned error: %v", err)
	}
	if info == nil || info.AccessToken == "" {
		t.Fatalf("JoinBreakoutRoom join info = %+v, want non-empty access token", info)
	}
	if info.URL != "ws://livekit.test" {
		t.Fatalf("join info URL = %q, want ws://livekit.test", info.URL)
	}

	if len(repo.assignments) != 1 || repo.assignments[0].breakoutRoomID != room.ID {
		t.Fatalf("assignments = %+v, want one assignment to room %s", repo.assignments, room.ID)
	}
	if got := countBreakoutEvents(t, pub.captures, event.TypeBreakoutParticipantMoved); got != 1 {
		t.Fatalf("participant.moved events = %d, want 1", got)
	}
}

func TestJoinBreakoutRoomLiveKitUnconfiguredReturnsUnavailable(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	userID := uuid.New()

	repo := newStubBreakoutRepo()
	room := &entity.BreakoutRoom{ID: uuid.New(), CallID: callID, Name: "Room A", Status: entity.BreakoutRoomStatusActive}
	repo.rooms[room.ID] = room

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: breakoutCall(callID, workspaceID, hostID)},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: connectedParticipant(callID, userID, entity.CallRoleParticipant),
		},
	}
	svc := NewService(calls, repo, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	info, err := svc.JoinBreakoutRoom(ctx, callID, userID, room.ID)
	if !hasCode(err, cerrors.CodeUnavailable) {
		t.Fatalf("JoinBreakoutRoom error = %v, want UNAVAILABLE", err)
	}
	if info != nil {
		t.Fatalf("JoinBreakoutRoom info = %+v, want nil", info)
	}
	if len(repo.assignments) != 0 {
		t.Fatalf("assignments = %+v, want none before livekit is configured", repo.assignments)
	}
}

func TestReturnToMainRoomReturnsMainLiveKitJoinInfo(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	userID := uuid.New()
	breakoutRoomID := uuid.New()

	participant := connectedParticipant(callID, userID, entity.CallRoleParticipant)
	participant.BreakoutRoomID = &breakoutRoomID

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: breakoutCall(callID, workspaceID, hostID)},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: participant,
		},
	}
	repo := newStubBreakoutRepo()
	pub := &capturingPublisher{}
	svc := NewService(calls, repo, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, newBreakoutSFU(t), mediaTestConfig(), nil, nil)
	svc.SetLiveKit(breakoutTestLiveKit())
	svc.SetLiveKitRoomClient(&fakeLiveKitRoomClient{})

	info, err := svc.ReturnToMainRoom(ctx, callID, userID)
	if err != nil {
		t.Fatalf("ReturnToMainRoom returned error: %v", err)
	}
	if info == nil || info.AccessToken == "" {
		t.Fatalf("ReturnToMainRoom join info = %+v, want non-empty access token", info)
	}

	// One unassign recorded (breakoutRoomID == Nil sentinel).
	if len(repo.assignments) != 1 || repo.assignments[0].breakoutRoomID != uuid.Nil {
		t.Fatalf("assignments = %+v, want one unassign", repo.assignments)
	}
	if got := countBreakoutEvents(t, pub.captures, event.TypeBreakoutParticipantMoved); got != 1 {
		t.Fatalf("participant.moved events = %d, want 1", got)
	}
}

func TestReturnToMainRoomLiveKitUnconfiguredReturnsUnavailable(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	userID := uuid.New()
	breakoutRoomID := uuid.New()

	participant := connectedParticipant(callID, userID, entity.CallRoleParticipant)
	participant.BreakoutRoomID = &breakoutRoomID

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: breakoutCall(callID, workspaceID, hostID)},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: participant,
		},
	}
	repo := newStubBreakoutRepo()
	svc := NewService(calls, repo, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	info, err := svc.ReturnToMainRoom(ctx, callID, userID)
	if !hasCode(err, cerrors.CodeUnavailable) {
		t.Fatalf("ReturnToMainRoom error = %v, want UNAVAILABLE", err)
	}
	if info != nil {
		t.Fatalf("ReturnToMainRoom info = %+v, want nil", info)
	}
	if len(repo.assignments) != 0 {
		t.Fatalf("assignments = %+v, want none before livekit is configured", repo.assignments)
	}
}

// breakoutDeleteScheduled reports whether a deferred LiveKit delete is pending
// for the given room name.
func breakoutDeleteScheduled(svc *Service, name string) bool {
	_, ok := svc.breakoutDeleteTimers.Load(name)
	return ok
}

func TestCloseAllBreakoutRoomsSchedulesDeferredLiveKitDelete(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()

	repo := newStubBreakoutRepo()
	roomA := &entity.BreakoutRoom{ID: uuid.New(), CallID: callID, Name: "A", Status: entity.BreakoutRoomStatusActive}
	roomB := &entity.BreakoutRoom{ID: uuid.New(), CallID: callID, Name: "B", Status: entity.BreakoutRoomStatusActive}
	repo.rooms[roomA.ID] = roomA
	repo.rooms[roomB.ID] = roomB

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: breakoutCall(callID, workspaceID, hostID)},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: connectedParticipant(callID, hostID, entity.CallRoleHost),
		},
	}
	pub := &capturingPublisher{}
	roomClient := &fakeLiveKitRoomClient{}
	svc := NewService(calls, repo, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, newBreakoutSFU(t), mediaTestConfig(), nil, nil)
	svc.SetLiveKit(breakoutTestLiveKit())
	svc.SetLiveKitRoomClient(roomClient)

	if err := svc.CloseAllBreakoutRooms(ctx, callID, hostID); err != nil {
		t.Fatalf("CloseAllBreakoutRooms returned error: %v", err)
	}

	wantA := breakoutLiveKitRoomName(callID, roomA.ID)
	wantB := breakoutLiveKitRoomName(callID, roomB.ID)

	// The LiveKit delete must NOT happen synchronously on close — deleting the
	// room while clients are still connected would kick them with ROOM_DELETED.
	if len(roomClient.deletedRoomNames) != 0 {
		t.Fatalf("deleted room names = %v, want none synchronously (delete is deferred)", roomClient.deletedRoomNames)
	}

	// Both breakout rooms must have a pending deferred-delete timer recorded so
	// the LiveKit room is reaped after the grace period.
	if !breakoutDeleteScheduled(svc, wantA) || !breakoutDeleteScheduled(svc, wantB) {
		t.Fatalf("deferred deletes not scheduled for %s and %s", wantA, wantB)
	}

	// Clients still receive the all-closed broadcast immediately so they begin
	// returning to the main room.
	if got := countBreakoutEvents(t, pub.captures, event.TypeBreakoutRoomsAllClosed); got != 1 {
		t.Fatalf("rooms.all_closed events = %d, want 1", got)
	}

	// Tidy up the pending timers so they don't fire after the test.
	svc.cancelBreakoutLiveKitDelete(wantA)
	svc.cancelBreakoutLiveKitDelete(wantB)
}

func TestCloseBreakoutRoomSchedulesDeferredLiveKitDelete(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()

	repo := newStubBreakoutRepo()
	room := &entity.BreakoutRoom{ID: uuid.New(), CallID: callID, Name: "A", Status: entity.BreakoutRoomStatusActive}
	repo.rooms[room.ID] = room

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: breakoutCall(callID, workspaceID, hostID)},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: connectedParticipant(callID, hostID, entity.CallRoleHost),
		},
	}
	pub := &capturingPublisher{}
	roomClient := &fakeLiveKitRoomClient{}
	svc := NewService(calls, repo, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, newBreakoutSFU(t), mediaTestConfig(), nil, nil)
	svc.SetLiveKit(breakoutTestLiveKit())
	svc.SetLiveKitRoomClient(roomClient)

	if err := svc.CloseBreakoutRoom(ctx, callID, hostID, room.ID); err != nil {
		t.Fatalf("CloseBreakoutRoom returned error: %v", err)
	}

	want := breakoutLiveKitRoomName(callID, room.ID)

	// No synchronous delete (would kick still-connected clients).
	if len(roomClient.deletedRoomNames) != 0 {
		t.Fatalf("deleted room names = %v, want none synchronously (delete is deferred)", roomClient.deletedRoomNames)
	}
	if !breakoutDeleteScheduled(svc, want) {
		t.Fatalf("deferred delete not scheduled for %s", want)
	}
	if got := countBreakoutEvents(t, pub.captures, event.TypeBreakoutRoomClosed); got != 1 {
		t.Fatalf("room.closed events = %d, want 1", got)
	}

	svc.cancelBreakoutLiveKitDelete(want)
}

func TestEnsureLiveKitBreakoutRoomCancelsPendingDelete(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	breakoutRoomID := uuid.New()

	roomClient := &fakeLiveKitRoomClient{}
	svc := NewService(&fakeCallRepo{}, newStubBreakoutRepo(), &fakeChannelRepo{}, &fakeWorkspaceRepo{}, &capturingPublisher{}, newBreakoutSFU(t), mediaTestConfig(), nil, nil)
	svc.SetLiveKit(breakoutTestLiveKit())
	svc.SetLiveKitRoomClient(roomClient)

	name := breakoutLiveKitRoomName(callID, breakoutRoomID)
	svc.scheduleBreakoutLiveKitDelete(name)
	if !breakoutDeleteScheduled(svc, name) {
		t.Fatalf("expected deferred delete scheduled for %s", name)
	}

	// Re-ensuring the same breakout room must cancel the pending delete so the
	// reused/recreated room is not deleted out from under its participants.
	call := breakoutCall(callID, workspaceID, hostID)
	if err := svc.EnsureLiveKitBreakoutRoom(ctx, call, breakoutRoomID); err != nil {
		t.Fatalf("EnsureLiveKitBreakoutRoom returned error: %v", err)
	}
	if breakoutDeleteScheduled(svc, name) {
		t.Fatalf("expected pending delete cancelled after re-ensure for %s", name)
	}
}

func TestCloseAllBreakoutSFURoomsDeletesLiveKitImmediatelyOnCallEnd(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()

	repo := newStubBreakoutRepo()
	room := &entity.BreakoutRoom{ID: uuid.New(), CallID: callID, Name: "A", Status: entity.BreakoutRoomStatusActive}
	repo.rooms[room.ID] = room

	roomClient := &fakeLiveKitRoomClient{}
	svc := NewService(&fakeCallRepo{calls: map[uuid.UUID]*entity.Call{callID: breakoutCall(callID, workspaceID, hostID)}}, repo, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, &capturingPublisher{}, newBreakoutSFU(t), mediaTestConfig(), nil, nil)
	svc.SetLiveKit(breakoutTestLiveKit())
	svc.SetLiveKitRoomClient(roomClient)

	name := breakoutLiveKitRoomName(callID, room.ID)
	// Pre-arm a pending grace delete (as a prior close would have).
	svc.scheduleBreakoutLiveKitDelete(name)

	// When the main call ends, breakout LiveKit rooms are deleted immediately
	// (they are empty) and any pending grace timer is cancelled.
	svc.closeAllBreakoutSFURooms(ctx, callID)

	if breakoutDeleteScheduled(svc, name) {
		t.Fatalf("expected pending grace delete cancelled on call end for %s", name)
	}
	deleted := false
	for _, n := range roomClient.deletedRoomNames {
		if n == name {
			deleted = true
		}
	}
	if !deleted {
		t.Fatalf("deleted room names = %v, want to contain %s (immediate delete on call end)", roomClient.deletedRoomNames, name)
	}
}
