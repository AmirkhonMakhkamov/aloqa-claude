package call

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	eventpkg "aloqa/internal/domain/event"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
	"aloqa/internal/platform/txscope"
	searchsvc "aloqa/internal/service/search"
)

func TestSendCallMessageCreatesMessageAndEnqueuesEvent(t *testing.T) {
	ctx := context.Background()
	f := newCallMessageServiceFixture()
	txScope := &fakeCallMessageTxScope{callMessages: f.messages}
	tx := &fakeCallMessageTxManager{scope: txScope}
	f.svc.SetTransactionManager(tx)

	msg, err := f.svc.SendCallMessage(ctx, f.workspaceID, f.callID, f.senderID, "  hello call  ")
	if err != nil {
		t.Fatalf("SendCallMessage returned error: %v", err)
	}
	if msg.Body != "  hello call  " {
		t.Fatalf("message body = %q, want verbatim body", msg.Body)
	}
	if stored := f.messages.messages[msg.ID]; stored == nil || stored.Body != "  hello call  " {
		t.Fatalf("stored message = %+v, want created message", stored)
	}
	if tx.calls != 1 {
		t.Fatalf("transaction calls = %d, want 1", tx.calls)
	}
	if len(txScope.events) != 1 {
		t.Fatalf("enqueued events = %d, want 1", len(txScope.events))
	}
	if evt := txScope.events[0]; evt.Type != eventpkg.TypeCallMessageCreated || evt.Subject != "aloqa.ws."+f.workspaceID.String() {
		t.Fatalf("event = %+v, want call.message.created on workspace subject", evt)
	}
}

func TestListCallMessagesReturnsRepoItemsAfterAccessCheck(t *testing.T) {
	ctx := context.Background()
	f := newCallMessageServiceFixture()
	deletedAt := time.Now().UTC()
	visible := &entity.CallMessage{ID: uuid.New(), CallID: f.callID, SenderID: f.senderID, Body: "visible"}
	deleted := &entity.CallMessage{ID: uuid.New(), CallID: f.callID, SenderID: f.senderID, Body: "deleted", DeletedAt: &deletedAt}
	f.messages.messages[visible.ID] = visible
	f.messages.messages[deleted.ID] = deleted

	items, err := f.svc.ListCallMessages(ctx, f.workspaceID, f.callID, f.senderID, pagination.Params{Limit: 10})
	if err != nil {
		t.Fatalf("ListCallMessages returned error: %v", err)
	}
	if len(items) != 1 || items[0].ID != visible.ID {
		t.Fatalf("items = %+v, want only visible message", items)
	}
	if f.messages.listCallID != f.callID {
		t.Fatalf("repo list call id = %s, want %s", f.messages.listCallID, f.callID)
	}
}

func TestDeleteCallMessageSenderAndHost(t *testing.T) {
	ctx := context.Background()
	f := newCallMessageServiceFixture()
	msg := &entity.CallMessage{ID: uuid.New(), CallID: f.callID, SenderID: f.senderID, Body: "delete me"}
	f.messages.messages[msg.ID] = msg
	txScope := &fakeCallMessageTxScope{callMessages: f.messages}
	f.svc.SetTransactionManager(&fakeCallMessageTxManager{scope: txScope})

	if err := f.svc.DeleteCallMessage(ctx, f.workspaceID, f.callID, f.senderID, msg.ID); err != nil {
		t.Fatalf("DeleteCallMessage by sender returned error: %v", err)
	}
	if f.messages.messages[msg.ID].DeletedAt == nil {
		t.Fatalf("message was not soft-deleted")
	}
	if len(txScope.events) != 1 || txScope.events[0].Type != eventpkg.TypeCallMessageDeleted {
		t.Fatalf("events = %+v, want call.message.deleted", txScope.events)
	}

	hostDeleted := &entity.CallMessage{ID: uuid.New(), CallID: f.callID, SenderID: f.otherID, Body: "host delete"}
	f.messages.messages[hostDeleted.ID] = hostDeleted
	if err := f.svc.DeleteCallMessage(ctx, f.workspaceID, f.callID, f.hostID, hostDeleted.ID); err != nil {
		t.Fatalf("DeleteCallMessage by host returned error: %v", err)
	}
	if f.messages.messages[hostDeleted.ID].DeletedAt == nil {
		t.Fatalf("host-deleted message was not soft-deleted")
	}
}

func TestDeleteCallMessageNonSenderRequiresHost(t *testing.T) {
	ctx := context.Background()
	f := newCallMessageServiceFixture()
	msg := &entity.CallMessage{ID: uuid.New(), CallID: f.callID, SenderID: f.otherID, Body: "owned by other"}
	f.messages.messages[msg.ID] = msg

	err := f.svc.DeleteCallMessage(ctx, f.workspaceID, f.callID, f.senderID, msg.ID)
	if !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("DeleteCallMessage non-sender error = %v, want FORBIDDEN", err)
	}
}

func TestDeleteCallMessageMissingMessage(t *testing.T) {
	ctx := context.Background()
	f := newCallMessageServiceFixture()

	err := f.svc.DeleteCallMessage(ctx, f.workspaceID, f.callID, f.senderID, uuid.New())
	if !hasCode(err, cerrors.CodeNotFound) {
		t.Fatalf("DeleteCallMessage missing message error = %v, want NOT_FOUND", err)
	}
}

func TestSendCallMessageValidation(t *testing.T) {
	ctx := context.Background()
	f := newCallMessageServiceFixture()

	tests := []string{"", "   ", strings.Repeat("a", callMessageMaxBody+1), string([]byte{0xff})}
	for _, body := range tests {
		if _, err := f.svc.SendCallMessage(ctx, f.workspaceID, f.callID, f.senderID, body); !hasCode(err, cerrors.CodeInvalidInput) {
			t.Fatalf("SendCallMessage(%q) error = %v, want INVALID_INPUT", body, err)
		}
	}
}

func TestSendCallMessageAccessAndStateErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("not connected", func(t *testing.T) {
		f := newCallMessageServiceFixture()
		f.calls.participants[[2]uuid.UUID{f.callID, f.senderID}].Status = entity.ParticipantStatusDisconnected
		if _, err := f.svc.SendCallMessage(ctx, f.workspaceID, f.callID, f.senderID, "hello"); !hasCode(err, cerrors.CodeForbidden) {
			t.Fatalf("SendCallMessage disconnected error = %v, want FORBIDDEN", err)
		}
	})

	t.Run("chat disabled rejects a member", func(t *testing.T) {
		f := newCallMessageServiceFixture()
		f.calls.calls[f.callID].Settings.Chat = false
		if _, err := f.svc.SendCallMessage(ctx, f.workspaceID, f.callID, f.senderID, "hello"); !hasCode(err, cerrors.CodeForbidden) {
			t.Fatalf("SendCallMessage chat disabled error = %v, want FORBIDDEN", err)
		}
	})

	t.Run("chat disabled still allows the host (ALK-812)", func(t *testing.T) {
		f := newCallMessageServiceFixture()
		f.calls.calls[f.callID].Settings.Chat = false
		if _, err := f.svc.SendCallMessage(ctx, f.workspaceID, f.callID, f.hostID, "hello"); err != nil {
			t.Fatalf("host SendCallMessage with chat disabled error = %v, want nil", err)
		}
	})

	t.Run("ended call", func(t *testing.T) {
		f := newCallMessageServiceFixture()
		f.calls.calls[f.callID].Status = entity.CallStatusEnded
		if _, err := f.svc.SendCallMessage(ctx, f.workspaceID, f.callID, f.senderID, "hello"); !hasCode(err, cerrors.CodeForbidden) {
			t.Fatalf("SendCallMessage ended call error = %v, want FORBIDDEN", err)
		}
	})
}

type callMessageServiceFixture struct {
	workspaceID uuid.UUID
	callID      uuid.UUID
	senderID    uuid.UUID
	hostID      uuid.UUID
	otherID     uuid.UUID
	calls       *fakeCallRepo
	messages    *fakeCallMessageRepo
	svc         *Service
}

func newCallMessageServiceFixture() callMessageServiceFixture {
	workspaceID := uuid.New()
	callID := uuid.New()
	senderID := uuid.New()
	hostID := uuid.New()
	otherID := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, senderID}: {WorkspaceID: workspaceID, UserID: senderID, Role: entity.WorkspaceRoleMember},
		{workspaceID, hostID}:   {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
		{workspaceID, otherID}:  {WorkspaceID: workspaceID, UserID: otherID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeMeeting,
				Status:      entity.CallStatusActive,
				Settings:    entity.CallSettings{Chat: true},
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, senderID}: {ID: uuid.New(), CallID: callID, UserID: senderID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
			{callID, hostID}:   {ID: uuid.New(), CallID: callID, UserID: hostID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
			{callID, otherID}:  {ID: uuid.New(), CallID: callID, UserID: otherID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
		},
	}
	messages := &fakeCallMessageRepo{messages: map[uuid.UUID]*entity.CallMessage{}}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	svc.SetCallMessageRepo(messages)

	return callMessageServiceFixture{
		workspaceID: workspaceID,
		callID:      callID,
		senderID:    senderID,
		hostID:      hostID,
		otherID:     otherID,
		calls:       calls,
		messages:    messages,
		svc:         svc,
	}
}

type fakeCallMessageRepo struct {
	messages   map[uuid.UUID]*entity.CallMessage
	listCallID uuid.UUID
	listParams pagination.Params
}

func (r *fakeCallMessageRepo) Create(_ context.Context, msg *entity.CallMessage) error {
	if r.messages == nil {
		r.messages = map[uuid.UUID]*entity.CallMessage{}
	}
	copy := *msg
	r.messages[msg.ID] = &copy
	return nil
}

func (r *fakeCallMessageRepo) ListByCall(_ context.Context, callID uuid.UUID, p pagination.Params) ([]entity.CallMessage, error) {
	r.listCallID = callID
	r.listParams = p
	var items []entity.CallMessage
	for _, msg := range r.messages {
		if msg.CallID == callID && msg.DeletedAt == nil {
			items = append(items, *msg)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].ID.String() > items[j].ID.String()
	})
	return items, nil
}

func (r *fakeCallMessageRepo) SoftDelete(_ context.Context, id, callID uuid.UUID) error {
	msg := r.messages[id]
	if msg == nil || msg.CallID != callID {
		return nil
	}
	now := time.Now().UTC()
	msg.DeletedAt = &now
	return nil
}

func (r *fakeCallMessageRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.CallMessage, error) {
	if msg := r.messages[id]; msg != nil {
		copy := *msg
		return &copy, nil
	}
	return nil, cerrors.NotFound("call message not found")
}

type fakeCallMessageTxManager struct {
	scope txscope.Scope
	calls int
}

func (m *fakeCallMessageTxManager) WithinTx(ctx context.Context, fn func(context.Context, txscope.Scope) error) error {
	m.calls++
	return fn(ctx, m.scope)
}

type fakeCallMessageTxScope struct {
	callMessages repository.CallMessageRepository
	events       []eventpkg.Event
}

func (s *fakeCallMessageTxScope) Users() repository.UserRepository                       { return nil }
func (s *fakeCallMessageTxScope) Workspaces() repository.WorkspaceRepository             { return nil }
func (s *fakeCallMessageTxScope) Messages() repository.MessageRepository                 { return nil }
func (s *fakeCallMessageTxScope) Files() repository.FileRepository                       { return nil }
func (s *fakeCallMessageTxScope) Channels() repository.ChannelRepository                 { return nil }
func (s *fakeCallMessageTxScope) ChannelGrants() repository.ChannelAccessGrantRepository { return nil }
func (s *fakeCallMessageTxScope) Calls() repository.CallRepository                       { return nil }
func (s *fakeCallMessageTxScope) CallMessages() repository.CallMessageRepository {
	return s.callMessages
}
func (s *fakeCallMessageTxScope) Calendars() repository.CalendarRepository      { return nil }
func (s *fakeCallMessageTxScope) Recordings() repository.RecordingRepository    { return nil }
func (s *fakeCallMessageTxScope) Invites() repository.GuestInviteRepository     { return nil }
func (s *fakeCallMessageTxScope) GuestGrants() repository.GuestAccessRepository { return nil }
func (s *fakeCallMessageTxScope) Roles() repository.WorkspaceRoleRepository     { return nil }
func (s *fakeCallMessageTxScope) Audit() repository.AuditRepository             { return nil }
func (s *fakeCallMessageTxScope) SearchIndexer() searchsvc.Indexer              { return nil }
func (s *fakeCallMessageTxScope) EnqueueRealtime(_ context.Context, evt eventpkg.Event, _ []byte) error {
	s.events = append(s.events, evt)
	return nil
}
