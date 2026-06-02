package call

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/pkg/cerrors"
)

type callInteractionEvent struct {
	Type             event.Type             `json:"type"`
	Subject          string                 `json:"subject"`
	WorkspaceID      uuid.UUID              `json:"workspace_id"`
	UserID           uuid.UUID              `json:"user_id"`
	DeliverySemantic event.DeliverySemantic `json:"delivery_semantic"`
	Replayable       bool                   `json:"replayable"`
	Payload          json.RawMessage        `json:"payload"`
}

func TestCallHandInteractionsPublishEphemeralEvents(t *testing.T) {
	for _, tc := range []struct {
		name    string
		action  func(context.Context, *Service, uuid.UUID, uuid.UUID, uuid.UUID) error
		evtType event.Type
	}{
		{
			name: "raise hand",
			action: func(ctx context.Context, svc *Service, workspaceID, callID, userID uuid.UUID) error {
				return svc.RaiseHand(ctx, workspaceID, callID, userID)
			},
			evtType: event.TypeCallHandRaised,
		},
		{
			name: "lower hand",
			action: func(ctx context.Context, svc *Service, workspaceID, callID, userID uuid.UUID) error {
				return svc.LowerHand(ctx, workspaceID, callID, userID)
			},
			evtType: event.TypeCallHandLowered,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			workspaceID := uuid.New()
			callID := uuid.New()
			actorID := uuid.New()
			recipientID := uuid.New()
			disconnectedID := uuid.New()
			svc, pub := newCallInteractionTestService(workspaceID, callID, actorID, recipientID, disconnectedID, entity.CallStatusActive, entity.ParticipantStatusConnected)

			if err := tc.action(ctx, svc, workspaceID, callID, actorID); err != nil {
				t.Fatalf("%s returned error: %v", tc.name, err)
			}

			if len(pub.captures) != 2 {
				t.Fatalf("published events = %d, want 2", len(pub.captures))
			}
			assertHandPublish(t, pub.captures, workspaceID, callID, actorID, []uuid.UUID{actorID, recipientID}, tc.evtType)
		})
	}
}

func TestCallHandInteractionsRequireActiveConnectedParticipant(t *testing.T) {
	for _, tc := range []struct {
		name             string
		action           func(context.Context, *Service, uuid.UUID, uuid.UUID, uuid.UUID) error
		callStatus       entity.CallStatus
		participantState entity.ParticipantStatus
		removeActor      bool
		wantMessage      string
	}{
		{
			name: "raise hand disconnected",
			action: func(ctx context.Context, svc *Service, workspaceID, callID, userID uuid.UUID) error {
				return svc.RaiseHand(ctx, workspaceID, callID, userID)
			},
			callStatus:       entity.CallStatusActive,
			participantState: entity.ParticipantStatusDisconnected,
			wantMessage:      "not in call",
		},
		{
			name: "raise hand not participant",
			action: func(ctx context.Context, svc *Service, workspaceID, callID, userID uuid.UUID) error {
				return svc.RaiseHand(ctx, workspaceID, callID, userID)
			},
			callStatus:       entity.CallStatusActive,
			participantState: entity.ParticipantStatusConnected,
			removeActor:      true,
			wantMessage:      "not in call",
		},
		{
			name: "raise hand ended call",
			action: func(ctx context.Context, svc *Service, workspaceID, callID, userID uuid.UUID) error {
				return svc.RaiseHand(ctx, workspaceID, callID, userID)
			},
			callStatus:       entity.CallStatusEnded,
			participantState: entity.ParticipantStatusConnected,
			wantMessage:      "call is not active",
		},
		{
			name: "lower hand disconnected",
			action: func(ctx context.Context, svc *Service, workspaceID, callID, userID uuid.UUID) error {
				return svc.LowerHand(ctx, workspaceID, callID, userID)
			},
			callStatus:       entity.CallStatusActive,
			participantState: entity.ParticipantStatusDisconnected,
			wantMessage:      "not in call",
		},
		{
			name: "lower hand not participant",
			action: func(ctx context.Context, svc *Service, workspaceID, callID, userID uuid.UUID) error {
				return svc.LowerHand(ctx, workspaceID, callID, userID)
			},
			callStatus:       entity.CallStatusActive,
			participantState: entity.ParticipantStatusConnected,
			removeActor:      true,
			wantMessage:      "not in call",
		},
		{
			name: "lower hand ended call",
			action: func(ctx context.Context, svc *Service, workspaceID, callID, userID uuid.UUID) error {
				return svc.LowerHand(ctx, workspaceID, callID, userID)
			},
			callStatus:       entity.CallStatusEnded,
			participantState: entity.ParticipantStatusConnected,
			wantMessage:      "call is not active",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			workspaceID := uuid.New()
			callID := uuid.New()
			actorID := uuid.New()
			recipientID := uuid.New()
			svc, pub := newCallInteractionTestService(workspaceID, callID, actorID, recipientID, uuid.New(), tc.callStatus, tc.participantState)
			if tc.removeActor {
				delete(svc.calls.(*fakeCallRepo).participants, [2]uuid.UUID{callID, actorID})
			}

			err := tc.action(ctx, svc, workspaceID, callID, actorID)
			assertAppError(t, err, cerrors.CodeForbidden, tc.wantMessage)
			if len(pub.captures) != 0 {
				t.Fatalf("published events = %d, want 0", len(pub.captures))
			}
		})
	}
}

func TestSendCallReaction(t *testing.T) {
	t.Run("valid emoji", func(t *testing.T) {
		ctx := context.Background()
		workspaceID := uuid.New()
		callID := uuid.New()
		actorID := uuid.New()
		recipientID := uuid.New()
		svc, pub := newCallInteractionTestService(workspaceID, callID, actorID, recipientID, uuid.New(), entity.CallStatusActive, entity.ParticipantStatusConnected)

		if err := svc.SendCallReaction(ctx, workspaceID, callID, actorID, "👍"); err != nil {
			t.Fatalf("SendCallReaction returned error: %v", err)
		}

		if len(pub.captures) != 2 {
			t.Fatalf("published events = %d, want 2", len(pub.captures))
		}
		assertReactionPublish(t, pub.captures, workspaceID, callID, actorID, []uuid.UUID{actorID, recipientID}, "👍")
	})

	for _, tc := range []struct {
		name  string
		emoji string
	}{
		{name: "empty", emoji: ""},
		{name: "too long", emoji: strings.Repeat("a", 33)},
		{name: "invalid utf8", emoji: string([]byte{0xff})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			workspaceID := uuid.New()
			callID := uuid.New()
			actorID := uuid.New()
			svc, pub := newCallInteractionTestService(workspaceID, callID, actorID, uuid.New(), uuid.New(), entity.CallStatusActive, entity.ParticipantStatusConnected)

			err := svc.SendCallReaction(ctx, workspaceID, callID, actorID, tc.emoji)
			assertAppError(t, err, cerrors.CodeInvalidInput, "invalid emoji")
			if len(pub.captures) != 0 {
				t.Fatalf("published events = %d, want 0", len(pub.captures))
			}
		})
	}
}

func newCallInteractionTestService(
	workspaceID, callID, actorID, recipientID, disconnectedID uuid.UUID,
	callStatus entity.CallStatus,
	actorStatus entity.ParticipantStatus,
) (*Service, *capturingPublisher) {
	pub := &capturingPublisher{}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, actorID}:        {WorkspaceID: workspaceID, UserID: actorID, Role: entity.WorkspaceRoleMember},
		{workspaceID, recipientID}:    {WorkspaceID: workspaceID, UserID: recipientID, Role: entity.WorkspaceRoleMember},
		{workspaceID, disconnectedID}: {WorkspaceID: workspaceID, UserID: disconnectedID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: callStatus, CreatedAt: time.Now()},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, actorID}:        {ID: uuid.New(), CallID: callID, UserID: actorID, Role: entity.CallRoleHost, Status: actorStatus},
			{callID, recipientID}:    {ID: uuid.New(), CallID: callID, UserID: recipientID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
			{callID, disconnectedID}: {ID: uuid.New(), CallID: callID, UserID: disconnectedID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusDisconnected},
		},
	}
	return NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, pub, nil, mediaTestConfig(), nil, nil, nil), pub
}

func assertHandPublish(t *testing.T, captures []capturedPublish, workspaceID, callID, actorID uuid.UUID, recipients []uuid.UUID, evtType event.Type) {
	t.Helper()
	events := decodeCapturedInteractionEvents(t, captures)
	for _, recipientID := range recipients {
		env, ok := events["aloqa.signal."+recipientID.String()]
		if !ok {
			t.Fatalf("missing publish for recipient %s", recipientID)
		}
		assertInteractionEnvelope(t, env, workspaceID, recipientID, evtType)

		var payload event.CallHandPayload
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			t.Fatalf("unmarshal hand payload: %v", err)
		}
		if payload.CallID != callID || payload.UserID != actorID {
			t.Fatalf("payload = %+v, want call_id=%s user_id=%s", payload, callID, actorID)
		}
	}
}

func assertReactionPublish(t *testing.T, captures []capturedPublish, workspaceID, callID, actorID uuid.UUID, recipients []uuid.UUID, emoji string) {
	t.Helper()
	events := decodeCapturedInteractionEvents(t, captures)
	for _, recipientID := range recipients {
		env, ok := events["aloqa.signal."+recipientID.String()]
		if !ok {
			t.Fatalf("missing publish for recipient %s", recipientID)
		}
		assertInteractionEnvelope(t, env, workspaceID, recipientID, event.TypeCallReaction)

		var payload event.CallReactionPayload
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			t.Fatalf("unmarshal reaction payload: %v", err)
		}
		if payload.CallID != callID || payload.UserID != actorID || payload.Emoji != emoji {
			t.Fatalf("payload = %+v, want call_id=%s user_id=%s emoji=%q", payload, callID, actorID, emoji)
		}
	}
}

func decodeCapturedInteractionEvents(t *testing.T, captures []capturedPublish) map[string]callInteractionEvent {
	t.Helper()
	events := make(map[string]callInteractionEvent, len(captures))
	for _, capture := range captures {
		var env callInteractionEvent
		if err := json.Unmarshal(capture.body, &env); err != nil {
			t.Fatalf("unmarshal published body for %s: %v", capture.subject, err)
		}
		if env.Subject != capture.subject {
			t.Fatalf("event subject = %q, want %q", env.Subject, capture.subject)
		}
		events[capture.subject] = env
	}
	return events
}

func assertInteractionEnvelope(t *testing.T, env callInteractionEvent, workspaceID, recipientID uuid.UUID, evtType event.Type) {
	t.Helper()
	if env.Type != evtType {
		t.Fatalf("type = %q, want %q", env.Type, evtType)
	}
	if env.WorkspaceID != workspaceID {
		t.Fatalf("workspace_id = %s, want %s", env.WorkspaceID, workspaceID)
	}
	if env.UserID != recipientID {
		t.Fatalf("user_id = %s, want recipient %s", env.UserID, recipientID)
	}
	if env.DeliverySemantic != event.DeliveryEphemeral {
		t.Fatalf("delivery_semantic = %q, want %q", env.DeliverySemantic, event.DeliveryEphemeral)
	}
	if env.Replayable {
		t.Fatalf("replayable = true, want false")
	}
}

func assertAppError(t *testing.T, err error, code cerrors.Code, message string) {
	t.Helper()
	appErr, ok := cerrors.AsAppError(err)
	if !ok {
		t.Fatalf("error = %v, want app error", err)
	}
	if appErr.Code != code || appErr.Message != message {
		t.Fatalf("app error = (%s, %q), want (%s, %q)", appErr.Code, appErr.Message, code, message)
	}
}
