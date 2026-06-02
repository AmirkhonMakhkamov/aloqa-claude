package call

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/security/guestaccess"
)

// A call-scoped guest reaches exactly the call they were invited to (any call
// type, incl. channel-less), and nothing else — not another call, not channels,
// not workspace content. (unified guest link)
func TestRequireCallAccessCallScopedGuest(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	guestID := uuid.New()
	ownCallID := uuid.New()
	otherChannelLessCallID := uuid.New()
	channelCallID := uuid.New()
	channelID := uuid.New()

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			ownCallID:              {ID: ownCallID, WorkspaceID: workspaceID, Type: entity.CallTypeGroup, Status: entity.CallStatusActive},
			otherChannelLessCallID: {ID: otherChannelLessCallID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive},
			channelCallID:          {ID: channelCallID, WorkspaceID: workspaceID, ChannelID: &channelID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{},
	}
	channels := &fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
		channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
	}}
	guests := guestaccess.NewChecker(&fakeGuestAccessRepo{grants: []entity.GuestAccessGrant{{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		UserID:      guestID,
		CallID:      &ownCallID,
		ExpiresAt:   time.Now().Add(time.Hour),
	}}})
	// Empty workspace repo: the guest is NOT a member.
	svc := NewService(calls, &fakeBreakoutRepo{}, channels, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), guests, nil)

	// Their own call → allowed.
	if _, err := svc.requireCallAccess(ctx, workspaceID, ownCallID, guestID); err != nil {
		t.Fatalf("requireCallAccess(own call) = %v, want nil", err)
	}

	// A different channel-less call → forbidden.
	if _, err := svc.requireCallAccess(ctx, workspaceID, otherChannelLessCallID, guestID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("requireCallAccess(other channel-less call) = %v, want FORBIDDEN", err)
	}

	// A channel call they were not invited to → forbidden.
	if _, err := svc.requireCallAccess(ctx, workspaceID, channelCallID, guestID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("requireCallAccess(channel call) = %v, want FORBIDDEN", err)
	}

	// isGuestUser stays true so forced-waiting is preserved.
	if !svc.isGuestUser(ctx, workspaceID, guestID) {
		t.Fatalf("isGuestUser(call-scoped guest) = false, want true")
	}
}

// A workspace member is unaffected by the guest branch on both channel and
// channel-less calls.
func TestRequireCallAccessMemberUnaffected(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	memberID := uuid.New()
	channelLessCallID := uuid.New()
	channelCallID := uuid.New()
	channelID := uuid.New()

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			channelLessCallID: {ID: channelLessCallID, WorkspaceID: workspaceID, Type: entity.CallTypeGroup, Status: entity.CallStatusActive},
			channelCallID:     {ID: channelCallID, WorkspaceID: workspaceID, ChannelID: &channelID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{},
	}
	channels := &fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
		channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
	}}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, memberID}: {WorkspaceID: workspaceID, UserID: memberID, Role: entity.WorkspaceRoleMember},
	}}
	// An empty guest checker (no grants) — the member must never enter the guest branch.
	guests := guestaccess.NewChecker(&fakeGuestAccessRepo{})
	svc := NewService(calls, &fakeBreakoutRepo{}, channels, workspaces, noopPublisher{}, nil, mediaTestConfig(), guests, nil)

	if _, err := svc.requireCallAccess(ctx, workspaceID, channelLessCallID, memberID); err != nil {
		t.Fatalf("requireCallAccess(member, channel-less) = %v, want nil", err)
	}
	if _, err := svc.requireCallAccess(ctx, workspaceID, channelCallID, memberID); err != nil {
		t.Fatalf("requireCallAccess(member, channel call) = %v, want nil", err)
	}
	if svc.isGuestUser(ctx, workspaceID, memberID) {
		t.Fatalf("isGuestUser(member) = true, want false")
	}
}
