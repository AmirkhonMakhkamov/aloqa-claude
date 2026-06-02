package call

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
)

// fakeMessageRepo records Create calls; all other MessageRepository methods are
// inherited from the embedded nil interface and panic if exercised (none are).
type fakeMessageRepo struct {
	repository.MessageRepository
	created []*entity.Message
}

func (r *fakeMessageRepo) Create(_ context.Context, msg *entity.Message) error {
	clone := *msg
	r.created = append(r.created, &clone)
	return nil
}

func newEmitTestService(calls *fakeCallRepo, msgs *fakeMessageRepo) *Service {
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	svc.SetMessageRepo(msgs)
	return svc
}

func TestEmitCallEndedChatMessage_AllLeftWritesSystemMessage(t *testing.T) {
	ctx := context.Background()
	callID := uuid.New()
	workspaceID := uuid.New()
	channelID := uuid.New()
	host := uuid.New()
	other := uuid.New()
	joined := time.Now().Add(-5 * time.Minute)
	started := joined
	ended := time.Now()

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, host}:  {ID: uuid.New(), CallID: callID, UserID: host, JoinedAt: &joined, Status: entity.ParticipantStatusDisconnected},
			{callID, other}: {ID: uuid.New(), CallID: callID, UserID: other, JoinedAt: &joined, Status: entity.ParticipantStatusDisconnected},
		},
	}
	msgs := &fakeMessageRepo{}
	svc := newEmitTestService(calls, msgs)

	call := &entity.Call{
		ID:          callID,
		WorkspaceID: workspaceID,
		ChannelID:   &channelID,
		Type:        entity.CallTypeOneToOne,
		Status:      entity.CallStatusEnded,
		CreatedBy:   host,
		StartedAt:   &started,
		EndedAt:     &ended,
		EndReason:   entity.CallEndReasonAllLeft,
	}

	svc.emitCallEndedChatMessage(ctx, call)

	if len(msgs.created) != 1 {
		t.Fatalf("created messages = %d, want 1", len(msgs.created))
	}
	m := msgs.created[0]
	if m.Type != entity.MessageTypeSystem {
		t.Fatalf("message type = %q, want system", m.Type)
	}
	if m.ChannelID != channelID {
		t.Fatalf("message channel = %s, want %s", m.ChannelID, channelID)
	}
	if m.UserID != host {
		t.Fatalf("message user = %s, want initiator %s", m.UserID, host)
	}
	if m.Content != "Call" {
		t.Fatalf("content = %q, want Call", m.Content)
	}

	var p callEventPayload
	if err := json.Unmarshal(m.CallEvent, &p); err != nil {
		t.Fatalf("call_event is not valid json: %v", err)
	}
	if p.CallID != callID {
		t.Fatalf("call_event.call_id = %s, want %s", p.CallID, callID)
	}
	if p.CallType != string(entity.CallTypeOneToOne) {
		t.Fatalf("call_event.call_type = %q, want one_to_one", p.CallType)
	}
	if p.EndReason != string(entity.CallEndReasonAllLeft) {
		t.Fatalf("call_event.end_reason = %q, want all_left", p.EndReason)
	}
	if p.InitiatorID != host {
		t.Fatalf("call_event.initiator_id = %s, want %s", p.InitiatorID, host)
	}
	if p.ParticipantCount != 2 || len(p.ParticipantUserIDs) != 2 {
		t.Fatalf("participants = count %d ids %d, want 2/2", p.ParticipantCount, len(p.ParticipantUserIDs))
	}
	if p.DurationSeconds <= 0 {
		t.Fatalf("duration_seconds = %d, want > 0", p.DurationSeconds)
	}
}

func TestEmitCallEndedChatMessage_MissedHasZeroDurationAndCount(t *testing.T) {
	ctx := context.Background()
	callID := uuid.New()
	caller := uuid.New()
	callee := uuid.New()
	channelID := uuid.New()

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{},
		// Nobody joined: JoinedAt is nil for both (ringing -> missed).
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, caller}: {ID: uuid.New(), CallID: callID, UserID: caller, Status: entity.ParticipantStatusInvited},
			{callID, callee}: {ID: uuid.New(), CallID: callID, UserID: callee, Status: entity.ParticipantStatusInvited},
		},
	}
	msgs := &fakeMessageRepo{}
	svc := newEmitTestService(calls, msgs)

	ended := time.Now()
	call := &entity.Call{
		ID:          callID,
		WorkspaceID: uuid.New(),
		ChannelID:   &channelID,
		Type:        entity.CallTypeOneToOne,
		Status:      entity.CallStatusEnded,
		CreatedBy:   caller,
		StartedAt:   nil,
		EndedAt:     &ended,
		EndReason:   entity.CallEndReasonMissed,
	}

	svc.emitCallEndedChatMessage(ctx, call)

	if len(msgs.created) != 1 {
		t.Fatalf("created messages = %d, want 1", len(msgs.created))
	}
	m := msgs.created[0]
	if m.Content != "Missed call" {
		t.Fatalf("content = %q, want Missed call", m.Content)
	}
	var p callEventPayload
	if err := json.Unmarshal(m.CallEvent, &p); err != nil {
		t.Fatalf("call_event invalid json: %v", err)
	}
	if p.ParticipantCount != 0 || len(p.ParticipantUserIDs) != 0 {
		t.Fatalf("participants = %d, want 0 (nobody joined)", p.ParticipantCount)
	}
	if p.DurationSeconds != 0 {
		t.Fatalf("duration_seconds = %d, want 0", p.DurationSeconds)
	}
}

func TestEmitCallEndedChatMessage_SkipsCallsWithoutChannel(t *testing.T) {
	ctx := context.Background()
	calls := &fakeCallRepo{calls: map[uuid.UUID]*entity.Call{}, participants: map[[2]uuid.UUID]*entity.CallParticipant{}}
	msgs := &fakeMessageRepo{}
	svc := newEmitTestService(calls, msgs)

	call := &entity.Call{
		ID:          uuid.New(),
		WorkspaceID: uuid.New(),
		ChannelID:   nil, // standalone group call — no chat timeline to write into
		Type:        entity.CallTypeGroup,
		Status:      entity.CallStatusEnded,
		CreatedBy:   uuid.New(),
		EndReason:   entity.CallEndReasonHostEnded,
	}
	svc.emitCallEndedChatMessage(ctx, call)

	if len(msgs.created) != 0 {
		t.Fatalf("created messages = %d, want 0 for channel-less call", len(msgs.created))
	}
}

func TestEmitCallEndedChatMessage_SkipsSelectorType(t *testing.T) {
	ctx := context.Background()
	channelID := uuid.New()
	calls := &fakeCallRepo{calls: map[uuid.UUID]*entity.Call{}, participants: map[[2]uuid.UUID]*entity.CallParticipant{}}
	msgs := &fakeMessageRepo{}
	svc := newEmitTestService(calls, msgs)

	call := &entity.Call{
		ID:          uuid.New(),
		WorkspaceID: uuid.New(),
		ChannelID:   &channelID,
		Type:        entity.CallTypeSelector,
		Status:      entity.CallStatusEnded,
		CreatedBy:   uuid.New(),
		EndReason:   entity.CallEndReasonHostEnded,
	}
	svc.emitCallEndedChatMessage(ctx, call)

	if len(msgs.created) != 0 {
		t.Fatalf("created messages = %d, want 0 for selector call", len(msgs.created))
	}
}

func TestLeaveCallAutoEndWritesCallEventToHistory(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	channelID := uuid.New()
	hostID := uuid.New()
	userID := uuid.New()
	joined := time.Now().Add(-3 * time.Minute)
	started := joined

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, ChannelID: &channelID, Type: entity.CallTypeOneToOne, Status: entity.CallStatusActive, CreatedBy: hostID, StartedAt: &started},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: {ID: uuid.New(), CallID: callID, UserID: hostID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusDisconnected, JoinedAt: &joined},
			{callID, userID}: {ID: uuid.New(), CallID: callID, UserID: userID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected, JoinedAt: &joined},
		},
	}
	channels := &fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
		channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypeDM},
	}}
	msgs := &fakeMessageRepo{}
	svc := NewService(calls, &fakeBreakoutRepo{}, channels, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	svc.SetMessageRepo(msgs)

	if _, err := svc.LeaveCall(ctx, workspaceID, callID, userID); err != nil {
		t.Fatalf("LeaveCall returned error: %v", err)
	}

	if len(msgs.created) != 1 {
		t.Fatalf("created messages = %d, want exactly 1 call-event message", len(msgs.created))
	}
	var p callEventPayload
	if err := json.Unmarshal(msgs.created[0].CallEvent, &p); err != nil {
		t.Fatalf("call_event invalid json: %v", err)
	}
	if p.EndReason != string(entity.CallEndReasonAllLeft) {
		t.Fatalf("end_reason = %q, want all_left", p.EndReason)
	}
}

func TestBuildCallEventPayload_DedupesJoinedAndComputesDuration(t *testing.T) {
	userA := uuid.New()
	joined := time.Now().Add(-90 * time.Second)
	started := joined
	ended := joined.Add(90 * time.Second)
	call := &entity.Call{
		ID:        uuid.New(),
		Type:      entity.CallTypeGroup,
		CreatedBy: userA,
		StartedAt: &started,
		EndedAt:   &ended,
		EndReason: entity.CallEndReasonAllLeft,
	}
	participants := []entity.CallParticipant{
		{UserID: userA, JoinedAt: &joined},
		{UserID: userA, JoinedAt: &joined}, // reconnect: same user twice -> dedup to 1
		{UserID: uuid.New()},               // never joined (JoinedAt nil) -> excluded
	}

	p := buildCallEventPayload(call, participants)
	if p.ParticipantCount != 1 || len(p.ParticipantUserIDs) != 1 {
		t.Fatalf("participant count = %d, want 1 (deduped, joined-only)", p.ParticipantCount)
	}
	if p.DurationSeconds != 90 {
		t.Fatalf("duration_seconds = %d, want 90", p.DurationSeconds)
	}
}
