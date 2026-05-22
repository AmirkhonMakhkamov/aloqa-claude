package call

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

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
	calls        map[uuid.UUID]*entity.Call
	participants map[[2]uuid.UUID]*entity.CallParticipant
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
func (r *fakeCallRepo) UpdateStatus(context.Context, uuid.UUID, entity.CallStatus) error {
	return nil
}
func (r *fakeCallRepo) End(context.Context, uuid.UUID) error { return nil }
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
