package call

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
)

// TestAuthorizeGuestLinkRequiresHost covers the host/co-host gate on minting a
// call guest link (#2): hosts and co-hosts are allowed, plain members and
// non-participants are rejected with FORBIDDEN, and an ended call is rejected
// with CALL_ENDED before the role check.
func TestAuthorizeGuestLinkRequiresHost(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	coHostID := uuid.New()
	memberID := uuid.New()
	strangerID := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, hostID}:     {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
		{workspaceID, coHostID}:   {WorkspaceID: workspaceID, UserID: coHostID, Role: entity.WorkspaceRoleMember},
		{workspaceID, memberID}:   {WorkspaceID: workspaceID, UserID: memberID, Role: entity.WorkspaceRoleMember},
		{workspaceID, strangerID}: {WorkspaceID: workspaceID, UserID: strangerID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeGroup, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}:   {ID: uuid.New(), CallID: callID, UserID: hostID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
			{callID, coHostID}: {ID: uuid.New(), CallID: callID, UserID: coHostID, Role: entity.CallRoleCoHost, Status: entity.ParticipantStatusConnected},
			{callID, memberID}: {ID: uuid.New(), CallID: callID, UserID: memberID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	if _, err := svc.AuthorizeGuestLink(ctx, workspaceID, callID, hostID); err != nil {
		t.Fatalf("AuthorizeGuestLink host error = %v, want nil", err)
	}
	if _, err := svc.AuthorizeGuestLink(ctx, workspaceID, callID, coHostID); err != nil {
		t.Fatalf("AuthorizeGuestLink co-host error = %v, want nil", err)
	}
	if _, err := svc.AuthorizeGuestLink(ctx, workspaceID, callID, memberID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("AuthorizeGuestLink member error = %v, want FORBIDDEN", err)
	}
	if _, err := svc.AuthorizeGuestLink(ctx, workspaceID, callID, strangerID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("AuthorizeGuestLink non-participant error = %v, want FORBIDDEN", err)
	}

	calls.calls[callID].Status = entity.CallStatusEnded
	if _, err := svc.AuthorizeGuestLink(ctx, workspaceID, callID, hostID); !hasCode(err, cerrors.CodeCallEnded) {
		t.Fatalf("AuthorizeGuestLink ended call error = %v, want CALL_ENDED", err)
	}
}
