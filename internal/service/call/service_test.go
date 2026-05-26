package call

import (
	"context"
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

	if _, err := svc.StartCall(ctx, workspaceA, userID, entity.CallTypeMeeting, "", &channelID, entity.CallSettings{}); !hasCode(err, cerrors.CodeNotFound) {
		t.Fatalf("StartCall with cross-workspace channel error = %v, want NOT_FOUND", err)
	}

	if _, err := svc.JoinCall(ctx, workspaceA, callID, userID); !hasCode(err, cerrors.CodeNotFound) {
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

func TestMediaJoinTokenIsCallScopedAndRoleAware(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	callID := uuid.New()
	otherCallID := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeWebinar, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {ID: uuid.New(), CallID: callID, UserID: userID, Role: entity.CallRoleViewer, Status: entity.ParticipantStatusConnected},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	token, err := svc.IssueMediaJoinToken(ctx, workspaceID, callID, userID)
	if err != nil {
		t.Fatalf("IssueMediaJoinToken returned error: %v", err)
	}
	if token.Role != mediaRoleViewer {
		t.Fatalf("media role = %q, want %q", token.Role, mediaRoleViewer)
	}
	if err := svc.AddMediaICECandidate(ctx, MediaICECandidateInput{
		CallID:    otherCallID,
		Token:     token.Token,
		Candidate: "candidate:0 1 UDP 2122252543 127.0.0.1 12345 typ host",
	}); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("AddMediaICECandidate with wrong route call error = %v, want FORBIDDEN", err)
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

	participant, err := svc.JoinCall(ctx, workspaceID, callID, guestID)
	if err != nil {
		t.Fatalf("JoinCall guest returned error: %v", err)
	}
	if participant == nil || participant.UserID != guestID {
		t.Fatalf("expected guest participant to be created")
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

	participant, err := svc.JoinCall(ctx, workspaceID, callID, remoteUserID)
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

	if _, err := svc.JoinCall(ctx, workspaceID, callID, remoteUserID); !hasCode(err, cerrors.CodeForbidden) {
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

func TestMediaJoinTokenIncludesPlacementAndRejectsWrongEdge(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	callID := uuid.New()

	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeWebinar, Status: entity.CallStatusActive},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, userID}: {ID: uuid.New(), CallID: callID, UserID: userID, Role: entity.CallRoleViewer, Status: entity.ParticipantStatusConnected},
		},
	}
	sfuServer, err := sfu.NewSFU(sfu.Config{})
	if err != nil {
		t.Fatalf("NewSFU returned error: %v", err)
	}
	defer sfuServer.Close()

	placement := &entity.MediaRoomPlacement{
		CallID:              callID,
		WorkspaceID:         workspaceID,
		NodeID:              "edge-b",
		Region:              "eu-central",
		ControlURL:          "https://edge-b.example.com",
		MediaURL:            "wss://edge-b.example.com/media",
		RoutingMode:         entity.MediaRoutingRegionalEdge,
		FanoutStrategy:      entity.MediaFanoutWebinarEdges,
		OverflowPolicy:      entity.MediaOverflowWebinarEdge,
		ScreenSharePriority: entity.MediaScreenShareProtected,
		TURNStrategy:        "regional_turn_pool",
		Sticky:              true,
		MaxParticipants:     10000,
		MaxPresenters:       50,
		MaxViewers:          10000,
	}

	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, noopPublisher{}, sfuServer, mediaTestConfig(), nil, nil)
	svc.SetMediaControlPlane(&fakeMediaControlPlane{
		localNodeID: "edge-a",
		policy: entity.MediaCallPolicy{
			MaxParticipants:     10000,
			MaxPresenters:       50,
			MaxViewers:          10000,
			RoutingMode:         entity.MediaRoutingRegionalEdge,
			FanoutStrategy:      entity.MediaFanoutWebinarEdges,
			OverflowPolicy:      entity.MediaOverflowWebinarEdge,
			ScreenSharePriority: entity.MediaScreenShareProtected,
			TURNStrategy:        "regional_turn_pool",
			Sticky:              true,
		},
		placement: placement,
	})

	token, err := svc.IssueMediaJoinToken(ctx, workspaceID, callID, userID)
	if err != nil {
		t.Fatalf("IssueMediaJoinToken returned error: %v", err)
	}
	if token.NodeID != placement.NodeID || token.ControlURL != placement.ControlURL {
		t.Fatalf("token routing = %+v, want placement %+v", token, placement)
	}
	if _, err := svc.HandleMediaOffer(ctx, MediaOfferInput{
		CallID: callID,
		Token:  token.Token,
		SDP:    "v=0",
		Type:   "offer",
	}); !hasCode(err, cerrors.CodeUnavailable) {
		t.Fatalf("HandleMediaOffer wrong-edge error = %v, want UNAVAILABLE", err)
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

	if _, err := svc.JoinCall(ctx, workspaceID, callID, userC); !hasCode(err, cerrors.CodeConflict) {
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

	participant, err := svc.JoinCall(ctx, workspaceID, callID, userID)
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
	if calls.participants[[2]uuid.UUID{callID, userID}].Status != entity.ParticipantStatusConnected {
		t.Fatalf("participant status changed after ended call")
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

	callEntity, err := svc.StartCall(ctx, workspaceID, userID, entity.CallTypeOneToOne, "dm", nil, entity.CallSettings{})
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

	participant, err := svc.JoinCall(ctx, workspaceID, callID, userID)
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
func (r *fakeWorkspaceRepo) ListMembers(context.Context, uuid.UUID, pagination.Params) ([]entity.WorkspaceMember, error) {
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
	liveKitWebhookEvents              map[string]*entity.LiveKitWebhookEvent
	liveKitWebhookClaimAttempts       map[string]int
	markLiveKitWebhookBeforeProcessed func(event *entity.LiveKitWebhookEvent)
	cancelBeforeUpdate                func(call *entity.Call)
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
func (r *fakeCallRepo) ListActiveByWorkspace(context.Context, uuid.UUID) ([]entity.Call, error) {
	return nil, nil
}
func (r *fakeCallRepo) UpdateStatus(_ context.Context, id uuid.UUID, status entity.CallStatus) error {
	call := r.calls[id]
	if call == nil {
		return cerrors.NotFound("call not found")
	}
	call.Status = status
	return nil
}
func (r *fakeCallRepo) ActivateRinging(_ context.Context, id uuid.UUID) (bool, error) {
	call := r.calls[id]
	if call == nil {
		return false, cerrors.NotFound("call not found")
	}
	if call.Status != entity.CallStatusRinging {
		return false, nil
	}
	call.Status = entity.CallStatusActive
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
	return true, nil
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
func (r *fakeCallRepo) UpdateParticipantStatus(context.Context, uuid.UUID, entity.ParticipantStatus) error {
	return nil
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
func (r *fakeCallRepo) UpdateParticipantMedia(context.Context, uuid.UUID, bool, bool, bool) error {
	return nil
}
func (r *fakeCallRepo) RemoveParticipant(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type removedLiveKitParticipant struct {
	callID uuid.UUID
	userID uuid.UUID
}

type fakeLiveKitRoomClient struct {
	ensureCalls         int
	ensureErr           error
	removedParticipants []removedLiveKitParticipant
}

func (c *fakeLiveKitRoomClient) EnsureRoom(context.Context, LiveKitEnsureRoomArgs) error {
	c.ensureCalls++
	return c.ensureErr
}

func (c *fakeLiveKitRoomClient) DeleteRoom(context.Context, uuid.UUID) error {
	return nil
}

func (c *fakeLiveKitRoomClient) RemoveParticipant(_ context.Context, callID, userID uuid.UUID) error {
	c.removedParticipants = append(c.removedParticipants, removedLiveKitParticipant{
		callID: callID,
		userID: userID,
	})
	return nil
}

type fakeBreakoutRepo struct{}

func (fakeBreakoutRepo) Create(context.Context, *entity.BreakoutRoom) error { return nil }
func (fakeBreakoutRepo) GetByID(context.Context, uuid.UUID) (*entity.BreakoutRoom, error) {
	return nil, cerrors.NotFound("breakout room not found")
}
func (fakeBreakoutRepo) ListByCall(context.Context, uuid.UUID) ([]entity.BreakoutRoom, error) {
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
