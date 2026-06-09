package call

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/livekit/protocol/auth"
	livekitpb "github.com/livekit/protocol/livekit"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/media/sfu"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
	"aloqa/internal/security/collabaccess"
	"aloqa/internal/security/guestaccess"
)

func TestCallTenantBoundaries(t *testing.T) {
	ctx := context.Background()
	workspaceA := uuid.New()
	workspaceB := uuid.New()
	userID := uuid.New()
	callID := uuid.New()
	channelID := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceA, userID}: {WorkspaceID: workspaceA, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	channels := &fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
		channelID: {ID: channelID, WorkspaceID: &workspaceB, Type: entity.ChannelTypePublic},
	}}
	calls := &fakeCallRepo{calls: map[uuid.UUID]*entity.Call{
		callID: {ID: callID, WorkspaceID: workspaceB, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive},
	}, participants: map[[2]uuid.UUID]*entity.CallParticipant{}}
	svc := NewService(calls, &fakeBreakoutRepo{}, channels, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	if _, err := svc.StartCall(ctx, workspaceA, userID, entity.CallTypeMeeting, "", &channelID, entity.CallSettings{}, ""); !hasCode(err, cerrors.CodeNotFound) {
		t.Fatalf("StartCall with cross-workspace channel error = %v, want NOT_FOUND", err)
	}

	if _, err := svc.JoinCall(ctx, workspaceA, callID, userID, ""); !hasCode(err, cerrors.CodeNotFound) {
		t.Fatalf("JoinCall with cross-workspace call error = %v, want NOT_FOUND", err)
	}
}

func TestViewerCannotPublishMedia(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	callID := uuid.New()
	participantID := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeWebinar,
				Status:      entity.CallStatusActive,
				Settings:    entity.CallSettings{ScreenSharing: true},
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {
				ID:     participantID,
				CallID: callID,
				UserID: userID,
				Role:   entity.CallRoleViewer,
				Status: entity.ParticipantStatusConnected,
			},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	if err := svc.UpdateMedia(ctx, workspaceID, callID, userID, boolPtr(false), boolPtr(true), boolPtr(false)); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("UpdateMedia viewer publish error = %v, want FORBIDDEN", err)
	}
}

// boolPtr is a tiny helper for tests that need to pass a *bool literal into
// the patch-style UpdateMedia signature. Lives in the test file so it stays
// out of production binaries.
func boolPtr(b bool) *bool { return &b }

func intPtr(i int) *int { return &i }

func TestStartCallWithAccessCreatesPrivateCallWithInvitedMembers(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	hostID := uuid.New()
	inviteeID := uuid.New()
	calls := &fakeCallRepo{}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, hostID}:    {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
		{workspaceID, inviteeID}: {WorkspaceID: workspaceID, UserID: inviteeID, Role: entity.WorkspaceRoleMember},
	}}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	callEntity, err := svc.StartCallWithAccess(ctx, workspaceID, hostID, entity.CallTypeMeeting, "Private", nil, entity.CallSettings{}, "", StartCallAccessOptions{
		AccessLevel:      entity.AccessLevelPrivate,
		InvitedMemberIDs: []uuid.UUID{inviteeID, inviteeID},
	})
	if err != nil {
		t.Fatalf("StartCallWithAccess returned error: %v", err)
	}
	if callEntity.AccessLevel != entity.AccessLevelPrivate {
		t.Fatalf("access_level = %q, want private", callEntity.AccessLevel)
	}
	if !calls.invited[callEntity.ID][inviteeID] {
		t.Fatalf("invitee was not inserted into private-call invite list")
	}
}

func TestStartCallWithAccessRejectsInvalidPrivateShapes(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	hostID := uuid.New()
	channelID := uuid.New()
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, hostID}: {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
	}}
	svc := NewService(&fakeCallRepo{}, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	if _, err := svc.StartCallWithAccess(ctx, workspaceID, hostID, entity.CallTypeMeeting, "", &channelID, entity.CallSettings{}, "", StartCallAccessOptions{AccessLevel: entity.AccessLevelPrivate}); !hasCode(err, cerrors.CodeInvalidInput) {
		t.Fatalf("private channel-attached StartCall error = %v, want INVALID_INPUT", err)
	}
	if _, err := svc.StartCallWithAccess(ctx, workspaceID, hostID, entity.CallTypeOneToOne, "", nil, entity.CallSettings{}, "", StartCallAccessOptions{AccessLevel: entity.AccessLevelPrivate}); !hasCode(err, cerrors.CodeInvalidInput) {
		t.Fatalf("private one_to_one StartCall error = %v, want INVALID_INPUT", err)
	}
}

func TestPrivateChannelLessJoinRequiresInvite(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	inviteeID := uuid.New()
	strangerID := uuid.New()
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, hostID}:     {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
		{workspaceID, inviteeID}:  {WorkspaceID: workspaceID, UserID: inviteeID, Role: entity.WorkspaceRoleMember},
		{workspaceID, strangerID}: {WorkspaceID: workspaceID, UserID: strangerID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive, CreatedBy: hostID, AccessLevel: entity.AccessLevelPrivate},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: {ID: uuid.New(), CallID: callID, UserID: hostID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
		},
		invited: map[uuid.UUID]map[uuid.UUID]bool{callID: {inviteeID: true}},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	if _, err := svc.JoinCall(ctx, workspaceID, callID, strangerID, ""); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("uninvited JoinCall error = %v, want FORBIDDEN", err)
	}
	participant, err := svc.JoinCall(ctx, workspaceID, callID, inviteeID, "")
	if err != nil {
		t.Fatalf("invited JoinCall returned error: %v", err)
	}
	if participant.UserID != inviteeID || participant.Status != entity.ParticipantStatusConnected {
		t.Fatalf("participant = %+v, want invited connected participant", participant)
	}
}

func TestPrivateChannelLessRejectsGuestGrantForOtherCall(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	otherCallID := uuid.New()
	hostID := uuid.New()
	guestID := uuid.New()
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive, CreatedBy: hostID, AccessLevel: entity.AccessLevelPrivate},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{},
	}
	guests := guestaccess.NewChecker(&fakeGuestAccessRepo{grants: []entity.GuestAccessGrant{{
		WorkspaceID: workspaceID,
		UserID:      guestID,
		CallID:      &otherCallID,
		ExpiresAt:   time.Now().Add(time.Hour),
	}}})
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), guests, nil)

	if _, err := svc.JoinCall(ctx, workspaceID, callID, guestID, ""); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("guest grant for another call JoinCall error = %v, want FORBIDDEN", err)
	}
}

func TestListActiveCallsHidesUnauthorizedPrivateChannelLessCalls(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	publicCallID := uuid.New()
	privateCallID := uuid.New()
	userID := uuid.New()
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			publicCallID:  {ID: publicCallID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive, AccessLevel: entity.AccessLevelPublic},
			privateCallID: {ID: privateCallID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive, AccessLevel: entity.AccessLevelPrivate},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	active, err := svc.ListActiveCalls(ctx, workspaceID, userID)
	if err != nil {
		t.Fatalf("ListActiveCalls returned error: %v", err)
	}
	if len(active) != 1 || active[0].ID != publicCallID {
		t.Fatalf("active calls = %+v, want only public call", active)
	}

	calls.invited = map[uuid.UUID]map[uuid.UUID]bool{privateCallID: {userID: true}}
	active, err = svc.ListActiveCalls(ctx, workspaceID, userID)
	if err != nil {
		t.Fatalf("ListActiveCalls invited returned error: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("active calls count = %d, want 2 after invite", len(active))
	}
}

func TestListRecentCallsHidesUnauthorizedPrivateChannelLessCalls(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	publicCallID := uuid.New()
	privateCallID := uuid.New()
	userID := uuid.New()
	now := time.Now().UTC()
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			publicCallID: {
				ID:          publicCallID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeMeeting,
				Status:      entity.CallStatusEnded,
				AccessLevel: entity.AccessLevelPublic,
				CreatedAt:   now.Add(-time.Minute),
			},
			privateCallID: {
				ID:          privateCallID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeMeeting,
				Status:      entity.CallStatusEnded,
				AccessLevel: entity.AccessLevelPrivate,
				CreatedAt:   now,
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	recent, err := svc.ListRecentCalls(ctx, workspaceID, userID, 20, "")
	if err != nil {
		t.Fatalf("ListRecentCalls returned error: %v", err)
	}
	if len(recent.Calls) != 1 || recent.Calls[0].ID != publicCallID {
		t.Fatalf("recent calls = %+v, want only public call", recent.Calls)
	}

	calls.invited = map[uuid.UUID]map[uuid.UUID]bool{privateCallID: {userID: true}}
	recent, err = svc.ListRecentCalls(ctx, workspaceID, userID, 20, "")
	if err != nil {
		t.Fatalf("ListRecentCalls invited returned error: %v", err)
	}
	if len(recent.Calls) != 2 {
		t.Fatalf("recent calls count = %d, want 2 after invite", len(recent.Calls))
	}
}

func TestPrivateCallEventPublishesOnlyUserScopedSubjects(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	memberID := uuid.New()
	inviteeID := uuid.New()
	pub := &capturingPublisher{}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeMeeting,
				Status:      entity.CallStatusActive,
				CreatedBy:   hostID,
				AccessLevel: entity.AccessLevelPrivate,
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}:   connectedParticipant(callID, hostID, entity.CallRoleHost),
			{callID, memberID}: connectedParticipant(callID, memberID, entity.CallRoleParticipant),
		},
		invited: map[uuid.UUID]map[uuid.UUID]bool{callID: {inviteeID: true}},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	svc.publishCallEvent(ctx, event.TypeCallStarted, calls.calls[callID], hostID)

	subjects := make([]string, 0, len(pub.captures))
	for _, capture := range pub.captures {
		if capture.subject == workspaceSubject(workspaceID) {
			t.Fatalf("private call event leaked to workspace subject")
		}
		subjects = append(subjects, capture.subject)
	}
	sort.Strings(subjects)
	want := []string{
		workspaceUserEventsSubject(workspaceID, hostID),
		workspaceUserEventsSubject(workspaceID, inviteeID),
		workspaceUserEventsSubject(workspaceID, memberID),
	}
	sort.Strings(want)
	if len(subjects) != len(want) {
		t.Fatalf("subjects = %+v, want %+v", subjects, want)
	}
	for i := range want {
		if subjects[i] != want[i] {
			t.Fatalf("subjects = %+v, want %+v", subjects, want)
		}
	}
}

func TestAuthorizeGuestLinkHonorsWhoCanAddGuestsPolicy(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	publicCallID := uuid.New()
	privateCallID := uuid.New()
	memberID := uuid.New()
	hostID := uuid.New()
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, memberID}: {WorkspaceID: workspaceID, UserID: memberID, Role: entity.WorkspaceRoleMember},
		{workspaceID, hostID}:   {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			publicCallID: {
				ID:          publicCallID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeMeeting,
				Status:      entity.CallStatusActive,
				CreatedBy:   hostID,
				AccessLevel: entity.AccessLevelPublic,
				Settings:    entity.CallSettings{WhoCanAddGuests: entity.WhoCanAddGuestsEveryone},
			},
			privateCallID: {
				ID:          privateCallID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeMeeting,
				Status:      entity.CallStatusActive,
				CreatedBy:   hostID,
				AccessLevel: entity.AccessLevelPrivate,
				Settings:    entity.CallSettings{WhoCanAddGuests: entity.WhoCanAddGuestsEveryone},
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{publicCallID, memberID}:  {ID: uuid.New(), CallID: publicCallID, UserID: memberID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
			{privateCallID, memberID}: {ID: uuid.New(), CallID: privateCallID, UserID: memberID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
			{privateCallID, hostID}:   {ID: uuid.New(), CallID: privateCallID, UserID: hostID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	if _, err := svc.AuthorizeGuestLink(ctx, workspaceID, publicCallID, memberID); err != nil {
		t.Fatalf("public/everyone member AuthorizeGuestLink returned error: %v", err)
	}
	if _, err := svc.AuthorizeGuestLink(ctx, workspaceID, privateCallID, memberID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("private/everyone member AuthorizeGuestLink error = %v, want FORBIDDEN", err)
	}
	if _, err := svc.AuthorizeGuestLink(ctx, workspaceID, privateCallID, hostID); err != nil {
		t.Fatalf("private host AuthorizeGuestLink returned error: %v", err)
	}
}

func TestForwardSignalRequiresBothParticipants(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	fromUser := uuid.New()
	toUser := uuid.New()
	pub := &capturingPublisher{}

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, fromUser}: {ID: uuid.New(), CallID: callID, UserID: fromUser, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	err := svc.ForwardSignal(ctx, callID, fromUser, toUser, "offer", event.SignalPayload{SDP: "v=0"})
	if !hasCode(err, cerrors.CodeNotFound) {
		t.Fatalf("ForwardSignal missing recipient error = %v, want NOT_FOUND", err)
	}
	if pub.called {
		t.Fatalf("signal was published even though recipient was not a participant")
	}

	calls.participants[[2]uuid.UUID{callID, toUser}] = &entity.CallParticipant{ID: uuid.New(), CallID: callID, UserID: toUser, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected}
	if err := svc.ForwardSignal(ctx, callID, fromUser, toUser, "offer", event.SignalPayload{SDP: "v=0"}); err != nil {
		t.Fatalf("ForwardSignal returned error: %v", err)
	}
	if !pub.called || pub.subject != "aloqa.signal."+toUser.String() {
		t.Fatalf("published subject = %q, called=%v", pub.subject, pub.called)
	}
}

func TestHostCanPromoteWebinarViewerToPresenter(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	viewerID := uuid.New()
	viewerParticipantID := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, hostID}:   {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
		{workspaceID, viewerID}: {WorkspaceID: workspaceID, UserID: viewerID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeWebinar, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}:   {ID: uuid.New(), CallID: callID, UserID: hostID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
			{callID, viewerID}: {ID: viewerParticipantID, CallID: callID, UserID: viewerID, Role: entity.CallRoleViewer, Status: entity.ParticipantStatusConnected},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	if err := svc.UpdateParticipantRole(ctx, workspaceID, callID, hostID, viewerID, entity.CallRolePresenter); err != nil {
		t.Fatalf("UpdateParticipantRole returned error: %v", err)
	}
	if got := calls.participants[[2]uuid.UUID{callID, viewerID}].Role; got != entity.CallRolePresenter {
		t.Fatalf("viewer role = %q, want presenter", got)
	}
}

func TestScreenShareCapacityIsEnforced(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userA := uuid.New()
	userB := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userA}: {WorkspaceID: workspaceID, UserID: userA, Role: entity.WorkspaceRoleMember},
		{workspaceID, userB}: {WorkspaceID: workspaceID, UserID: userB, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive, Settings: entity.CallSettings{ScreenSharing: true}},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userA}: {ID: uuid.New(), CallID: callID, UserID: userA, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected, ScreenSharing: true},
			{callID, userB}: {ID: uuid.New(), CallID: callID, UserID: userB, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	if err := svc.UpdateMedia(ctx, workspaceID, callID, userB, boolPtr(true), boolPtr(true), boolPtr(true)); !hasCode(err, cerrors.CodeConflict) {
		t.Fatalf("UpdateMedia second screen share error = %v, want CONFLICT", err)
	}
}

func TestGuestGrantAllowsJoiningChannelScopedCall(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	channelID := uuid.New()
	guestID := uuid.New()

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, ChannelID: &channelID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{},
	}
	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePrivate},
		},
	}
	guests := guestaccess.NewChecker(&fakeGuestAccessRepo{grants: []entity.GuestAccessGrant{{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		UserID:      guestID,
		ChannelIDs:  []uuid.UUID{channelID},
		ExpiresAt:   time.Now().Add(time.Hour),
	}}})
	svc := NewService(calls, &fakeBreakoutRepo{}, channels, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), guests, nil)

	participant, err := svc.JoinCall(ctx, workspaceID, callID, guestID, "")
	if err != nil {
		t.Fatalf("JoinCall guest returned error: %v", err)
	}
	if participant == nil || participant.UserID != guestID {
		t.Fatalf("expected guest participant to be created")
	}
}

// 9b: one active call per channel — starting a second is rejected so the FE can
// join the existing one.
func TestStartCallRejectsSecondCallInChannel(t *testing.T) {
	ctx := context.Background()
	workspaceID, channelID := uuid.New(), uuid.New()
	hostID, existingCallID := uuid.New(), uuid.New()
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, hostID}: {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
	}}
	channels := &fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
		channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			existingCallID: {ID: existingCallID, WorkspaceID: workspaceID, ChannelID: &channelID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, channels, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	if _, err := svc.StartCall(ctx, workspaceID, hostID, entity.CallTypeMeeting, "", &channelID, entity.CallSettings{}, ""); !hasCode(err, cerrors.CodeChannelCallExists) {
		t.Fatalf("StartCall in a busy channel = %v, want CHANNEL_ALREADY_HAS_ACTIVE_CALL", err)
	}
}

// 9c: a user may be in only one call at a time — starting a new call while
// connected to another is rejected.
func TestStartCallRejectsUserAlreadyInAnotherCall(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID, otherCallID := uuid.New(), uuid.New()
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			otherCallID: {ID: otherCallID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{otherCallID, userID}: {ID: uuid.New(), CallID: otherCallID, UserID: userID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	if _, err := svc.StartCall(ctx, workspaceID, userID, entity.CallTypeMeeting, "", nil, entity.CallSettings{}, ""); !hasCode(err, cerrors.CodeUserInCall) {
		t.Fatalf("StartCall while already in another call = %v, want USER_ALREADY_IN_CALL", err)
	}
}

func TestCrossWorkspaceDMMemberCanJoinSharedChannelCall(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	channelID := uuid.New()
	hostID := uuid.New()
	remoteUserID := uuid.New()

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, ChannelID: &channelID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: {ID: uuid.New(), CallID: callID, UserID: hostID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
		},
	}
	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypeDM},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, hostID}:       {ChannelID: channelID, UserID: hostID},
			{channelID, remoteUserID}: {ChannelID: channelID, UserID: remoteUserID},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, hostID}: {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
	}}
	svc := NewService(calls, &fakeBreakoutRepo{}, channels, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, fakeCallCollabChecker{
		decision: collabaccess.Decision{Managed: true, Allowed: true},
	})

	participant, err := svc.JoinCall(ctx, workspaceID, callID, remoteUserID, "")
	if err != nil {
		t.Fatalf("JoinCall remote collaboration user returned error: %v", err)
	}
	if participant == nil || participant.UserID != remoteUserID {
		t.Fatalf("expected cross-workspace participant to be created")
	}
}

func TestCrossWorkspaceDMMemberCannotJoinCallWhenSharedCallsRevoked(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	channelID := uuid.New()
	hostID := uuid.New()
	remoteUserID := uuid.New()

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, ChannelID: &channelID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: {ID: uuid.New(), CallID: callID, UserID: hostID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
		},
	}
	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypeDM},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, hostID}:       {ChannelID: channelID, UserID: hostID},
			{channelID, remoteUserID}: {ChannelID: channelID, UserID: remoteUserID},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, hostID}: {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
	}}
	svc := NewService(calls, &fakeBreakoutRepo{}, channels, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, fakeCallCollabChecker{
		decision: collabaccess.Decision{Managed: true, Allowed: false},
	})

	if _, err := svc.JoinCall(ctx, workspaceID, callID, remoteUserID, ""); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("JoinCall revoked collaboration error = %v, want FORBIDDEN", err)
	}
}

func TestValidateQualityReportRejectsInvalidMetrics(t *testing.T) {
	tests := []struct {
		name  string
		input MediaQualityReportInput
	}{
		{name: "missing stream", input: MediaQualityReportInput{}},
		{name: "negative bitrate", input: MediaQualityReportInput{StreamID: "camera", AvailableBitrateKbps: -1}},
		{name: "invalid packet loss", input: MediaQualityReportInput{StreamID: "camera", PacketLossPct: 101}},
		{name: "negative rtt", input: MediaQualityReportInput{StreamID: "camera", RoundTripTimeMs: -1}},
		{name: "negative counter", input: MediaQualityReportInput{StreamID: "camera", NACKCountDelta: -1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateQualityReport(tt.input); !hasCode(err, cerrors.CodeInvalidInput) {
				t.Fatalf("validateQualityReport error = %v, want INVALID_INPUT", err)
			}
		})
	}
}

func TestJoinCallHonorsPolicyParticipantCap(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userA := uuid.New()
	userB := uuid.New()
	userC := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userA}: {WorkspaceID: workspaceID, UserID: userA, Role: entity.WorkspaceRoleMember},
		{workspaceID, userB}: {WorkspaceID: workspaceID, UserID: userB, Role: entity.WorkspaceRoleMember},
		{workspaceID, userC}: {WorkspaceID: workspaceID, UserID: userC, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeOneToOne, Status: entity.CallStatusActive, Settings: entity.CallSettings{MaxParticipants: 10}},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userA}: {ID: uuid.New(), CallID: callID, UserID: userA, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
			{callID, userB}: {ID: uuid.New(), CallID: callID, UserID: userB, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	svc.SetMediaControlPlane(&fakeMediaControlPlane{
		localNodeID: "edge-a",
		policy: entity.MediaCallPolicy{
			MaxParticipants:     2,
			MaxPresenters:       2,
			MaxViewers:          0,
			RoutingMode:         entity.MediaRoutingStickyEdge,
			FanoutStrategy:      entity.MediaFanoutSingleNode,
			OverflowPolicy:      entity.MediaOverflowReject,
			ScreenSharePriority: entity.MediaScreenShareBalanced,
			TURNStrategy:        "regional_turn_pool",
			Sticky:              true,
		},
	})

	if _, err := svc.JoinCall(ctx, workspaceID, callID, userC, ""); !hasCode(err, cerrors.CodeConflict) {
		t.Fatalf("JoinCall cap error = %v, want CONFLICT", err)
	}
}

func TestJoinCallKeepsExistingWaitingParticipantWaiting(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()
	participantID := uuid.New()
	pub := &capturingPublisher{}

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeMeeting,
				Status:      entity.CallStatusRinging,
				Settings:    entity.CallSettings{WaitingRoom: true},
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {
				ID:     participantID,
				CallID: callID,
				UserID: userID,
				Role:   entity.CallRoleParticipant,
				Status: entity.ParticipantStatusWaiting,
			},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, pub, nil, mediaTestConfig(), nil, nil)

	participant, err := svc.JoinCall(ctx, workspaceID, callID, userID, "")
	if err != nil {
		t.Fatalf("JoinCall returned error: %v", err)
	}

	if participant.Status != entity.ParticipantStatusWaiting {
		t.Fatalf("participant status = %q, want %q", participant.Status, entity.ParticipantStatusWaiting)
	}
	if got := calls.participants[[2]uuid.UUID{callID, userID}].Status; got != entity.ParticipantStatusWaiting {
		t.Fatalf("stored participant status = %q, want %q", got, entity.ParticipantStatusWaiting)
	}
	if pub.called {
		t.Fatalf("waiting participant rejoin published %q; want no event", pub.subject)
	}
}

func TestInviteParticipantsFreshMemberPublishesCallInvited(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	targetID := uuid.New()
	pub := &capturingPublisher{}
	svc, calls := inviteParticipantsFixture(workspaceID, callID, hostID, entity.CallRoleHost, targetID, nil, pub)

	result, err := svc.InviteParticipants(ctx, workspaceID, callID, hostID, []uuid.UUID{targetID})
	if err != nil {
		t.Fatalf("InviteParticipants returned error: %v", err)
	}
	if len(result.Invited) != 1 || result.Invited[0].UserID != targetID {
		t.Fatalf("invited = %+v, want target", result.Invited)
	}
	if len(result.Skipped) != 0 {
		t.Fatalf("skipped = %+v, want empty", result.Skipped)
	}
	stored := calls.participants[[2]uuid.UUID{callID, targetID}]
	if stored == nil || stored.Status != entity.ParticipantStatusInvited || stored.Role != entity.CallRoleParticipant {
		t.Fatalf("stored participant = %+v, want invited participant", stored)
	}
	if got := countBreakoutEvents(t, pub.captures, event.TypeCallInvited); got != 1 {
		t.Fatalf("call.invited events = %d, want 1", got)
	}
	if got := countBreakoutEvents(t, pub.captures, event.TypeCallParticipantUpdated); got != 0 {
		t.Fatalf("durable/presence participant updates = %d, want 0", got)
	}
	if pub.subject != "aloqa.signal."+targetID.String() {
		t.Fatalf("publish subject = %q, want per-user signal subject", pub.subject)
	}
}

func TestInviteParticipantsExistingStatusTransitionTable(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		status     entity.ParticipantStatus
		wantStatus entity.ParticipantStatus
		wantSkip   string
		wantRings  int
	}{
		{status: entity.ParticipantStatusConnected, wantStatus: entity.ParticipantStatusConnected, wantSkip: "already_connected"},
		{status: entity.ParticipantStatusJoining, wantStatus: entity.ParticipantStatusJoining, wantSkip: "joining"},
		{status: entity.ParticipantStatusWaiting, wantStatus: entity.ParticipantStatusWaiting, wantSkip: "waiting"},
		{status: entity.ParticipantStatusInvited, wantStatus: entity.ParticipantStatusInvited, wantRings: 1},
		{status: entity.ParticipantStatusDeclined, wantStatus: entity.ParticipantStatusInvited, wantRings: 1},
		{status: entity.ParticipantStatusDisconnected, wantStatus: entity.ParticipantStatusInvited, wantRings: 1},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			workspaceID := uuid.New()
			callID := uuid.New()
			hostID := uuid.New()
			targetID := uuid.New()
			pub := &capturingPublisher{}
			status := tc.status
			svc, calls := inviteParticipantsFixture(workspaceID, callID, hostID, entity.CallRoleHost, targetID, &status, pub)

			result, err := svc.InviteParticipants(ctx, workspaceID, callID, hostID, []uuid.UUID{targetID})
			if err != nil {
				t.Fatalf("InviteParticipants returned error: %v", err)
			}
			stored := calls.participants[[2]uuid.UUID{callID, targetID}]
			if stored.Status != tc.wantStatus {
				t.Fatalf("stored status = %q, want %q", stored.Status, tc.wantStatus)
			}
			if tc.wantSkip != "" {
				if len(result.Skipped) != 1 || result.Skipped[0].Reason != tc.wantSkip || result.Skipped[0].UserID != targetID {
					t.Fatalf("skipped = %+v, want %s for target", result.Skipped, tc.wantSkip)
				}
				if len(result.Invited) != 0 {
					t.Fatalf("invited = %+v, want empty", result.Invited)
				}
			} else if len(result.Invited) != 1 || result.Invited[0].Status != entity.ParticipantStatusInvited {
				t.Fatalf("invited = %+v, want invited target", result.Invited)
			}
			if got := countBreakoutEvents(t, pub.captures, event.TypeCallInvited); got != tc.wantRings {
				t.Fatalf("call.invited events = %d, want %d", got, tc.wantRings)
			}
		})
	}
}

func TestInviteParticipantsAccessGuards(t *testing.T) {
	ctx := context.Background()

	t.Run("non host forbidden", func(t *testing.T) {
		workspaceID := uuid.New()
		callID := uuid.New()
		actorID := uuid.New()
		targetID := uuid.New()
		svc, _ := inviteParticipantsFixture(workspaceID, callID, actorID, entity.CallRoleParticipant, targetID, nil, &capturingPublisher{})

		if _, err := svc.InviteParticipants(ctx, workspaceID, callID, actorID, []uuid.UUID{targetID}); !hasCode(err, cerrors.CodeForbidden) {
			t.Fatalf("InviteParticipants non-host error = %v, want FORBIDDEN", err)
		}
	})

	t.Run("non workspace member is unprocessable", func(t *testing.T) {
		workspaceID := uuid.New()
		callID := uuid.New()
		hostID := uuid.New()
		targetID := uuid.New()
		svc, _ := inviteParticipantsFixture(workspaceID, callID, hostID, entity.CallRoleHost, targetID, nil, &capturingPublisher{})
		delete(svc.members.(*fakeWorkspaceRepo).members, [2]uuid.UUID{workspaceID, targetID})

		if _, err := svc.InviteParticipants(ctx, workspaceID, callID, hostID, []uuid.UUID{targetID}); !hasCode(err, cerrors.CodeUnprocessable) {
			t.Fatalf("InviteParticipants non-member error = %v, want UNPROCESSABLE", err)
		}
	})

	t.Run("workspace member without call access is skipped", func(t *testing.T) {
		workspaceID := uuid.New()
		callID := uuid.New()
		channelID := uuid.New()
		hostID := uuid.New()
		targetID := uuid.New()
		pub := &capturingPublisher{}
		status := entity.ParticipantStatusDisconnected
		svc, _ := inviteParticipantsFixture(workspaceID, callID, hostID, entity.CallRoleHost, targetID, &status, pub)
		calls := svc.calls.(*fakeCallRepo)
		calls.calls[callID].ChannelID = &channelID
		channels := svc.channels.(*fakeChannelRepo)
		channels.channels[channelID] = &entity.Channel{ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePrivate}
		channels.members = map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, hostID}: {ChannelID: channelID, UserID: hostID},
		}

		result, err := svc.InviteParticipants(ctx, workspaceID, callID, hostID, []uuid.UUID{targetID})
		if err != nil {
			t.Fatalf("InviteParticipants returned error: %v", err)
		}
		if len(result.Skipped) != 1 || result.Skipped[0].Reason != "no_access" {
			t.Fatalf("skipped = %+v, want no_access", result.Skipped)
		}
		if got := countBreakoutEvents(t, pub.captures, event.TypeCallInvited); got != 0 {
			t.Fatalf("call.invited events = %d, want 0", got)
		}
	})
}

func TestDeclineCallTransitionsInvitedAndWaitingOnly(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		status    entity.ParticipantStatus
		wantEvent int
		wantState entity.ParticipantStatus
	}{
		{status: entity.ParticipantStatusInvited, wantEvent: 1, wantState: entity.ParticipantStatusDeclined},
		{status: entity.ParticipantStatusWaiting, wantEvent: 1, wantState: entity.ParticipantStatusDeclined},
		{status: entity.ParticipantStatusConnected, wantState: entity.ParticipantStatusConnected},
		{status: entity.ParticipantStatusDisconnected, wantState: entity.ParticipantStatusDisconnected},
		{status: entity.ParticipantStatusDeclined, wantState: entity.ParticipantStatusDeclined},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			workspaceID := uuid.New()
			callID := uuid.New()
			userID := uuid.New()
			pub := &capturingPublisher{}
			status := tc.status
			svc, calls := inviteParticipantsFixture(workspaceID, callID, userID, entity.CallRoleHost, userID, &status, pub)

			if err := svc.DeclineCall(ctx, workspaceID, callID, userID); err != nil {
				t.Fatalf("DeclineCall returned error: %v", err)
			}
			if got := calls.participants[[2]uuid.UUID{callID, userID}].Status; got != tc.wantState {
				t.Fatalf("stored status = %q, want %q", got, tc.wantState)
			}
			if got := countBreakoutEvents(t, pub.captures, event.TypeCallParticipantUpdated); got != tc.wantEvent {
				t.Fatalf("participant updated events = %d, want %d", got, tc.wantEvent)
			}
		})
	}
}

func TestDeclineCallUnknownParticipantNoops(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	targetID := uuid.New()
	svc, calls := inviteParticipantsFixture(workspaceID, callID, hostID, entity.CallRoleHost, targetID, nil, &capturingPublisher{})
	delete(calls.participants, [2]uuid.UUID{callID, targetID})

	if err := svc.DeclineCall(ctx, workspaceID, callID, targetID); err != nil {
		t.Fatalf("DeclineCall unknown participant returned error: %v", err)
	}
}

func TestJoinCallInvitedMemberConnectsDirectlyAndActivatesRinging(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	inviteeID := uuid.New()
	pub := &capturingPublisher{}
	status := entity.ParticipantStatusInvited
	svc, calls := inviteParticipantsFixture(workspaceID, callID, hostID, entity.CallRoleHost, inviteeID, &status, pub)
	calls.calls[callID].Status = entity.CallStatusRinging
	calls.calls[callID].Settings = entity.CallSettings{EntryMode: entity.EntryModeManualAdmit}

	participant, err := svc.JoinCall(ctx, workspaceID, callID, inviteeID, "")
	if err != nil {
		t.Fatalf("JoinCall invited returned error: %v", err)
	}
	if participant.Status != entity.ParticipantStatusConnected {
		t.Fatalf("participant status = %q, want connected", participant.Status)
	}
	if calls.calls[callID].Status != entity.CallStatusActive {
		t.Fatalf("call status = %q, want active", calls.calls[callID].Status)
	}
	if got := countBreakoutEvents(t, pub.captures, event.TypeCallParticipantJoined); got != 1 {
		t.Fatalf("joined events = %d, want 1", got)
	}
}

func TestJoinCallInvitedMemberBypassesPasswordAndActivationRace(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	inviteeID := uuid.New()
	status := entity.ParticipantStatusInvited
	svc, calls := inviteParticipantsFixture(workspaceID, callID, hostID, entity.CallRoleHost, inviteeID, &status, &capturingPublisher{})
	calls.calls[callID].Status = entity.CallStatusRinging
	calls.calls[callID].Settings = entity.CallSettings{EntryMode: entity.EntryModePassword}
	calls.calls[callID].JoinPasswordHash = "not-a-valid-password-hash"
	calls.activateBeforeUpdate = func(call *entity.Call) {
		call.Status = entity.CallStatusActive
	}

	participant, err := svc.JoinCall(ctx, workspaceID, callID, inviteeID, "")
	if err != nil {
		t.Fatalf("JoinCall invited activation race returned error: %v", err)
	}
	if participant.Status != entity.ParticipantStatusConnected {
		t.Fatalf("participant status = %q, want connected", participant.Status)
	}
}

func inviteParticipantsFixture(
	workspaceID, callID, hostID uuid.UUID,
	hostRole entity.CallRole,
	targetID uuid.UUID,
	targetStatus *entity.ParticipantStatus,
	pub EventPublisher,
) (*Service, *fakeCallRepo) {
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, hostID}:   {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
		{workspaceID, targetID}: {WorkspaceID: workspaceID, UserID: targetID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: {ID: uuid.New(), CallID: callID, UserID: hostID, Role: hostRole, Status: entity.ParticipantStatusConnected},
		},
	}
	if targetStatus != nil {
		calls.participants[[2]uuid.UUID{callID, targetID}] = &entity.CallParticipant{
			ID:     uuid.New(),
			CallID: callID,
			UserID: targetID,
			Role:   entity.CallRoleParticipant,
			Status: *targetStatus,
		}
	}
	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{},
		members:  map[[2]uuid.UUID]*entity.ChannelMember{},
	}
	return NewService(calls, &fakeBreakoutRepo{}, channels, workspaces, pub, nil, mediaTestConfig(), nil, nil), calls
}

func removeParticipantFixture(workspaceID, callID, hostID, targetID uuid.UUID, actorRole entity.CallRole) (*Service, *fakeCallRepo) {
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, hostID}: {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}:   {ID: uuid.New(), CallID: callID, UserID: hostID, Role: actorRole, Status: entity.ParticipantStatusConnected},
			{callID, targetID}: {ID: uuid.New(), CallID: callID, UserID: targetID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	return svc, calls
}

func TestRemoveParticipantHostEvictsConnectedTarget(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID := uuid.New(), uuid.New()
	hostID, targetID := uuid.New(), uuid.New()
	svc, calls := removeParticipantFixture(workspaceID, callID, hostID, targetID, entity.CallRoleHost)

	if err := svc.RemoveParticipant(ctx, workspaceID, callID, hostID, targetID); err != nil {
		t.Fatalf("RemoveParticipant returned error: %v", err)
	}
	if got := calls.participants[[2]uuid.UUID{callID, targetID}].Status; got != entity.ParticipantStatusDisconnected {
		t.Fatalf("target status = %q, want %q", got, entity.ParticipantStatusDisconnected)
	}
}

func TestRemoveParticipantRejectsNonHostActor(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID := uuid.New(), uuid.New()
	actorID, targetID := uuid.New(), uuid.New()
	svc, _ := removeParticipantFixture(workspaceID, callID, actorID, targetID, entity.CallRoleParticipant)

	if err := svc.RemoveParticipant(ctx, workspaceID, callID, actorID, targetID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("RemoveParticipant by non-host = %v, want Forbidden", err)
	}
}

func TestRemoveParticipantRejectsSelf(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID := uuid.New(), uuid.New()
	hostID, targetID := uuid.New(), uuid.New()
	svc, _ := removeParticipantFixture(workspaceID, callID, hostID, targetID, entity.CallRoleHost)

	if err := svc.RemoveParticipant(ctx, workspaceID, callID, hostID, hostID); !hasCode(err, cerrors.CodeInvalidInput) {
		t.Fatalf("RemoveParticipant of self = %v, want InvalidInput", err)
	}
}

// guestReconnectFixture builds a service where `guestID` is a guest with an
// existing disconnected participant that left `leftAgo` ago, ready to rejoin.
func guestReconnectFixture(
	workspaceID, callID, channelID, guestID, participantID uuid.UUID,
	leftAgo time.Duration,
	pub EventPublisher,
) *Service {
	leftAt := time.Now().Add(-leftAgo)
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, ChannelID: &channelID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive, Settings: entity.CallSettings{WaitingRoom: true}},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, guestID}: {
				ID:         participantID,
				CallID:     callID,
				UserID:     guestID,
				Role:       entity.CallRoleParticipant,
				Status:     entity.ParticipantStatusDisconnected,
				LeftAt:     &leftAt,
				LeftReason: entity.ParticipantLeftReasonLeft,
			},
		},
	}
	channels := &fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{
		channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePrivate},
	}}
	guests := guestaccess.NewChecker(&fakeGuestAccessRepo{grants: []entity.GuestAccessGrant{{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		UserID:      guestID,
		ChannelIDs:  []uuid.UUID{channelID},
		ExpiresAt:   time.Now().Add(time.Hour),
	}}})
	return NewService(calls, &fakeBreakoutRepo{}, channels, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), guests, nil)
}

// A guest dropped moments ago (a transient disconnect or the stray /leave from
// the admit→kick race) reconnects silently instead of re-knocking (ALK-700 +
// guest admit→kick loop hardening).
func TestJoinCallGuestSilentReconnectWithinGrace(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID, channelID := uuid.New(), uuid.New(), uuid.New()
	guestID, participantID := uuid.New(), uuid.New()
	svc := guestReconnectFixture(workspaceID, callID, channelID, guestID, participantID, 5*time.Second, noopPublisher{})

	participant, err := svc.JoinCall(ctx, workspaceID, callID, guestID, "")
	if err != nil {
		t.Fatalf("JoinCall returned error: %v", err)
	}
	if participant.Status != entity.ParticipantStatusConnected {
		t.Fatalf("participant status = %q, want %q (silent reconnect within grace)", participant.Status, entity.ParticipantStatusConnected)
	}
}

// A guest who left earlier than the grace window still re-knocks for host
// approval (preserves the ALK-700 forced-waiting rule).
func TestJoinCallGuestReKnocksAfterGraceExpires(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID, channelID := uuid.New(), uuid.New(), uuid.New()
	guestID, participantID := uuid.New(), uuid.New()
	svc := guestReconnectFixture(workspaceID, callID, channelID, guestID, participantID, 5*time.Minute, noopPublisher{})

	participant, err := svc.JoinCall(ctx, workspaceID, callID, guestID, "")
	if err != nil {
		t.Fatalf("JoinCall returned error: %v", err)
	}
	if participant.Status != entity.ParticipantStatusWaiting {
		t.Fatalf("participant status = %q, want %q (re-knock after grace)", participant.Status, entity.ParticipantStatusWaiting)
	}
}

func TestJoinCall_OnEndedCall_ReturnsCallEndedNot403(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeMeeting,
				Status:      entity.CallStatusEnded,
			},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	_, err := svc.JoinCall(ctx, workspaceID, callID, userID, "")
	if err == nil {
		t.Fatal("JoinCall on ended call returned nil error, want CALL_ENDED")
	}
	appErr, ok := cerrors.AsAppError(err)
	if !ok {
		t.Fatalf("JoinCall on ended call err = %T (%v), want *cerrors.AppError", err, err)
	}
	if appErr.Code != cerrors.CodeCallEnded {
		t.Fatalf("JoinCall on ended call code = %q, want %q", appErr.Code, cerrors.CodeCallEnded)
	}
	if appErr.HTTPStatus() != 410 {
		t.Fatalf("JoinCall on ended call HTTP status = %d, want 410 Gone", appErr.HTTPStatus())
	}
}

func TestLiveKitParticipantJoinedDoesNotAdmitWaitingParticipant(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()
	participantID := uuid.New()
	pub := &capturingPublisher{}
	rooms := &fakeLiveKitRoomClient{}

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeMeeting,
				Status:      entity.CallStatusRinging,
				Settings:    entity.CallSettings{WaitingRoom: true},
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {
				ID:     participantID,
				CallID: callID,
				UserID: userID,
				Role:   entity.CallRoleParticipant,
				Status: entity.ParticipantStatusWaiting,
			},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)
	svc.SetLiveKit(LiveKitSettings{
		URL:       "http://livekit.test",
		APIKey:    "test-key",
		APISecret: "test-secret",
	})
	svc.SetLiveKitRoomClient(rooms)

	err := svc.handleLiveKitParticipantJoined(ctx, callID, &livekitpb.ParticipantInfo{
		Identity: userID.String(),
	})
	if err != nil {
		t.Fatalf("handleLiveKitParticipantJoined returned error: %v", err)
	}

	if got := calls.participants[[2]uuid.UUID{callID, userID}].Status; got != entity.ParticipantStatusWaiting {
		t.Fatalf("stored participant status = %q, want %q", got, entity.ParticipantStatusWaiting)
	}
	if pub.called {
		t.Fatalf("waiting participant livekit join published %q; want no event", pub.subject)
	}
	if len(rooms.removedParticipants) != 1 {
		t.Fatalf("removed participant calls = %d, want 1", len(rooms.removedParticipants))
	}
	if got := rooms.removedParticipants[0]; got.callID != callID || got.userID != userID {
		t.Fatalf("removed participant = (%s, %s), want (%s, %s)", got.callID, got.userID, callID, userID)
	}
}

func TestLiveKitParticipantJoinedDoesNotRepublishAlreadyActiveParticipant(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive, CreatedBy: uuid.New()},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {ID: uuid.New(), CallID: callID, UserID: userID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
		},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	if err := svc.handleLiveKitParticipantJoined(ctx, callID, &livekitpb.ParticipantInfo{Identity: userID.String()}); err != nil {
		t.Fatalf("handleLiveKitParticipantJoined returned error: %v", err)
	}
	if got := len(pub.captures); got != 0 {
		t.Fatalf("publish count = %d, want 0 for duplicate participant_joined", got)
	}
}

func TestLiveKitWebhookDedupesParticipantLeftAcrossServiceInstances(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()
	otherUserID := uuid.New()
	eventID := uuid.New().String()
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}:      {ID: uuid.New(), CallID: callID, UserID: userID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
			{callID, otherUserID}: {ID: uuid.New(), CallID: callID, UserID: otherUserID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
		},
		liveKitWebhookEvents: map[string]*entity.LiveKitWebhookEvent{},
	}
	firstPub := &capturingPublisher{}
	secondPub := &capturingPublisher{}
	first := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, firstPub, nil, mediaTestConfig(), nil, nil)
	second := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, secondPub, nil, mediaTestConfig(), nil, nil)
	ev := liveKitWebhookEvent(eventID, "participant_left", callID, userID)

	if err := first.HandleLiveKitWebhook(ctx, ev); err != nil {
		t.Fatalf("first HandleLiveKitWebhook returned error: %v", err)
	}
	if got := len(firstPub.captures); got != 1 {
		t.Fatalf("first publish count = %d, want 1", got)
	}
	if firstPub.captures[0].subject == "" {
		t.Fatalf("first publish subject was empty")
	}
	if err := second.HandleLiveKitWebhook(ctx, ev); err != nil {
		t.Fatalf("second HandleLiveKitWebhook returned error: %v", err)
	}
	if got := len(secondPub.captures); got != 0 {
		t.Fatalf("duplicate publish count = %d, want 0", got)
	}
	if got := calls.liveKitWebhookClaimAttempts[eventID]; got != 2 {
		t.Fatalf("claim attempts = %d, want 2", got)
	}
}

func TestLiveKitWebhookDedupesRoomFinishedAcrossServiceInstances(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New().String()
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive, CreatedBy: userID},
		},
		liveKitWebhookEvents: map[string]*entity.LiveKitWebhookEvent{},
	}
	firstPub := &capturingPublisher{}
	secondPub := &capturingPublisher{}
	first := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, firstPub, nil, mediaTestConfig(), nil, nil)
	second := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, secondPub, nil, mediaTestConfig(), nil, nil)
	ev := liveKitWebhookEvent(eventID, "room_finished", callID, uuid.Nil)

	if err := first.HandleLiveKitWebhook(ctx, ev); err != nil {
		t.Fatalf("first HandleLiveKitWebhook returned error: %v", err)
	}
	if got := len(firstPub.captures); got != 1 {
		t.Fatalf("first publish count = %d, want 1", got)
	}
	if calls.calls[callID].EndReason != entity.CallEndReasonAllLeft {
		t.Fatalf("end reason = %q, want %q", calls.calls[callID].EndReason, entity.CallEndReasonAllLeft)
	}
	if err := second.HandleLiveKitWebhook(ctx, ev); err != nil {
		t.Fatalf("second HandleLiveKitWebhook returned error: %v", err)
	}
	if got := len(secondPub.captures); got != 0 {
		t.Fatalf("duplicate publish count = %d, want 0", got)
	}
}

func TestLiveKitWebhookInProgressDuplicateReturnsRetryableConflict(t *testing.T) {
	ctx := context.Background()
	callID := uuid.New()
	eventID := uuid.New().String()
	leaseExpiresAt := time.Now().Add(time.Minute).UTC()
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: uuid.New(), Type: entity.CallTypeMeeting, Status: entity.CallStatusActive},
		},
		liveKitWebhookEvents: map[string]*entity.LiveKitWebhookEvent{
			eventID: {
				EventID:        eventID,
				CallID:         callID,
				EventType:      "room_finished",
				Status:         "processing",
				LeaseExpiresAt: &leaseExpiresAt,
			},
		},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	err := svc.HandleLiveKitWebhook(ctx, liveKitWebhookEvent(eventID, "room_finished", callID, uuid.Nil))
	if !hasCode(err, cerrors.CodeConflict) {
		t.Fatalf("HandleLiveKitWebhook in-progress duplicate error = %v, want CONFLICT", err)
	}
	if got := len(pub.captures); got != 0 {
		t.Fatalf("publish count = %d, want 0", got)
	}
}

func TestLiveKitWebhookSupersededClaimDoesNotMarkProcessed(t *testing.T) {
	ctx := context.Background()
	callID := uuid.New()
	eventID := uuid.New().String()
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: uuid.New(), Type: entity.CallTypeMeeting, Status: entity.CallStatusActive, CreatedBy: uuid.New()},
		},
		liveKitWebhookEvents: map[string]*entity.LiveKitWebhookEvent{},
		markLiveKitWebhookBeforeProcessed: func(event *entity.LiveKitWebhookEvent) {
			event.ClaimToken = "newer-claim"
		},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	err := svc.HandleLiveKitWebhook(ctx, liveKitWebhookEvent(eventID, "room_finished", callID, uuid.Nil))
	if !hasCode(err, cerrors.CodeConflict) {
		t.Fatalf("HandleLiveKitWebhook superseded claim error = %v, want CONFLICT", err)
	}
	if calls.liveKitWebhookEvents[eventID].Status != "processing" {
		t.Fatalf("event status = %q, want processing", calls.liveKitWebhookEvents[eventID].Status)
	}
}

func TestLiveKitWebhookIgnoresParticipantLeftAfterRoomFinished(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive, CreatedBy: userID},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {ID: uuid.New(), CallID: callID, UserID: userID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
		},
		liveKitWebhookEvents: map[string]*entity.LiveKitWebhookEvent{},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	if err := svc.HandleLiveKitWebhook(ctx, liveKitWebhookEvent(uuid.New().String(), "room_finished", callID, uuid.Nil)); err != nil {
		t.Fatalf("room_finished HandleLiveKitWebhook returned error: %v", err)
	}
	if err := svc.HandleLiveKitWebhook(ctx, liveKitWebhookEvent(uuid.New().String(), "participant_left", callID, userID)); err != nil {
		t.Fatalf("participant_left HandleLiveKitWebhook returned error: %v", err)
	}
	if got := len(pub.captures); got != 1 {
		t.Fatalf("publish count after out-of-order delivery = %d, want 1", got)
	}
	// room_finished ends the call AND disconnects its remaining participants (the
	// zombie-call fix), so userID is already disconnected. The point of this test
	// is that the LATE participant_left is ignored — proven by the single publish
	// above — and that it does not mutate the already-disconnected row further.
	left := calls.participants[[2]uuid.UUID{callID, userID}]
	if left.Status != entity.ParticipantStatusDisconnected {
		t.Fatalf("participant status = %q after room_finished, want disconnected", left.Status)
	}
	if left.LeftReason != entity.ParticipantLeftReasonTimeout {
		t.Fatalf("participant left_reason = %q, want timeout (set by room_finished, not the late participant_left)", left.LeftReason)
	}
}

// Regression (2026-06-03 prod): breakout rooms are separate LiveKit rooms, so
// moving a participant into one disconnects them from the MAIN room and LiveKit
// fires participant_left for main. While the participant is assigned to a
// breakout room this is a transition, NOT a call leave — it must not mark them
// disconnected, emit participant.left, or count toward auto-end. Without this the
// host saw 0 participants in each breakout room and the movers "fell out".
func TestLiveKitWebhookParticipantLeftIgnoredWhileInBreakoutRoom(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	userID := uuid.New()
	breakoutRoomID := uuid.New()

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			// Two participants so the (irrelevant) auto-end path can't fire even if reached.
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive, CreatedBy: hostID},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: {ID: uuid.New(), CallID: callID, UserID: hostID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
			{callID, userID}: {ID: uuid.New(), CallID: callID, UserID: userID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected, BreakoutRoomID: &breakoutRoomID},
		},
		liveKitWebhookEvents: map[string]*entity.LiveKitWebhookEvent{},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	if err := svc.HandleLiveKitWebhook(ctx, liveKitWebhookEvent(uuid.New().String(), "participant_left", callID, userID)); err != nil {
		t.Fatalf("participant_left HandleLiveKitWebhook returned error: %v", err)
	}

	if got := calls.participants[[2]uuid.UUID{callID, userID}].Status; got != entity.ParticipantStatusConnected {
		t.Fatalf("participant status = %v, want connected (a breakout transition must not disconnect)", got)
	}
	if got := len(pub.captures); got != 0 {
		t.Fatalf("publish count = %d, want 0 (no participant.left for a breakout transition)", got)
	}
	if calls.calls[callID].Status != entity.CallStatusActive {
		t.Fatalf("call status = %v, want still active", calls.calls[callID].Status)
	}
}

// Review follow-up: a participant who HARD-leaves (tab close/crash) while still
// assigned to a breakout room must be marked disconnected and emit
// participant.left. Their breakout-room participant_left is the real call leave —
// a clean return-to-main clears the assignment first and short-circuits the
// handler, so reaching the unassign means a genuine drop.
func TestLiveKitBreakoutParticipantLeftMarksHardLeaveDisconnected(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	userID := uuid.New()
	breakoutRoomID := uuid.New()

	leaver := connectedParticipant(callID, userID, entity.CallRoleParticipant)
	leaver.BreakoutRoomID = &breakoutRoomID
	host := connectedParticipant(callID, hostID, entity.CallRoleHost)

	call := breakoutCall(callID, workspaceID, hostID)
	call.PinnedParticipantUserID = &userID
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: call},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: leaver,
			{callID, hostID}: host,
		},
		liveKitWebhookEvents: map[string]*entity.LiveKitWebhookEvent{},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, newStubBreakoutRepo(), &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	ev := &livekitpb.WebhookEvent{
		Id:          uuid.New().String(),
		Event:       "participant_left",
		Room:        &livekitpb.Room{Name: breakoutLiveKitRoomName(callID, breakoutRoomID)},
		Participant: &livekitpb.ParticipantInfo{Identity: userID.String()},
	}
	if err := svc.HandleLiveKitWebhook(ctx, ev); err != nil {
		t.Fatalf("HandleLiveKitWebhook returned error: %v", err)
	}

	if calls.calls[callID].PinnedParticipantUserID != nil {
		t.Fatalf("pinned participant = %v, want nil", calls.calls[callID].PinnedParticipantUserID)
	}
	if leaver.Status != entity.ParticipantStatusDisconnected {
		t.Fatalf("leaver status = %v, want disconnected", leaver.Status)
	}
	if got := countBreakoutEvents(t, pub.captures, event.TypeCallParticipantLeft); got != 1 {
		t.Fatalf("call.participant.left events = %d, want 1", got)
	}
	if got := countBreakoutEvents(t, pub.captures, event.TypeCallPinnedChanged); got != 1 {
		t.Fatalf("call.pinned.changed events = %d, want 1", got)
	}
	// Host is still connected, so the call must NOT auto-end.
	if calls.calls[callID].Status == entity.CallStatusEnded {
		t.Fatalf("call ended despite the host still being connected")
	}
}

func TestLiveKitTrackChangedDoesNotPublishWhenScreenShareStateAlreadyMatches(t *testing.T) {
	ctx := context.Background()
	callID := uuid.New()
	userID := uuid.New()
	calls := &fakeCallRepo{
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {ID: uuid.New(), CallID: callID, UserID: userID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected, ScreenSharing: true},
		},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	err := svc.handleLiveKitTrackChanged(ctx, callID, &livekitpb.ParticipantInfo{Identity: userID.String()}, &livekitpb.TrackInfo{Source: livekitpb.TrackSource_SCREEN_SHARE}, true)
	if err != nil {
		t.Fatalf("handleLiveKitTrackChanged returned error: %v", err)
	}
	if got := len(pub.captures); got != 0 {
		t.Fatalf("publish count = %d, want 0 for duplicate track event", got)
	}
}

func TestLiveKitParticipantLeftRetryAfterDisconnectStillAutoEnds(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()
	now := time.Now().UTC()
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive, CreatedBy: userID},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {
				ID:         uuid.New(),
				CallID:     callID,
				UserID:     userID,
				Role:       entity.CallRoleParticipant,
				Status:     entity.ParticipantStatusDisconnected,
				LeftAt:     &now,
				LeftReason: entity.ParticipantLeftReasonLeft,
			},
		},
		liveKitWebhookEvents: map[string]*entity.LiveKitWebhookEvent{},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	if err := svc.HandleLiveKitWebhook(ctx, liveKitWebhookEvent(uuid.New().String(), "participant_left", callID, userID)); err != nil {
		t.Fatalf("HandleLiveKitWebhook returned error: %v", err)
	}
	if calls.calls[callID].Status != entity.CallStatusEnded {
		t.Fatalf("call status = %q, want ended", calls.calls[callID].Status)
	}
	if calls.calls[callID].EndReason != entity.CallEndReasonAllLeft {
		t.Fatalf("end reason = %q, want all_left", calls.calls[callID].EndReason)
	}
	if got := len(pub.captures); got != 1 {
		t.Fatalf("publish count = %d, want 1 call-ended event", got)
	}
}

func TestStartCallRequiresLiveKitRoomBeforePersist(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{calls: map[uuid.UUID]*entity.Call{}, participants: map[[2]uuid.UUID]*entity.CallParticipant{}}
	roomClient := &fakeLiveKitRoomClient{ensureErr: cerrors.Unavailable("livekit unavailable")}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	svc.SetLiveKit(LiveKitSettings{URL: "https://livekit.example.com", APIKey: "key", APISecret: "secret", TokenTTL: time.Minute})
	svc.SetLiveKitRoomClient(roomClient)

	callEntity, err := svc.StartCall(ctx, workspaceID, userID, entity.CallTypeOneToOne, "dm", nil, entity.CallSettings{}, "")
	if !hasCode(err, cerrors.CodeUnavailable) {
		t.Fatalf("StartCall error = %v, want UNAVAILABLE", err)
	}
	if callEntity != nil {
		t.Fatalf("StartCall returned call despite LiveKit failure: %+v", callEntity)
	}
	if roomClient.ensureCalls != 1 {
		t.Fatalf("EnsureRoom calls = %d, want 1", roomClient.ensureCalls)
	}
	if len(calls.calls) != 0 {
		t.Fatalf("calls persisted after LiveKit failure = %d, want 0", len(calls.calls))
	}
}

func TestStartCallDefaultsChatEnabled(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{calls: map[uuid.UUID]*entity.Call{}, participants: map[[2]uuid.UUID]*entity.CallParticipant{}}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	configureCleanupLiveKit(svc, nil)

	// Empty settings → Settings.Chat is the Go zero value (false). StartCall must
	// default it to true so the call-chat gate (message.go "call chat is disabled")
	// never 403s for ad-hoc / channel calls. (hotfix)
	callEntity, err := svc.StartCall(ctx, workspaceID, userID, entity.CallTypeOneToOne, "dm", nil, entity.CallSettings{}, "")
	if err != nil {
		t.Fatalf("StartCall returned error: %v", err)
	}
	if callEntity == nil {
		t.Fatalf("StartCall returned nil call")
	}
	if !callEntity.Settings.Chat {
		t.Fatalf("StartCall Settings.Chat = false, want true (chat on by default)")
	}
	persisted, ok := calls.calls[callEntity.ID]
	if !ok {
		t.Fatalf("call was not persisted")
	}
	if !persisted.Settings.Chat {
		t.Fatalf("persisted call Settings.Chat = false, want true")
	}
}

func TestStartCallDefaultsBreakoutRoomsForGroupAndMeeting(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		typ  entity.CallType
		want bool
	}{
		{name: "group", typ: entity.CallTypeGroup, want: true},
		{name: "meeting", typ: entity.CallTypeMeeting, want: true},
		{name: "one_to_one", typ: entity.CallTypeOneToOne, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspaceID := uuid.New()
			userID := uuid.New()
			workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
				{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
			}}
			calls := &fakeCallRepo{calls: map[uuid.UUID]*entity.Call{}, participants: map[[2]uuid.UUID]*entity.CallParticipant{}}
			svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
			configureCleanupLiveKit(svc, nil)

			callEntity, err := svc.StartCall(ctx, workspaceID, userID, tc.typ, "", nil, entity.CallSettings{}, "")
			if err != nil {
				t.Fatalf("StartCall returned error: %v", err)
			}
			if callEntity == nil {
				t.Fatalf("StartCall returned nil call")
			}
			if callEntity.Settings.BreakoutRooms != tc.want {
				t.Fatalf("StartCall Settings.BreakoutRooms = %v, want %v", callEntity.Settings.BreakoutRooms, tc.want)
			}
			persisted, ok := calls.calls[callEntity.ID]
			if !ok {
				t.Fatalf("call was not persisted")
			}
			if persisted.Settings.BreakoutRooms != tc.want {
				t.Fatalf("persisted Settings.BreakoutRooms = %v, want %v", persisted.Settings.BreakoutRooms, tc.want)
			}
		})
	}
}

func TestJoinCallRequiresLiveKitRoomBeforeParticipantInsert(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	userID := uuid.New()
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeGroup, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: {ID: uuid.New(), CallID: callID, UserID: hostID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
		},
	}
	roomClient := &fakeLiveKitRoomClient{ensureErr: cerrors.Unavailable("livekit unavailable")}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	svc.SetLiveKit(LiveKitSettings{URL: "https://livekit.example.com", APIKey: "key", APISecret: "secret", TokenTTL: time.Minute})
	svc.SetLiveKitRoomClient(roomClient)

	participant, err := svc.JoinCall(ctx, workspaceID, callID, userID, "")
	if !hasCode(err, cerrors.CodeUnavailable) {
		t.Fatalf("JoinCall error = %v, want UNAVAILABLE", err)
	}
	if participant != nil {
		t.Fatalf("JoinCall returned participant despite LiveKit failure: %+v", participant)
	}
	if roomClient.ensureCalls != 1 {
		t.Fatalf("EnsureRoom calls = %d, want 1", roomClient.ensureCalls)
	}
	if _, ok := calls.participants[[2]uuid.UUID{callID, userID}]; ok {
		t.Fatalf("participant was inserted after LiveKit failure")
	}
}

func configureCleanupLiveKit(svc *Service, roomClient *fakeLiveKitRoomClient) *fakeLiveKitRoomClient {
	if roomClient == nil {
		roomClient = &fakeLiveKitRoomClient{}
	}
	svc.SetLiveKit(LiveKitSettings{URL: "wss://livekit.test", APIKey: "key", APISecret: "secret", TokenTTL: time.Hour})
	svc.SetLiveKitRoomClient(roomClient)
	return roomClient
}

func TestCleanupStaleOpenCallsEndsRingingAsMissed(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	inviteeID := uuid.New()
	startedAt := time.Now().Add(-10 * time.Minute)
	pub := &capturingPublisher{}

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeOneToOne,
				Status:      entity.CallStatusRinging,
				CreatedBy:   hostID,
				StartedAt:   &startedAt,
				CreatedAt:   startedAt,
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: {
				ID:       uuid.New(),
				CallID:   callID,
				UserID:   hostID,
				Role:     entity.CallRoleHost,
				Status:   entity.ParticipantStatusConnected,
				JoinedAt: &startedAt,
			},
			{callID, inviteeID}: {
				ID:     uuid.New(),
				CallID: callID,
				UserID: inviteeID,
				Role:   entity.CallRoleParticipant,
				Status: entity.ParticipantStatusInvited,
			},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)
	configureCleanupLiveKit(svc, nil)

	ended, err := svc.CleanupStaleOpenCalls(ctx, 5*time.Minute, 100)
	if err != nil {
		t.Fatalf("CleanupStaleOpenCalls returned error: %v", err)
	}
	if ended != 1 {
		t.Fatalf("ended = %d, want 1", ended)
	}
	callEntity := calls.calls[callID]
	if callEntity.Status != entity.CallStatusEnded || callEntity.EndReason != entity.CallEndReasonMissed {
		t.Fatalf("call lifecycle = status %q reason %q, want ended/missed", callEntity.Status, callEntity.EndReason)
	}
	for _, participant := range calls.participants {
		if participant.Status != entity.ParticipantStatusDisconnected || participant.LeftReason != entity.ParticipantLeftReasonMissed {
			t.Fatalf("participant lifecycle = status %q reason %q, want disconnected/missed", participant.Status, participant.LeftReason)
		}
	}
	if len(pub.captures) != 1 {
		t.Fatalf("published events = %d, want one call.ended event", len(pub.captures))
	}

	ended, err = svc.CleanupStaleOpenCalls(ctx, 5*time.Minute, 100)
	if err != nil {
		t.Fatalf("second CleanupStaleOpenCalls returned error: %v", err)
	}
	if ended != 0 {
		t.Fatalf("second ended = %d, want 0", ended)
	}
	if len(pub.captures) != 1 {
		t.Fatalf("published events after retry = %d, want still one call.ended event", len(pub.captures))
	}
}

func TestCleanupStaleOpenCallsEndsRingingWithOnlyCallerInLiveKit(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	inviteeID := uuid.New()
	startedAt := time.Now().Add(-10 * time.Minute)

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeOneToOne,
				Status:      entity.CallStatusRinging,
				CreatedBy:   hostID,
				StartedAt:   &startedAt,
				CreatedAt:   startedAt,
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: {
				ID:       uuid.New(),
				CallID:   callID,
				UserID:   hostID,
				Role:     entity.CallRoleHost,
				Status:   entity.ParticipantStatusConnected,
				JoinedAt: &startedAt,
			},
			{callID, inviteeID}: {
				ID:     uuid.New(),
				CallID: callID,
				UserID: inviteeID,
				Role:   entity.CallRoleParticipant,
				Status: entity.ParticipantStatusInvited,
			},
		},
	}
	roomClient := &fakeLiveKitRoomClient{
		participantsByCall: map[uuid.UUID][]*livekitpb.ParticipantInfo{
			callID: {
				{Identity: hostID.String()},
			},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	configureCleanupLiveKit(svc, roomClient)

	ended, err := svc.CleanupStaleOpenCalls(ctx, 5*time.Minute, 100)
	if err != nil {
		t.Fatalf("CleanupStaleOpenCalls returned error: %v", err)
	}
	if ended != 1 {
		t.Fatalf("ended = %d, want 1", ended)
	}
	callEntity := calls.calls[callID]
	if callEntity.Status != entity.CallStatusEnded || callEntity.EndReason != entity.CallEndReasonMissed {
		t.Fatalf("call lifecycle = status %q reason %q, want ended/missed", callEntity.Status, callEntity.EndReason)
	}
}

func TestCleanupStaleOpenCallsKeepsRingingWithAnsweredParticipantInLiveKit(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	inviteeID := uuid.New()
	startedAt := time.Now().Add(-10 * time.Minute)

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeOneToOne,
				Status:      entity.CallStatusRinging,
				CreatedBy:   hostID,
				StartedAt:   &startedAt,
				CreatedAt:   startedAt,
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: {
				ID:       uuid.New(),
				CallID:   callID,
				UserID:   hostID,
				Role:     entity.CallRoleHost,
				Status:   entity.ParticipantStatusConnected,
				JoinedAt: &startedAt,
			},
			{callID, inviteeID}: {
				ID:       uuid.New(),
				CallID:   callID,
				UserID:   inviteeID,
				Role:     entity.CallRoleParticipant,
				Status:   entity.ParticipantStatusConnected,
				JoinedAt: &startedAt,
			},
		},
	}
	roomClient := &fakeLiveKitRoomClient{
		participantsByCall: map[uuid.UUID][]*livekitpb.ParticipantInfo{
			callID: {
				{Identity: hostID.String()},
				{Identity: inviteeID.String()},
			},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	configureCleanupLiveKit(svc, roomClient)

	ended, err := svc.CleanupStaleOpenCalls(ctx, 5*time.Minute, 100)
	if err != nil {
		t.Fatalf("CleanupStaleOpenCalls returned error: %v", err)
	}
	if ended != 0 {
		t.Fatalf("ended = %d, want 0", ended)
	}
	if calls.calls[callID].Status != entity.CallStatusRinging {
		t.Fatalf("call status = %q, want ringing", calls.calls[callID].Status)
	}
}

func TestCleanupStaleOpenCallsSkipsWhenLiveKitDisabled(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()
	startedAt := time.Now().Add(-10 * time.Minute)

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeMeeting,
				Status:      entity.CallStatusActive,
				CreatedBy:   userID,
				StartedAt:   &startedAt,
				CreatedAt:   startedAt,
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {
				ID:       uuid.New(),
				CallID:   callID,
				UserID:   userID,
				Role:     entity.CallRoleHost,
				Status:   entity.ParticipantStatusConnected,
				JoinedAt: &startedAt,
			},
		},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	ended, err := svc.CleanupStaleOpenCalls(ctx, 5*time.Minute, 100)
	if err != nil {
		t.Fatalf("CleanupStaleOpenCalls returned error: %v", err)
	}
	if ended != 0 {
		t.Fatalf("ended = %d, want 0", ended)
	}
	if calls.calls[callID].Status != entity.CallStatusActive {
		t.Fatalf("call status = %q, want active", calls.calls[callID].Status)
	}
	if len(pub.captures) != 0 {
		t.Fatalf("published events = %d, want none", len(pub.captures))
	}
}

func TestCleanupStaleOpenCallsEndsActiveAsAllLeftAndTimeoutsParticipants(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	leftUserID := uuid.New()
	startedAt := time.Now().Add(-10 * time.Minute)
	leftAt := startedAt.Add(2 * time.Minute)

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeMeeting,
				Status:      entity.CallStatusActive,
				CreatedBy:   hostID,
				StartedAt:   &startedAt,
				CreatedAt:   startedAt,
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: {
				ID:       uuid.New(),
				CallID:   callID,
				UserID:   hostID,
				Role:     entity.CallRoleHost,
				Status:   entity.ParticipantStatusConnected,
				JoinedAt: &startedAt,
			},
			{callID, leftUserID}: {
				ID:         uuid.New(),
				CallID:     callID,
				UserID:     leftUserID,
				Role:       entity.CallRoleParticipant,
				Status:     entity.ParticipantStatusDisconnected,
				JoinedAt:   &startedAt,
				LeftAt:     &leftAt,
				LeftReason: entity.ParticipantLeftReasonLeft,
			},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	configureCleanupLiveKit(svc, nil)

	ended, err := svc.CleanupStaleOpenCalls(ctx, 5*time.Minute, 100)
	if err != nil {
		t.Fatalf("CleanupStaleOpenCalls returned error: %v", err)
	}
	if ended != 1 {
		t.Fatalf("ended = %d, want 1", ended)
	}
	callEntity := calls.calls[callID]
	if callEntity.Status != entity.CallStatusEnded || callEntity.EndReason != entity.CallEndReasonAllLeft {
		t.Fatalf("call lifecycle = status %q reason %q, want ended/all_left", callEntity.Status, callEntity.EndReason)
	}
	hostParticipant := calls.participants[[2]uuid.UUID{callID, hostID}]
	if hostParticipant.Status != entity.ParticipantStatusDisconnected || hostParticipant.LeftReason != entity.ParticipantLeftReasonTimeout {
		t.Fatalf("host lifecycle = status %q reason %q, want disconnected/timeout", hostParticipant.Status, hostParticipant.LeftReason)
	}
	leftParticipant := calls.participants[[2]uuid.UUID{callID, leftUserID}]
	if leftParticipant.LeftReason != entity.ParticipantLeftReasonLeft || leftParticipant.LeftAt == nil || !leftParticipant.LeftAt.Equal(leftAt) {
		t.Fatalf("left participant was overwritten: %+v", leftParticipant)
	}
}

func TestCleanupStaleOpenCallsKeepsRecentlyActivatedOldRingingCall(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()
	startedAt := time.Now().Add(-10 * time.Minute)

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeOneToOne,
				Status:      entity.CallStatusRinging,
				CreatedBy:   userID,
				StartedAt:   &startedAt,
				CreatedAt:   startedAt,
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {
				ID:       uuid.New(),
				CallID:   callID,
				UserID:   userID,
				Role:     entity.CallRoleHost,
				Status:   entity.ParticipantStatusConnected,
				JoinedAt: &startedAt,
			},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	configureCleanupLiveKit(svc, nil)

	didActivate, err := activateRingingCall(ctx, calls, callID)
	if err != nil {
		t.Fatalf("activateRingingCall returned error: %v", err)
	}
	if !didActivate {
		t.Fatalf("activateRingingCall didActivate = false, want true")
	}
	if calls.calls[callID].StartedAt == nil || !calls.calls[callID].StartedAt.After(startedAt) {
		t.Fatalf("activated call StartedAt = %v, want after %v", calls.calls[callID].StartedAt, startedAt)
	}

	ended, err := svc.CleanupStaleOpenCalls(ctx, 5*time.Minute, 100)
	if err != nil {
		t.Fatalf("CleanupStaleOpenCalls returned error: %v", err)
	}
	if ended != 0 {
		t.Fatalf("ended = %d, want 0", ended)
	}
	if calls.calls[callID].Status != entity.CallStatusActive {
		t.Fatalf("call status = %q, want active", calls.calls[callID].Status)
	}
}

func TestCleanupStaleOpenCallsKeepsCallWithLiveKitParticipants(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()
	startedAt := time.Now().Add(-10 * time.Minute)

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeMeeting,
				Status:      entity.CallStatusActive,
				CreatedBy:   userID,
				StartedAt:   &startedAt,
				CreatedAt:   startedAt,
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {
				ID:       uuid.New(),
				CallID:   callID,
				UserID:   userID,
				Role:     entity.CallRoleHost,
				Status:   entity.ParticipantStatusConnected,
				JoinedAt: &startedAt,
			},
		},
	}
	roomClient := &fakeLiveKitRoomClient{
		participantsByCall: map[uuid.UUID][]*livekitpb.ParticipantInfo{
			callID: {
				{Identity: userID.String()},
			},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	configureCleanupLiveKit(svc, roomClient)

	ended, err := svc.CleanupStaleOpenCalls(ctx, 5*time.Minute, 100)
	if err != nil {
		t.Fatalf("CleanupStaleOpenCalls returned error: %v", err)
	}
	if ended != 0 {
		t.Fatalf("ended = %d, want 0", ended)
	}
	if calls.calls[callID].Status != entity.CallStatusActive {
		t.Fatalf("call status = %q, want active", calls.calls[callID].Status)
	}
	if roomClient.listParticipantsCalls != 1 {
		t.Fatalf("ListParticipants calls = %d, want 1", roomClient.listParticipantsCalls)
	}
}

func TestCleanupStaleOpenCallsKeepsCallOnLiveKitInspectionError(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()
	startedAt := time.Now().Add(-10 * time.Minute)

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeMeeting,
				Status:      entity.CallStatusActive,
				CreatedBy:   userID,
				StartedAt:   &startedAt,
				CreatedAt:   startedAt,
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{},
	}
	roomClient := &fakeLiveKitRoomClient{listParticipantsErr: errors.New("livekit temporarily unavailable")}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	configureCleanupLiveKit(svc, roomClient)

	ended, err := svc.CleanupStaleOpenCalls(ctx, 5*time.Minute, 100)
	if err != nil {
		t.Fatalf("CleanupStaleOpenCalls returned error: %v", err)
	}
	if ended != 0 {
		t.Fatalf("ended = %d, want 0", ended)
	}
	if calls.calls[callID].Status != entity.CallStatusActive {
		t.Fatalf("call status = %q, want active", calls.calls[callID].Status)
	}
}

func TestLeaveCallIsIdempotentAndAutoEndsOneToOne(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	userID := uuid.New()
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeOneToOne, Status: entity.CallStatusActive, CreatedBy: hostID},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}: {ID: uuid.New(), CallID: callID, UserID: hostID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
			{callID, userID}: {ID: uuid.New(), CallID: callID, UserID: userID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	result, err := svc.LeaveCall(ctx, workspaceID, callID, userID)
	if err != nil {
		t.Fatalf("LeaveCall returned error: %v", err)
	}
	if result == nil || result.AlreadyLeft {
		t.Fatalf("LeaveCall result = %+v, want not already left", result)
	}
	participant := calls.participants[[2]uuid.UUID{callID, userID}]
	if participant.Status != entity.ParticipantStatusDisconnected || participant.LeftReason != entity.ParticipantLeftReasonLeft {
		t.Fatalf("participant lifecycle = status %q reason %q, want disconnected/left", participant.Status, participant.LeftReason)
	}
	// The non-leaving party (the host here) must NOT linger as status='connected'
	// once the auto-end fires — otherwise it becomes the zombie call that the
	// active-call surfaces resurface for the other user. (zombie-calls)
	host := calls.participants[[2]uuid.UUID{callID, hostID}]
	if host.Status != entity.ParticipantStatusDisconnected || host.LeftAt == nil {
		t.Fatalf("host lifecycle = status %q left_at %v, want disconnected with left_at set", host.Status, host.LeftAt)
	}
	callEntity := calls.calls[callID]
	if callEntity.Status != entity.CallStatusEnded || callEntity.EndReason != entity.CallEndReasonAllLeft {
		t.Fatalf("call lifecycle = status %q reason %q, want ended/all_left", callEntity.Status, callEntity.EndReason)
	}

	second, err := svc.LeaveCall(ctx, workspaceID, callID, userID)
	if err != nil {
		t.Fatalf("second LeaveCall returned error: %v", err)
	}
	if second == nil || !second.AlreadyLeft {
		t.Fatalf("second LeaveCall result = %+v, want already left", second)
	}
}

func TestLeaveCallAfterEndedIsAlreadyLeftNoMutation(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	participant := &entity.CallParticipant{
		ID:     uuid.New(),
		CallID: callID,
		UserID: userID,
		Role:   entity.CallRoleHost,
		Status: entity.ParticipantStatusConnected,
	}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeGroup, Status: entity.CallStatusEnded, CreatedBy: userID, EndReason: entity.CallEndReasonHostEnded},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: participant,
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	result, err := svc.LeaveCall(ctx, workspaceID, callID, userID)
	if err != nil {
		t.Fatalf("LeaveCall returned error: %v", err)
	}
	if result == nil || !result.AlreadyLeft {
		t.Fatalf("LeaveCall result = %+v, want already left", result)
	}
	if participant.Status != entity.ParticipantStatusConnected || participant.LeftReason != "" || participant.LeftAt != nil {
		t.Fatalf("participant mutated after ended leave: %+v", participant)
	}
}

// Ending a call must disconnect every still-connected participant, not just the
// caller. Otherwise the non-ending participants linger as status='connected'
// with left_at=NULL forever — the "zombie call" the active-call surfaces keep
// resurfacing for them. (zombie-calls)
func TestEndCallDisconnectsRemainingParticipants(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	otherID := uuid.New()
	startedAt := time.Now().Add(-5 * time.Minute)
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, hostID}:  {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
		{workspaceID, otherID}: {WorkspaceID: workspaceID, UserID: otherID, Role: entity.WorkspaceRoleMember},
	}}
	host := &entity.CallParticipant{
		ID: uuid.New(), CallID: callID, UserID: hostID,
		Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected, JoinedAt: &startedAt,
	}
	other := &entity.CallParticipant{
		ID: uuid.New(), CallID: callID, UserID: otherID,
		Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected, JoinedAt: &startedAt,
	}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeGroup, Status: entity.CallStatusActive, CreatedBy: hostID, StartedAt: &startedAt},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}:  host,
			{callID, otherID}: other,
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	if err := svc.EndCall(ctx, workspaceID, callID, hostID); err != nil {
		t.Fatalf("EndCall returned error: %v", err)
	}

	for name, p := range map[string]*entity.CallParticipant{"host": host, "other": other} {
		if p.Status != entity.ParticipantStatusDisconnected {
			t.Fatalf("%s status = %q, want disconnected", name, p.Status)
		}
		if p.LeftAt == nil {
			t.Fatalf("%s left_at is nil, want set", name)
		}
	}
}

// Cancelling a ringing call must also disconnect its live participants (the
// caller), with reason 'missed' — same zombie-call invariant on the cancel path.
// (zombie-calls)
func TestCancelCallDisconnectsLiveParticipants(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	callerID := uuid.New()
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, callerID}: {WorkspaceID: workspaceID, UserID: callerID, Role: entity.WorkspaceRoleMember},
	}}
	caller := &entity.CallParticipant{
		ID: uuid.New(), CallID: callID, UserID: callerID,
		Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected,
	}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeOneToOne, Status: entity.CallStatusRinging, CreatedBy: callerID},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, callerID}: caller,
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	if _, err := svc.CancelCall(ctx, workspaceID, callID, callerID); err != nil {
		t.Fatalf("CancelCall returned error: %v", err)
	}
	if caller.Status != entity.ParticipantStatusDisconnected || caller.LeftReason != entity.ParticipantLeftReasonMissed {
		t.Fatalf("caller lifecycle = status %q reason %q, want disconnected/missed", caller.Status, caller.LeftReason)
	}
	if caller.LeftAt == nil {
		t.Fatalf("caller left_at is nil, want set")
	}
}

func TestLeaveCallConcurrentDisconnectIsAlreadyLeftNoPublish(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()
	participant := &entity.CallParticipant{
		ID:     uuid.New(),
		CallID: callID,
		UserID: userID,
		Role:   entity.CallRoleHost,
		Status: entity.ParticipantStatusConnected,
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	pub := &capturingPublisher{}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeGroup, Status: entity.CallStatusActive, CreatedBy: userID},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: participant,
		},
		disconnectBeforeUpdate: func(p *entity.CallParticipant) {
			now := time.Now().UTC()
			p.Status = entity.ParticipantStatusDisconnected
			p.LeftAt = &now
			p.LeftReason = entity.ParticipantLeftReasonLeft
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, pub, nil, mediaTestConfig(), nil, nil)

	result, err := svc.LeaveCall(ctx, workspaceID, callID, userID)
	if err != nil {
		t.Fatalf("LeaveCall returned error: %v", err)
	}
	if result == nil || !result.AlreadyLeft {
		t.Fatalf("LeaveCall result = %+v, want already left", result)
	}
	if pub.called {
		t.Fatalf("concurrent leave published %q; want no event", pub.subject)
	}
}

func TestLiveKitParticipantLeftConcurrentDisconnectStillAutoEnds(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()
	participant := &entity.CallParticipant{
		ID:     uuid.New(),
		CallID: callID,
		UserID: userID,
		Role:   entity.CallRoleHost,
		Status: entity.ParticipantStatusConnected,
	}
	pub := &capturingPublisher{}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeGroup, Status: entity.CallStatusActive, CreatedBy: userID},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: participant,
		},
		disconnectBeforeUpdate: func(p *entity.CallParticipant) {
			now := time.Now().UTC()
			p.Status = entity.ParticipantStatusDisconnected
			p.LeftAt = &now
			p.LeftReason = entity.ParticipantLeftReasonLeft
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)

	err := svc.handleLiveKitParticipantLeft(ctx, callID, &livekitpb.ParticipantInfo{
		Identity: userID.String(),
	})
	if err != nil {
		t.Fatalf("handleLiveKitParticipantLeft returned error: %v", err)
	}
	if calls.calls[callID].Status != entity.CallStatusEnded {
		t.Fatalf("call status = %q, want ended", calls.calls[callID].Status)
	}
	if calls.calls[callID].EndReason != entity.CallEndReasonAllLeft {
		t.Fatalf("end reason = %q, want all_left", calls.calls[callID].EndReason)
	}
	if got := len(pub.captures); got != 1 {
		t.Fatalf("publish count = %d, want 1 call-ended event", got)
	}
}

func TestCancelCallEndsRingingByCreator(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeOneToOne, Status: entity.CallStatusRinging, CreatedBy: userID},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {ID: uuid.New(), CallID: callID, UserID: userID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	result, err := svc.CancelCall(ctx, workspaceID, callID, userID)
	if err != nil {
		t.Fatalf("CancelCall returned error: %v", err)
	}
	if result == nil || !result.Ended {
		t.Fatalf("CancelCall result = %+v, want ended", result)
	}
	if callEntity := calls.calls[callID]; callEntity.Status != entity.CallStatusEnded || callEntity.EndReason != entity.CallEndReasonCancelled {
		t.Fatalf("call lifecycle = status %q reason %q, want ended/cancelled", callEntity.Status, callEntity.EndReason)
	}

	second, err := svc.CancelCall(ctx, workspaceID, callID, userID)
	if err != nil {
		t.Fatalf("second CancelCall returned error: %v", err)
	}
	if second == nil || second.Ended {
		t.Fatalf("second CancelCall result = %+v, want not ended", second)
	}
}

func TestIssueLiveKitJoinInfoUsesParticipantRoleForPublishGrant(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	viewerID := uuid.New()
	apiSecret := "aloqa-livekit-test-secret"

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeWebinar,
				Status:      entity.CallStatusActive,
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, viewerID}: {
				ID:     uuid.New(),
				CallID: callID,
				UserID: viewerID,
				Role:   entity.CallRoleViewer,
				Status: entity.ParticipantStatusConnected,
			},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	svc.SetLiveKit(LiveKitSettings{
		URL:       "ws://livekit.test",
		APIKey:    "APItest",
		APISecret: apiSecret,
		TokenTTL:  time.Hour,
	})

	info, err := svc.IssueLiveKitJoinInfo(ctx, calls.calls[callID], viewerID, "")
	if err != nil {
		t.Fatalf("IssueLiveKitJoinInfo returned error: %v", err)
	}
	verifier, err := auth.ParseAPIToken(info.AccessToken)
	if err != nil {
		t.Fatalf("ParseAPIToken returned error: %v", err)
	}
	_, grants, err := verifier.Verify(apiSecret)
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}

	if grants.Video == nil {
		t.Fatalf("token video grant = nil")
	}
	if grants.Video.CanPublish == nil || *grants.Video.CanPublish {
		t.Fatalf("CanPublish = %v, want false for viewer", grants.Video.CanPublish)
	}
	if grants.Video.CanSubscribe == nil || !*grants.Video.CanSubscribe {
		t.Fatalf("CanSubscribe = %v, want true for viewer", grants.Video.CanSubscribe)
	}
}

func TestIssueLiveKitJoinInfoRejectsWaitingParticipant(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()

	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {
				ID:          callID,
				WorkspaceID: workspaceID,
				Type:        entity.CallTypeMeeting,
				Status:      entity.CallStatusActive,
				Settings:    entity.CallSettings{WaitingRoom: true},
			},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {
				ID:     uuid.New(),
				CallID: callID,
				UserID: userID,
				Role:   entity.CallRoleParticipant,
				Status: entity.ParticipantStatusWaiting,
			},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
	svc.SetLiveKit(LiveKitSettings{
		URL:       "ws://livekit.test",
		APIKey:    "APItest",
		APISecret: "aloqa-livekit-test-secret",
		TokenTTL:  time.Hour,
	})

	if _, err := svc.IssueLiveKitJoinInfo(ctx, calls.calls[callID], userID, ""); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("IssueLiveKitJoinInfo waiting error = %v, want FORBIDDEN", err)
	}
}

func TestCancelCallDoesNotEndWhenCallWasActivatedConcurrently(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeOneToOne, Status: entity.CallStatusRinging, CreatedBy: userID},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {ID: uuid.New(), CallID: callID, UserID: userID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
		},
		cancelBeforeUpdate: func(call *entity.Call) {
			call.Status = entity.CallStatusActive
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	result, err := svc.CancelCall(ctx, workspaceID, callID, userID)
	if !hasCode(err, cerrors.CodeConflict) {
		t.Fatalf("CancelCall error = %v, want CONFLICT", err)
	}
	if result != nil {
		t.Fatalf("CancelCall result = %+v, want nil", result)
	}
	if callEntity := calls.calls[callID]; callEntity.Status != entity.CallStatusActive || callEntity.EndReason != "" || callEntity.EndedAt != nil {
		t.Fatalf("call lifecycle = status %q reason %q ended_at %v, want active/no reason/no ended_at", callEntity.Status, callEntity.EndReason, callEntity.EndedAt)
	}
}

func TestReportNetworkQualityAppliesMeetingWidePolicyAndPersistsClientSnapshot(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	callID := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {ID: uuid.New(), CallID: callID, UserID: userID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
		},
	}
	sfuServer, err := sfu.NewSFU(sfu.Config{})
	if err != nil {
		t.Fatalf("NewSFU returned error: %v", err)
	}
	defer sfuServer.Close()
	room, err := sfuServer.CreateRoom(callID.String(), sfu.RoomOptions{MaxPresenters: 5, MaxViewers: 0})
	if err != nil {
		t.Fatalf("CreateRoom returned error: %v", err)
	}
	pc, err := sfuServer.NewPeerConnection()
	if err != nil {
		t.Fatalf("NewPeerConnection returned error: %v", err)
	}
	defer pc.Close()
	if _, err := room.AddPresenter(userID.String(), pc); err != nil {
		t.Fatalf("AddPresenter returned error: %v", err)
	}

	control := &fakeMediaControlPlane{
		localNodeID: "edge-a",
		policy: entity.MediaCallPolicy{
			MaxParticipants:     500,
			MaxPresenters:       32,
			RoutingMode:         entity.MediaRoutingStickyEdge,
			FanoutStrategy:      entity.MediaFanoutRegionalCascade,
			OverflowPolicy:      entity.MediaOverflowRegionalMove,
			ScreenSharePriority: entity.MediaScreenShareProtected,
			TURNStrategy:        "regional_turn_pool",
			Sticky:              true,
		},
		qualityPolicy: &entity.MediaQualityPolicy{
			WorkspaceID:          workspaceID,
			CallID:               callID,
			Mode:                 entity.MediaQualityPolicyAudioOnly,
			MeetingWideDowngrade: true,
			AlertingEnabled:      true,
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, sfuServer, mediaTestConfig(), nil, nil)
	svc.SetMediaControlPlane(control)

	decision, err := svc.ReportNetworkQuality(ctx, workspaceID, callID, userID, MediaQualityReportInput{
		StreamID:             "camera",
		AvailableBitrateKbps: 1800,
		ObservedBitrateKbps:  1600,
		PacketLossPct:        1,
		RoundTripTimeMs:      90,
		JitterMs:             8,
	})
	if err != nil {
		t.Fatalf("ReportNetworkQuality returned error: %v", err)
	}
	if !decision.VideoSuspended {
		t.Fatalf("VideoSuspended = false, want true under audio-only policy")
	}
	if decision.MaxVideoBitrateKbps != 0 {
		t.Fatalf("MaxVideoBitrateKbps = %d, want 0 under audio-only policy", decision.MaxVideoBitrateKbps)
	}
	if len(control.recordedSnapshots) != 1 || control.recordedSnapshots[0].Source != entity.MediaTelemetrySourceClient {
		t.Fatalf("client snapshot not recorded: %+v", control.recordedSnapshots)
	}
}

func hasCode(err error, code cerrors.Code) bool {
	appErr, ok := cerrors.AsAppError(err)
	return ok && appErr.Code == code
}

func mediaTestConfig() MediaConfig {
	return MediaConfig{
		TokenSecret:              []byte("01234567890123456789012345678901"),
		TokenTTL:                 time.Minute,
		MaxPresentersPerCall:     50,
		MaxViewersPerCall:        10000,
		MaxScreenSharesPerCall:   1,
		MaxTracksPerPresenter:    8,
		DefaultWebinarPresenters: 50,
	}
}

type noopPublisher struct{}

func (noopPublisher) Publish(context.Context, string, []byte) error { return nil }

type capturingPublisher struct {
	called   bool
	subject  string
	body     []byte
	captures []capturedPublish
}

type capturedPublish struct {
	subject string
	body    []byte
}

type fakeCallCollabChecker struct {
	decision collabaccess.Decision
	err      error
}

func (f fakeCallCollabChecker) AuthorizeCall(context.Context, uuid.UUID, uuid.UUID) (collabaccess.Decision, error) {
	return f.decision, f.err
}

func (p *capturingPublisher) Publish(_ context.Context, subject string, body []byte) error {
	p.called = true
	p.subject = subject
	p.body = append([]byte(nil), body...)
	p.captures = append(p.captures, capturedPublish{
		subject: subject,
		body:    append([]byte(nil), body...),
	})
	return nil
}

type fakeMediaControlPlane struct {
	localNodeID       string
	policy            entity.MediaCallPolicy
	placement         *entity.MediaRoomPlacement
	resolvedPlacement *entity.MediaRoomPlacement
	canServe          bool
	err               error
	qualityPolicy     *entity.MediaQualityPolicy
	recordedSnapshots []entity.MediaQoSSample
}

func (f fakeMediaControlPlane) EnsurePlacement(context.Context, *entity.Call, sfu.RoomOptions) (*entity.MediaRoomPlacement, error) {
	return f.placement, f.err
}

func (f fakeMediaControlPlane) ResolveParticipantPlacement(context.Context, *entity.Call, *entity.CallParticipant, string) (*entity.MediaRoomPlacement, error) {
	if f.resolvedPlacement != nil {
		return f.resolvedPlacement, f.err
	}
	return f.placement, f.err
}

func (f fakeMediaControlPlane) CanServeNode(context.Context, *entity.Call, string) (bool, error) {
	if f.canServe {
		return true, f.err
	}
	if f.placement == nil {
		return true, f.err
	}
	return f.placement.NodeID == f.localNodeID, f.err
}

func (f fakeMediaControlPlane) PolicyForCall(*entity.Call) entity.MediaCallPolicy {
	return f.policy
}

func (f fakeMediaControlPlane) LocalNodeID() string {
	return f.localNodeID
}

func (f fakeMediaControlPlane) IsLocalNode(nodeID string) bool {
	return nodeID == f.localNodeID
}

func (f *fakeMediaControlPlane) GetCallQualityPolicy(context.Context, uuid.UUID, uuid.UUID) (*entity.MediaQualityPolicy, error) {
	if f.qualityPolicy == nil {
		return &entity.MediaQualityPolicy{Mode: entity.MediaQualityPolicyAuto}, nil
	}
	return f.qualityPolicy, nil
}

func (f *fakeMediaControlPlane) RecordQualitySnapshot(_ context.Context, sample entity.MediaQoSSample) error {
	f.recordedSnapshots = append(f.recordedSnapshots, sample)
	return nil
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
func (r *fakeWorkspaceRepo) Update(context.Context, *entity.Workspace) error { return nil }
func (r *fakeWorkspaceRepo) AddMember(context.Context, *entity.WorkspaceMember) error {
	return nil
}
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

type fakeGuestAccessRepo struct {
	grants []entity.GuestAccessGrant
}

func (r *fakeGuestAccessRepo) CreateGrant(context.Context, *entity.GuestAccessGrant) error {
	return nil
}
func (r *fakeGuestAccessRepo) ListActiveByUserWorkspace(_ context.Context, userID, workspaceID uuid.UUID, now time.Time) ([]entity.GuestAccessGrant, error) {
	var active []entity.GuestAccessGrant
	for _, grant := range r.grants {
		if grant.UserID == userID && grant.WorkspaceID == workspaceID && grant.ExpiresAt.After(now) {
			active = append(active, grant)
		}
	}
	return active, nil
}

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
	if r.members != nil {
		if member := r.members[[2]uuid.UUID{channelID, userID}]; member != nil {
			return member, nil
		}
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

type fakeCallRepo struct {
	calls                             map[uuid.UUID]*entity.Call
	participants                      map[[2]uuid.UUID]*entity.CallParticipant
	invited                           map[uuid.UUID]map[uuid.UUID]bool
	liveKitWebhookEvents              map[string]*entity.LiveKitWebhookEvent
	liveKitWebhookClaimAttempts       map[string]int
	settingsUpdates                   int
	markLiveKitWebhookBeforeProcessed func(event *entity.LiveKitWebhookEvent)
	cancelBeforeUpdate                func(call *entity.Call)
	activateBeforeUpdate              func(call *entity.Call)
	disconnectBeforeUpdate            func(participant *entity.CallParticipant)
}

func liveKitWebhookEvent(eventID, eventType string, callID, userID uuid.UUID) *livekitpb.WebhookEvent {
	ev := &livekitpb.WebhookEvent{
		Id:    eventID,
		Event: eventType,
		Room:  &livekitpb.Room{Name: callID.String()},
	}
	if userID != uuid.Nil {
		ev.Participant = &livekitpb.ParticipantInfo{Identity: userID.String()}
	}
	return ev
}

func (r *fakeCallRepo) Create(_ context.Context, call *entity.Call) error {
	if r.calls == nil {
		r.calls = map[uuid.UUID]*entity.Call{}
	}
	r.calls[call.ID] = call
	return nil
}
func (r *fakeCallRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Call, error) {
	if call := r.calls[id]; call != nil {
		return call, nil
	}
	return nil, cerrors.NotFound("call not found")
}
func (r *fakeCallRepo) ListActiveByWorkspace(_ context.Context, workspaceID uuid.UUID) ([]entity.Call, error) {
	var calls []entity.Call
	for _, call := range r.calls {
		if call.Status != entity.CallStatusEnded && call.WorkspaceID == workspaceID {
			calls = append(calls, *call)
		}
	}
	return calls, nil
}
func (r *fakeCallRepo) ListRecentByWorkspace(_ context.Context, workspaceID uuid.UUID, limit int, before *time.Time) ([]entity.Call, error) {
	var calls []entity.Call
	for _, call := range r.calls {
		if call.WorkspaceID != workspaceID {
			continue
		}
		if before != nil && !call.CreatedAt.Before(*before) {
			continue
		}
		calls = append(calls, *call)
	}
	sort.Slice(calls, func(i, j int) bool {
		return calls[i].CreatedAt.After(calls[j].CreatedAt)
	})
	if limit > 0 && len(calls) > limit {
		return calls[:limit], nil
	}
	return calls, nil
}
func (r *fakeCallRepo) ListStaleOpen(_ context.Context, before time.Time, limit int) ([]entity.Call, error) {
	calls := []entity.Call{}
	for _, call := range r.calls {
		if call.Status == entity.CallStatusEnded {
			continue
		}
		staleAt := call.CreatedAt
		if call.StartedAt != nil {
			staleAt = *call.StartedAt
		}
		if !staleAt.Before(before) {
			continue
		}
		calls = append(calls, *call)
		if limit > 0 && len(calls) >= limit {
			break
		}
	}
	return calls, nil
}
func (r *fakeCallRepo) UpdateStatus(_ context.Context, id uuid.UUID, status entity.CallStatus) error {
	call := r.calls[id]
	if call == nil {
		return cerrors.NotFound("call not found")
	}
	call.Status = status
	return nil
}
func (r *fakeCallRepo) UpdateSettings(_ context.Context, id uuid.UUID, settings entity.CallSettings) error {
	call := r.calls[id]
	if call == nil {
		return cerrors.NotFound("call not found")
	}
	r.settingsUpdates++
	call.Settings = settings
	return nil
}

func (r *fakeCallRepo) UpdateAccessLevel(_ context.Context, id uuid.UUID, level entity.AccessLevel) error {
	call := r.calls[id]
	if call == nil {
		return cerrors.NotFound("call not found")
	}
	call.AccessLevel = level.Resolved()
	return nil
}

func (r *fakeCallRepo) AddInvitedMembers(_ context.Context, callID uuid.UUID, userIDs []uuid.UUID, _ uuid.UUID) error {
	if r.invited == nil {
		r.invited = map[uuid.UUID]map[uuid.UUID]bool{}
	}
	if r.invited[callID] == nil {
		r.invited[callID] = map[uuid.UUID]bool{}
	}
	for _, u := range userIDs {
		r.invited[callID][u] = true
	}
	return nil
}

func (r *fakeCallRepo) IsInvited(_ context.Context, callID, userID uuid.UUID) (bool, error) {
	return r.invited[callID][userID], nil
}

func (r *fakeCallRepo) ListInvitedMembers(_ context.Context, callID uuid.UUID) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, len(r.invited[callID]))
	for u := range r.invited[callID] {
		ids = append(ids, u)
	}
	return ids, nil
}

func (r *fakeCallRepo) SnapshotConnectedIntoInvited(_ context.Context, callID, _ uuid.UUID) error {
	if r.invited == nil {
		r.invited = map[uuid.UUID]map[uuid.UUID]bool{}
	}
	if r.invited[callID] == nil {
		r.invited[callID] = map[uuid.UUID]bool{}
	}
	for key, p := range r.participants {
		if key[0] == callID && p.Status == entity.ParticipantStatusConnected && !p.IsGuest {
			r.invited[callID][p.UserID] = true
		}
	}
	return nil
}
func (r *fakeCallRepo) ActivateRinging(_ context.Context, id uuid.UUID) (bool, error) {
	call := r.calls[id]
	if call == nil {
		return false, cerrors.NotFound("call not found")
	}
	if r.activateBeforeUpdate != nil {
		r.activateBeforeUpdate(call)
		r.activateBeforeUpdate = nil
	}
	if call.Status != entity.CallStatusRinging {
		return false, nil
	}
	now := time.Now().UTC()
	call.Status = entity.CallStatusActive
	call.StartedAt = &now
	return true, nil
}
func (r *fakeCallRepo) End(ctx context.Context, id uuid.UUID) error {
	return r.EndWithReason(ctx, id, "")
}
func (r *fakeCallRepo) ClaimLiveKitWebhookEvent(_ context.Context, event *entity.LiveKitWebhookEvent) (entity.LiveKitWebhookClaimResult, error) {
	if r.liveKitWebhookEvents == nil {
		r.liveKitWebhookEvents = map[string]*entity.LiveKitWebhookEvent{}
	}
	if r.liveKitWebhookClaimAttempts == nil {
		r.liveKitWebhookClaimAttempts = map[string]int{}
	}
	r.liveKitWebhookClaimAttempts[event.EventID]++
	if existing := r.liveKitWebhookEvents[event.EventID]; existing != nil {
		if existing.Status == "processed" {
			return entity.LiveKitWebhookClaimDuplicate, nil
		}
		if existing.LeaseExpiresAt != nil && existing.LeaseExpiresAt.After(time.Now().UTC()) {
			return entity.LiveKitWebhookClaimInProgress, nil
		}
	}
	r.liveKitWebhookEvents[event.EventID] = event
	return entity.LiveKitWebhookClaimProcess, nil
}
func (r *fakeCallRepo) MarkLiveKitWebhookEventProcessed(_ context.Context, eventID, claimToken string) (bool, error) {
	event := r.liveKitWebhookEvents[eventID]
	if event == nil {
		return true, nil
	}
	if r.markLiveKitWebhookBeforeProcessed != nil {
		r.markLiveKitWebhookBeforeProcessed(event)
	}
	if event.ClaimToken != claimToken {
		return false, nil
	}
	now := time.Now().UTC()
	event.Status = "processed"
	event.ProcessedAt = &now
	event.LeaseExpiresAt = nil
	return true, nil
}
func (r *fakeCallRepo) EndWithReason(_ context.Context, id uuid.UUID, reason entity.CallEndReason) error {
	_, err := r.EndWithReasonIfNotEnded(context.Background(), id, reason)
	return err
}
func (r *fakeCallRepo) EndWithReasonIfNotEnded(_ context.Context, id uuid.UUID, reason entity.CallEndReason) (bool, error) {
	call := r.calls[id]
	if call == nil {
		return false, cerrors.NotFound("call not found")
	}
	if call.Status == entity.CallStatusEnded {
		return false, nil
	}
	now := time.Now().UTC()
	call.Status = entity.CallStatusEnded
	call.EndedAt = &now
	if call.EndReason == "" {
		call.EndReason = reason
	}
	// Mirror postgres EndWithReasonIfNotEnded: ending a call atomically
	// disconnects its still-live participants in the same statement. (zombie-calls)
	r.disconnectLiveParticipants(id, entity.ParticipantLeftReasonTimeout)
	return true, nil
}
func (r *fakeCallRepo) CancelRingingWithReason(_ context.Context, id uuid.UUID, reason entity.CallEndReason) (bool, error) {
	call := r.calls[id]
	if call == nil {
		return false, cerrors.NotFound("call not found")
	}
	if r.cancelBeforeUpdate != nil {
		r.cancelBeforeUpdate(call)
		r.cancelBeforeUpdate = nil
	}
	if call.Status != entity.CallStatusRinging {
		return false, nil
	}
	now := time.Now().UTC()
	call.Status = entity.CallStatusEnded
	call.EndedAt = &now
	if call.EndReason == "" {
		call.EndReason = reason
	}
	// Mirror postgres CancelRingingWithReason. (zombie-calls)
	r.disconnectLiveParticipants(id, entity.ParticipantLeftReasonMissed)
	return true, nil
}

// disconnectLiveParticipants mirrors the CTE in postgres endCallAndDisconnect:
// every joining/connected/waiting participant of the call becomes disconnected,
// preserving any left_at/left_reason already set (COALESCE semantics).
func (r *fakeCallRepo) disconnectLiveParticipants(callID uuid.UUID, reason entity.ParticipantLeftReason) {
	now := time.Now().UTC()
	for key, p := range r.participants {
		if key[0] != callID {
			continue
		}
		switch p.Status {
		case entity.ParticipantStatusJoining, entity.ParticipantStatusConnected, entity.ParticipantStatusWaiting:
			p.Status = entity.ParticipantStatusDisconnected
			if p.LeftAt == nil {
				p.LeftAt = &now
			}
			if p.LeftReason == "" {
				p.LeftReason = reason
			}
		}
	}
}
func (r *fakeCallRepo) AddParticipant(_ context.Context, p *entity.CallParticipant) error {
	if r.participants == nil {
		r.participants = map[[2]uuid.UUID]*entity.CallParticipant{}
	}
	r.participants[[2]uuid.UUID{p.CallID, p.UserID}] = p
	return nil
}
func (r *fakeCallRepo) AddParticipantIfCapacity(_ context.Context, p *entity.CallParticipant, _ int) error {
	return r.AddParticipant(context.Background(), p)
}
func (r *fakeCallRepo) GetParticipant(_ context.Context, callID, userID uuid.UUID) (*entity.CallParticipant, error) {
	if p := r.participants[[2]uuid.UUID{callID, userID}]; p != nil {
		return p, nil
	}
	return nil, cerrors.NotFound("call participant not found")
}
func (r *fakeCallRepo) ListParticipants(_ context.Context, callID uuid.UUID) ([]entity.CallParticipant, error) {
	var participants []entity.CallParticipant
	for key, p := range r.participants {
		if key[0] == callID {
			participants = append(participants, *p)
		}
	}
	return participants, nil
}
func (r *fakeCallRepo) UpdateParticipantStatus(ctx context.Context, id uuid.UUID, status entity.ParticipantStatus) error {
	return r.UpdateParticipantStatusWithReason(ctx, id, status, "")
}
func (r *fakeCallRepo) UpdateParticipantStatusWithReason(_ context.Context, id uuid.UUID, status entity.ParticipantStatus, reason entity.ParticipantLeftReason) error {
	for _, p := range r.participants {
		if p.ID != id {
			continue
		}
		now := time.Now().UTC()
		p.Status = status
		if status == entity.ParticipantStatusConnected {
			p.JoinedAt = &now
			p.LeftAt = nil
			p.LeftReason = ""
		}
		if status == entity.ParticipantStatusDisconnected {
			p.LeftAt = &now
			p.LeftReason = reason
		}
		return nil
	}
	return cerrors.NotFound("call participant not found")
}
func (r *fakeCallRepo) DisconnectParticipantIfConnectedWithReason(_ context.Context, id uuid.UUID, reason entity.ParticipantLeftReason) (bool, error) {
	for _, p := range r.participants {
		if p.ID != id {
			continue
		}
		if r.disconnectBeforeUpdate != nil {
			r.disconnectBeforeUpdate(p)
			r.disconnectBeforeUpdate = nil
		}
		if p.Status == entity.ParticipantStatusDisconnected {
			return false, nil
		}
		now := time.Now().UTC()
		p.Status = entity.ParticipantStatusDisconnected
		p.LeftAt = &now
		if p.LeftReason == "" {
			p.LeftReason = reason
		}
		return true, nil
	}
	return false, cerrors.NotFound("call participant not found")
}
func (r *fakeCallRepo) UpdateParticipantRole(_ context.Context, id uuid.UUID, role entity.CallRole) error {
	for _, p := range r.participants {
		if p.ID == id {
			p.Role = role
			return nil
		}
	}
	return cerrors.NotFound("call participant not found")
}
func (r *fakeCallRepo) TransferHost(_ context.Context, callID, fromUserID, toUserID uuid.UUID) (bool, error) {
	from := r.participants[[2]uuid.UUID{callID, fromUserID}]
	to := r.participants[[2]uuid.UUID{callID, toUserID}]
	if from == nil || to == nil || from.Role != entity.CallRoleHost ||
		to.Role == entity.CallRoleHost || to.Status != entity.ParticipantStatusConnected {
		return false, nil
	}
	from.Role = entity.CallRoleParticipant
	to.Role = entity.CallRoleHost
	return true, nil
}
func (r *fakeCallRepo) UpdateParticipantMedia(_ context.Context, id uuid.UUID, audioMuted, videoMuted, screenSharing bool) error {
	for _, p := range r.participants {
		if p.ID == id {
			p.AudioMuted = audioMuted
			p.VideoMuted = videoMuted
			p.ScreenSharing = screenSharing
			return nil
		}
	}
	return cerrors.NotFound("call participant not found")
}
func (r *fakeCallRepo) SetCanScreenShare(_ context.Context, id uuid.UUID, canShare bool) error {
	for _, p := range r.participants {
		if p.ID == id {
			p.CanScreenShare = canShare
			return nil
		}
	}
	return cerrors.NotFound("participant not found")
}
func (r *fakeCallRepo) SetFeaturedShareUserID(_ context.Context, callID uuid.UUID, userID *uuid.UUID) error {
	if c := r.calls[callID]; c != nil {
		c.FeaturedShareUserID = userID
		return nil
	}
	return cerrors.NotFound("call not found")
}
func (r *fakeCallRepo) SetPinnedParticipantUserID(_ context.Context, callID uuid.UUID, userID *uuid.UUID) error {
	if c := r.calls[callID]; c != nil {
		c.PinnedParticipantUserID = userID
		return nil
	}
	return cerrors.NotFound("call not found")
}
func (r *fakeCallRepo) RemoveParticipant(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type removedLiveKitParticipant struct {
	callID uuid.UUID
	userID uuid.UUID
}

type updatedLiveKitParticipant struct {
	room     string
	identity string
	perm     *livekitpb.ParticipantPermission
}

type fakeLiveKitRoomClient struct {
	ensureCalls           int
	ensureErr             error
	ensuredRoomNames      []string
	deletedRoomNames      []string
	removedParticipants   []removedLiveKitParticipant
	participantsByCall    map[uuid.UUID][]*livekitpb.ParticipantInfo
	participantsByRoom    map[string][]*livekitpb.ParticipantInfo
	listParticipantsErr   error
	listParticipantsCalls int
	updatedParticipants   []updatedLiveKitParticipant
	updateParticipantErr  error
	mutedTracks           []mutedLiveKitTrack
	mutePublishedTrackErr error
}

type mutedLiveKitTrack struct {
	room     string
	identity string
	trackSID string
}

func (c *fakeLiveKitRoomClient) EnsureRoom(_ context.Context, args LiveKitEnsureRoomArgs) error {
	c.ensureCalls++
	name := args.RoomName
	if name == "" {
		name = args.CallID.String()
	}
	c.ensuredRoomNames = append(c.ensuredRoomNames, name)
	return c.ensureErr
}

func (c *fakeLiveKitRoomClient) DeleteRoom(_ context.Context, callID uuid.UUID) error {
	c.deletedRoomNames = append(c.deletedRoomNames, callID.String())
	return nil
}

func (c *fakeLiveKitRoomClient) DeleteRoomByName(_ context.Context, name string) error {
	c.deletedRoomNames = append(c.deletedRoomNames, name)
	return nil
}

func (c *fakeLiveKitRoomClient) RemoveParticipant(_ context.Context, callID, userID uuid.UUID) error {
	c.removedParticipants = append(c.removedParticipants, removedLiveKitParticipant{
		callID: callID,
		userID: userID,
	})
	return nil
}

func (c *fakeLiveKitRoomClient) ListParticipants(ctx context.Context, callID uuid.UUID) ([]*livekitpb.ParticipantInfo, error) {
	return c.ListParticipantsByRoom(ctx, callID.String())
}

func (c *fakeLiveKitRoomClient) ListParticipantsByRoom(_ context.Context, room string) ([]*livekitpb.ParticipantInfo, error) {
	c.listParticipantsCalls++
	if c.listParticipantsErr != nil {
		return nil, c.listParticipantsErr
	}
	if c.participantsByRoom != nil {
		return c.participantsByRoom[room], nil
	}
	callID, err := uuid.Parse(room)
	if err != nil {
		return nil, nil
	}
	return c.participantsByCall[callID], nil
}

func (c *fakeLiveKitRoomClient) UpdateParticipant(_ context.Context, room, identity string, perm *livekitpb.ParticipantPermission) error {
	if c.updateParticipantErr != nil {
		return c.updateParticipantErr
	}
	c.updatedParticipants = append(c.updatedParticipants, updatedLiveKitParticipant{room: room, identity: identity, perm: perm})
	return nil
}

func (c *fakeLiveKitRoomClient) MutePublishedTrack(_ context.Context, room, identity, trackSID string) error {
	if c.mutePublishedTrackErr != nil {
		return c.mutePublishedTrackErr
	}
	c.mutedTracks = append(c.mutedTracks, mutedLiveKitTrack{room: room, identity: identity, trackSID: trackSID})
	// Reflect the mute so a subsequent ListParticipants sees the track muted (the
	// real RoomService does this), letting an idempotent reconcile pass skip it.
	for _, parts := range c.participantsByCall {
		for _, p := range parts {
			if p.GetIdentity() != identity {
				continue
			}
			for _, tr := range p.GetTracks() {
				if tr.GetSid() == trackSID {
					tr.Muted = true
				}
			}
		}
	}
	for _, parts := range c.participantsByRoom {
		for _, p := range parts {
			if p.GetIdentity() != identity {
				continue
			}
			for _, tr := range p.GetTracks() {
				if tr.GetSid() == trackSID {
					tr.Muted = true
				}
			}
		}
	}
	return nil
}

type fakeBreakoutRepo struct{}

func (fakeBreakoutRepo) Create(context.Context, *entity.BreakoutRoom) error { return nil }
func (fakeBreakoutRepo) CreateRoomsWithinCap(context.Context, uuid.UUID, int, []entity.BreakoutRoom) error {
	return nil
}
func (fakeBreakoutRepo) GetByID(context.Context, uuid.UUID) (*entity.BreakoutRoom, error) {
	return nil, cerrors.NotFound("breakout room not found")
}
func (fakeBreakoutRepo) ListByCall(context.Context, uuid.UUID) ([]entity.BreakoutRoom, error) {
	return nil, nil
}
func (fakeBreakoutRepo) ListCallsWithExpiredActiveBreakouts(context.Context, time.Time, int) ([]uuid.UUID, error) {
	return nil, nil
}
func (fakeBreakoutRepo) Close(context.Context, uuid.UUID) error { return nil }
func (fakeBreakoutRepo) CloseAllByCall(context.Context, uuid.UUID) error {
	return nil
}
func (fakeBreakoutRepo) AssignParticipant(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return nil
}
func (fakeBreakoutRepo) UnassignParticipant(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (fakeBreakoutRepo) UnassignAllByRoom(context.Context, uuid.UUID) error {
	return nil
}
func (fakeBreakoutRepo) ListParticipants(context.Context, uuid.UUID) ([]entity.CallParticipant, error) {
	return nil, nil
}
