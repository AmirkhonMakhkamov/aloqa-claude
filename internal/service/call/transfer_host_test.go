package call

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
)

// transferHostFixture builds a meeting call with a connected host and a
// connected participant, plus both as workspace members.
func transferHostFixture(t *testing.T) (*Service, *fakeCallRepo, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	targetID := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, hostID}:   {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
		{workspaceID, targetID}: {WorkspaceID: workspaceID, UserID: targetID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}:   {ID: uuid.New(), CallID: callID, UserID: hostID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
			{callID, targetID}: {ID: uuid.New(), CallID: callID, UserID: targetID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	return svc, calls, workspaceID, callID, hostID, targetID
}

func TestTransferHost_HappyPath_SwapsRolesAtomically(t *testing.T) {
	ctx := context.Background()
	svc, calls, workspaceID, callID, hostID, targetID := transferHostFixture(t)

	if err := svc.TransferHost(ctx, workspaceID, callID, hostID, targetID); err != nil {
		t.Fatalf("TransferHost returned error: %v", err)
	}
	if got := calls.participants[[2]uuid.UUID{callID, hostID}].Role; got != entity.CallRoleParticipant {
		t.Fatalf("old host role = %q, want participant (full transfer demotes old host)", got)
	}
	if got := calls.participants[[2]uuid.UUID{callID, targetID}].Role; got != entity.CallRoleHost {
		t.Fatalf("target role = %q, want host", got)
	}
}

func TestTransferHost_NonHostActor_Forbidden(t *testing.T) {
	ctx := context.Background()
	svc, calls, workspaceID, callID, hostID, targetID := transferHostFixture(t)
	// Demote the actor so they are a plain participant, not the host.
	calls.participants[[2]uuid.UUID{callID, hostID}].Role = entity.CallRoleParticipant

	if err := svc.TransferHost(ctx, workspaceID, callID, hostID, targetID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("TransferHost by non-host error = %v, want FORBIDDEN", err)
	}
}

func TestTransferHost_TargetNotConnected_InvalidInput(t *testing.T) {
	ctx := context.Background()
	svc, calls, workspaceID, callID, hostID, targetID := transferHostFixture(t)
	calls.participants[[2]uuid.UUID{callID, targetID}].Status = entity.ParticipantStatusDisconnected

	if err := svc.TransferHost(ctx, workspaceID, callID, hostID, targetID); !hasCode(err, cerrors.CodeInvalidInput) {
		t.Fatalf("TransferHost to disconnected target error = %v, want INVALID_INPUT", err)
	}
}

func TestTransferHost_TargetAlreadyHost_Conflict(t *testing.T) {
	ctx := context.Background()
	svc, calls, workspaceID, callID, hostID, targetID := transferHostFixture(t)
	calls.participants[[2]uuid.UUID{callID, targetID}].Role = entity.CallRoleHost

	if err := svc.TransferHost(ctx, workspaceID, callID, hostID, targetID); !hasCode(err, cerrors.CodeConflict) {
		t.Fatalf("TransferHost to existing host error = %v, want CONFLICT", err)
	}
}

func TestTransferHost_Self_InvalidInput(t *testing.T) {
	ctx := context.Background()
	svc, _, workspaceID, callID, hostID, _ := transferHostFixture(t)

	if err := svc.TransferHost(ctx, workspaceID, callID, hostID, hostID); !hasCode(err, cerrors.CodeInvalidInput) {
		t.Fatalf("TransferHost to self error = %v, want INVALID_INPUT", err)
	}
}

// ALK-695: a connected participant of a DM-linked call must keep call access
// (chat/roster/media) even when they are not a member row of the underlying DM
// channel. Without the short-circuit, requireCallAccess would 403 them.
func TestRequireCallAccess_ConnectedParticipantBypassesDMChannelGate(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	channelID := uuid.New()
	userID := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, ChannelID: &channelID, Type: entity.CallTypeOneToOne, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {ID: uuid.New(), CallID: callID, UserID: userID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
		},
	}
	// DM channel where the user is NOT a member row → channel gate would deny.
	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypeDM},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, channels, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	if err := svc.CanAccessCall(ctx, workspaceID, callID, userID); err != nil {
		t.Fatalf("CanAccessCall connected participant error = %v, want nil (connected participant bypasses DM channel gate)", err)
	}
}

// Contrast: a non-connected participant who is not a channel member must still
// be denied — the short-circuit is gated strictly on `connected`.
func TestRequireCallAccess_DisconnectedParticipantStillGatedByChannel(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	channelID := uuid.New()
	userID := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, ChannelID: &channelID, Type: entity.CallTypeOneToOne, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {ID: uuid.New(), CallID: callID, UserID: userID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusDisconnected},
		},
	}
	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypeDM},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, channels, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	if err := svc.CanAccessCall(ctx, workspaceID, callID, userID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("CanAccessCall disconnected non-member error = %v, want FORBIDDEN", err)
	}
}
