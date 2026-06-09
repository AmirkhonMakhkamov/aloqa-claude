package call

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	livekitpb "github.com/livekit/protocol/livekit"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/pkg/cerrors"
)

type shareTestEvent struct {
	Type    event.Type      `json:"type"`
	Subject string          `json:"subject"`
	Payload json.RawMessage `json:"payload"`
}

// shareTestHarness builds a meeting call (no channel → workspace-content gate)
// with a host, a participant, and a viewer, plus a wired LiveKit room client so
// grant/revoke ordering can be asserted.
func shareTestHarness(t *testing.T, callStatus entity.CallStatus) (
	*Service, *fakeCallRepo, *fakeLiveKitRoomClient, *capturingPublisher,
	uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID,
) {
	t.Helper()
	workspaceID := uuid.New()
	callID := uuid.New()
	hostID := uuid.New()
	participantID := uuid.New()
	viewerID := uuid.New()

	pub := &capturingPublisher{}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, hostID}:        {WorkspaceID: workspaceID, UserID: hostID, Role: entity.WorkspaceRoleMember},
		{workspaceID, participantID}: {WorkspaceID: workspaceID, UserID: participantID, Role: entity.WorkspaceRoleMember},
		{workspaceID, viewerID}:      {WorkspaceID: workspaceID, UserID: viewerID, Role: entity.WorkspaceRoleMember},
	}}
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{
			// ScreenSharing=true: the meeting permits screen-share at the meeting
			// level, so a per-participant ALK-697 grant resolves to a real screen
			// source (the grant is ANDed with the meeting toggle under ALK-812).
			callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: callStatus, CreatedAt: time.Now(), Settings: entity.CallSettings{ScreenSharing: true}},
		},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}:        {ID: uuid.New(), CallID: callID, UserID: hostID, Role: entity.CallRoleHost, Status: entity.ParticipantStatusConnected},
			{callID, participantID}: {ID: uuid.New(), CallID: callID, UserID: participantID, Role: entity.CallRoleParticipant, Status: entity.ParticipantStatusConnected},
			{callID, viewerID}:      {ID: uuid.New(), CallID: callID, UserID: viewerID, Role: entity.CallRoleViewer, Status: entity.ParticipantStatusConnected},
		},
	}
	rooms := &fakeLiveKitRoomClient{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, workspaces, pub, nil, mediaTestConfig(), nil, nil)
	svc.SetLiveKitRoomClient(rooms)
	return svc, calls, rooms, pub, workspaceID, callID, hostID, participantID
}

func decodeShareEvents(t *testing.T, captures []capturedPublish) []shareTestEvent {
	t.Helper()
	events := make([]shareTestEvent, 0, len(captures))
	for _, c := range captures {
		var ev shareTestEvent
		if err := json.Unmarshal(c.body, &ev); err != nil {
			t.Fatalf("unmarshal published body for %s: %v", c.subject, err)
		}
		events = append(events, ev)
	}
	return events
}

func countShareEvents(t *testing.T, captures []capturedPublish, evtType event.Type) int {
	t.Helper()
	n := 0
	for _, ev := range decodeShareEvents(t, captures) {
		if ev.Type == evtType {
			n++
		}
	}
	return n
}

func hasTrackSource(sources []livekitpb.TrackSource, want livekitpb.TrackSource) bool {
	for _, s := range sources {
		if s == want {
			return true
		}
	}
	return false
}

func TestGrantScreenShareHostAppliesLiveKitThenPersistsThenBroadcasts(t *testing.T) {
	ctx := context.Background()
	svc, calls, rooms, pub, workspaceID, callID, hostID, participantID := shareTestHarness(t, entity.CallStatusActive)

	if err := svc.GrantScreenShare(ctx, workspaceID, callID, hostID, participantID); err != nil {
		t.Fatalf("GrantScreenShare returned error: %v", err)
	}

	// LiveKit permission applied with the screen sources.
	if len(rooms.updatedParticipants) != 1 {
		t.Fatalf("UpdateParticipant calls = %d, want 1", len(rooms.updatedParticipants))
	}
	up := rooms.updatedParticipants[0]
	if up.room != callID.String() || up.identity != participantID.String() {
		t.Fatalf("UpdateParticipant target = (%s,%s), want (%s,%s)", up.room, up.identity, callID, participantID)
	}
	if up.perm == nil || !up.perm.CanPublish || !hasTrackSource(up.perm.CanPublishSources, livekitpb.TrackSource_SCREEN_SHARE) {
		t.Fatalf("UpdateParticipant perm = %+v, want screen-share source", up.perm)
	}

	// Persisted grant.
	if p := calls.participants[[2]uuid.UUID{callID, participantID}]; p == nil || !p.CanScreenShare {
		t.Fatalf("can_screen_share not persisted: %+v", p)
	}

	// Both the participant.updated and the resolved{true} event broadcast.
	if got := countShareEvents(t, pub.captures, event.TypeCallParticipantUpdated); got != 1 {
		t.Fatalf("participant.updated events = %d, want 1", got)
	}
	if got := countShareEvents(t, pub.captures, event.TypeCallShareRequestResolved); got != 1 {
		t.Fatalf("share_request.resolved events = %d, want 1", got)
	}
}

func TestGrantScreenShareRejectsNonHost(t *testing.T) {
	ctx := context.Background()
	svc, _, rooms, pub, workspaceID, callID, _, participantID := shareTestHarness(t, entity.CallStatusActive)

	// participant (non-host) attempts to grant another -> Forbidden.
	if err := svc.GrantScreenShare(ctx, workspaceID, callID, participantID, participantID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("non-host grant error = %v, want FORBIDDEN", err)
	}
	if len(rooms.updatedParticipants) != 0 {
		t.Fatalf("UpdateParticipant called %d times on rejected grant", len(rooms.updatedParticipants))
	}
	if len(pub.captures) != 0 {
		t.Fatalf("published %d events on rejected grant", len(pub.captures))
	}
}

func TestGrantScreenShareRejectsViewerTarget(t *testing.T) {
	ctx := context.Background()
	svc, _, rooms, _, workspaceID, callID, hostID, _ := shareTestHarness(t, entity.CallStatusActive)
	// viewerID is the third participant; reconstruct it from the call participants.
	var viewerID uuid.UUID
	for key, p := range svc.calls.(*fakeCallRepo).participants {
		if p.Role == entity.CallRoleViewer {
			viewerID = key[1]
		}
	}
	if err := svc.GrantScreenShare(ctx, workspaceID, callID, hostID, viewerID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("grant to viewer error = %v, want FORBIDDEN", err)
	}
	if len(rooms.updatedParticipants) != 0 {
		t.Fatalf("UpdateParticipant called on viewer grant")
	}
}

func TestGrantScreenShareLiveKitErrorDoesNotPersistOrBroadcast(t *testing.T) {
	ctx := context.Background()
	svc, calls, rooms, pub, workspaceID, callID, hostID, participantID := shareTestHarness(t, entity.CallStatusActive)
	rooms.updateParticipantErr = errors.New("livekit down")

	if err := svc.GrantScreenShare(ctx, workspaceID, callID, hostID, participantID); err == nil {
		t.Fatalf("GrantScreenShare succeeded despite LiveKit error")
	}
	if p := calls.participants[[2]uuid.UUID{callID, participantID}]; p != nil && p.CanScreenShare {
		t.Fatalf("can_screen_share persisted despite LiveKit error")
	}
	if len(pub.captures) != 0 {
		t.Fatalf("broadcast %d events despite LiveKit error", len(pub.captures))
	}
}

func TestRevokeScreenShareAppliesNonScreenPermissionFirstThenPersists(t *testing.T) {
	ctx := context.Background()
	svc, calls, rooms, _, workspaceID, callID, hostID, participantID := shareTestHarness(t, entity.CallStatusActive)
	// Pre-grant the participant.
	calls.participants[[2]uuid.UUID{callID, participantID}].CanScreenShare = true

	if err := svc.RevokeScreenShare(ctx, workspaceID, callID, hostID, participantID); err != nil {
		t.Fatalf("RevokeScreenShare returned error: %v", err)
	}
	if len(rooms.updatedParticipants) != 1 {
		t.Fatalf("UpdateParticipant calls = %d, want 1", len(rooms.updatedParticipants))
	}
	perm := rooms.updatedParticipants[0].perm
	if perm == nil || !perm.CanPublish || hasTrackSource(perm.CanPublishSources, livekitpb.TrackSource_SCREEN_SHARE) {
		t.Fatalf("revoke perm = %+v, want camera/mic without screen source", perm)
	}
	if p := calls.participants[[2]uuid.UUID{callID, participantID}]; p == nil || p.CanScreenShare {
		t.Fatalf("can_screen_share not cleared: %+v", p)
	}
}

func TestRevokeScreenShareNoOpForHostTarget(t *testing.T) {
	ctx := context.Background()
	svc, _, rooms, pub, workspaceID, callID, hostID, _ := shareTestHarness(t, entity.CallStatusActive)
	// Revoking the host (the actor) is a no-op (host never gated).
	if err := svc.RevokeScreenShare(ctx, workspaceID, callID, hostID, hostID); err != nil {
		t.Fatalf("RevokeScreenShare host no-op error = %v", err)
	}
	if len(rooms.updatedParticipants) != 0 {
		t.Fatalf("UpdateParticipant called for host target")
	}
	if len(pub.captures) != 0 {
		t.Fatalf("broadcast events for host revoke no-op")
	}
}

func TestResolveShareRequestDeniedBroadcastsOnlyResolvedFalse(t *testing.T) {
	ctx := context.Background()
	svc, _, rooms, pub, workspaceID, callID, hostID, participantID := shareTestHarness(t, entity.CallStatusActive)

	if err := svc.ResolveShareRequest(ctx, workspaceID, callID, hostID, participantID, false); err != nil {
		t.Fatalf("ResolveShareRequest(deny) error: %v", err)
	}
	if len(rooms.updatedParticipants) != 0 {
		t.Fatalf("denied resolve must not touch LiveKit")
	}
	if len(pub.captures) != 1 {
		t.Fatalf("published %d events, want 1", len(pub.captures))
	}
	events := decodeShareEvents(t, pub.captures)
	if events[0].Type != event.TypeCallShareRequestResolved {
		t.Fatalf("event type = %s, want resolved", events[0].Type)
	}
	var payload event.ShareRequestResolvedPayload
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal resolved payload: %v", err)
	}
	if payload.Approved || payload.RequesterUserID != participantID || payload.CallID != callID {
		t.Fatalf("payload = %+v, want approved=false requester=%s", payload, participantID)
	}
}

func TestRequestScreenShareUngrantedBroadcastsCreatedAndGrantedNoOps(t *testing.T) {
	ctx := context.Background()
	svc, calls, _, pub, workspaceID, callID, _, participantID := shareTestHarness(t, entity.CallStatusActive)

	// Ungranted connected participant -> created broadcast.
	if err := svc.RequestScreenShare(ctx, workspaceID, callID, participantID); err != nil {
		t.Fatalf("RequestScreenShare error: %v", err)
	}
	if got := countShareEvents(t, pub.captures, event.TypeCallShareRequestCreated); got != 1 {
		t.Fatalf("share_request.created events = %d, want 1", got)
	}

	// Already-granted -> no-op (no further publish).
	pub.captures = nil
	calls.participants[[2]uuid.UUID{callID, participantID}].CanScreenShare = true
	if err := svc.RequestScreenShare(ctx, workspaceID, callID, participantID); err != nil {
		t.Fatalf("RequestScreenShare granted no-op error: %v", err)
	}
	if len(pub.captures) != 0 {
		t.Fatalf("already-granted request published %d events, want 0", len(pub.captures))
	}
}

func TestMuteParticipantHostMutesLiveKitPersistsAndBroadcasts(t *testing.T) {
	ctx := context.Background()
	svc, calls, rooms, pub, workspaceID, callID, hostID, participantID := shareTestHarness(t, entity.CallStatusActive)
	rooms.participantsByCall = map[uuid.UUID][]*livekitpb.ParticipantInfo{
		callID: {
			{Identity: participantID.String(), Tracks: []*livekitpb.TrackInfo{{Sid: "mic-1", Source: livekitpb.TrackSource_MICROPHONE}}},
		},
	}

	if err := svc.MuteParticipant(ctx, workspaceID, callID, hostID, participantID); err != nil {
		t.Fatalf("MuteParticipant error: %v", err)
	}
	if len(rooms.mutedTracks) != 1 || rooms.mutedTracks[0].trackSID != "mic-1" {
		t.Fatalf("muted tracks = %+v, want mic-1", rooms.mutedTracks)
	}
	if p := calls.participants[[2]uuid.UUID{callID, participantID}]; p == nil || !p.AudioMuted {
		t.Fatalf("audio_muted not persisted: %+v", p)
	}
	if got := countShareEvents(t, pub.captures, event.TypeCallParticipantUpdated); got != 1 {
		t.Fatalf("participant.updated events = %d, want 1", got)
	}
	if got := countShareEvents(t, pub.captures, event.TypeCallParticipantMuted); got != 1 {
		t.Fatalf("participant.muted events = %d, want 1", got)
	}
}

func TestMuteParticipantInBreakoutRoomMutesBreakoutLiveKitRoom(t *testing.T) {
	ctx := context.Background()
	svc, calls, rooms, _, workspaceID, callID, hostID, participantID := shareTestHarness(t, entity.CallStatusActive)
	breakoutRoomID := uuid.New()
	participant := calls.participants[[2]uuid.UUID{callID, participantID}]
	participant.BreakoutRoomID = &breakoutRoomID
	roomName := breakoutLiveKitRoomName(callID, breakoutRoomID)
	rooms.participantsByRoom = map[string][]*livekitpb.ParticipantInfo{
		roomName: {
			{Identity: participantID.String(), Tracks: []*livekitpb.TrackInfo{{Sid: "breakout-mic-1", Source: livekitpb.TrackSource_MICROPHONE}}},
		},
	}

	if err := svc.MuteParticipant(ctx, workspaceID, callID, hostID, participantID); err != nil {
		t.Fatalf("MuteParticipant error: %v", err)
	}
	if len(rooms.mutedTracks) != 1 {
		t.Fatalf("muted tracks = %+v, want one breakout track", rooms.mutedTracks)
	}
	if rooms.mutedTracks[0].room != roomName || rooms.mutedTracks[0].trackSID != "breakout-mic-1" {
		t.Fatalf("muted track = %+v, want room=%s sid=breakout-mic-1", rooms.mutedTracks[0], roomName)
	}
}

func TestDisableParticipantCameraMutesCameraTrackPersistsAndBroadcasts(t *testing.T) {
	ctx := context.Background()
	svc, calls, rooms, pub, workspaceID, callID, hostID, participantID := shareTestHarness(t, entity.CallStatusActive)
	rooms.participantsByCall = map[uuid.UUID][]*livekitpb.ParticipantInfo{
		callID: {
			{Identity: participantID.String(), Tracks: []*livekitpb.TrackInfo{{Sid: "cam-1", Source: livekitpb.TrackSource_CAMERA}}},
		},
	}

	if err := svc.DisableParticipantCamera(ctx, workspaceID, callID, hostID, participantID); err != nil {
		t.Fatalf("DisableParticipantCamera error: %v", err)
	}
	if len(rooms.mutedTracks) != 1 || rooms.mutedTracks[0].trackSID != "cam-1" {
		t.Fatalf("muted tracks = %+v, want cam-1", rooms.mutedTracks)
	}
	if p := calls.participants[[2]uuid.UUID{callID, participantID}]; p == nil || !p.VideoMuted {
		t.Fatalf("video_muted not persisted: %+v", p)
	}
	if got := countShareEvents(t, pub.captures, event.TypeCallParticipantMuted); got != 1 {
		t.Fatalf("participant.muted events = %d, want 1", got)
	}
}

func TestAskParticipantToUnmuteBroadcastsRequestOnly(t *testing.T) {
	ctx := context.Background()
	svc, calls, rooms, pub, workspaceID, callID, hostID, participantID := shareTestHarness(t, entity.CallStatusActive)
	calls.participants[[2]uuid.UUID{callID, participantID}].AudioMuted = true

	if err := svc.AskParticipantToUnmute(ctx, workspaceID, callID, hostID, participantID); err != nil {
		t.Fatalf("AskParticipantToUnmute error: %v", err)
	}
	if len(rooms.mutedTracks) != 0 {
		t.Fatalf("ask-unmute muted tracks = %+v, want none", rooms.mutedTracks)
	}
	if got := countShareEvents(t, pub.captures, event.TypeCallAskUnmute); got != 1 {
		t.Fatalf("ask-unmute events = %d, want 1", got)
	}
	events := decodeShareEvents(t, pub.captures)
	var payload event.CallAskUnmutePayload
	for _, ev := range events {
		if ev.Type != event.TypeCallAskUnmute {
			continue
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("unmarshal ask-unmute payload: %v", err)
		}
	}
	if payload.CallID != callID || payload.UserID != participantID || payload.RequestedByUserID != hostID {
		t.Fatalf("ask-unmute payload = %+v, want call=%s target=%s actor=%s", payload, callID, participantID, hostID)
	}
}

func TestSetFeaturedShareHostPersistsAndBroadcasts(t *testing.T) {
	ctx := context.Background()
	svc, calls, _, pub, workspaceID, callID, hostID, participantID := shareTestHarness(t, entity.CallStatusActive)

	if err := svc.SetFeaturedShare(ctx, workspaceID, callID, hostID, &participantID); err != nil {
		t.Fatalf("SetFeaturedShare error: %v", err)
	}
	c := calls.calls[callID]
	if c.FeaturedShareUserID == nil || *c.FeaturedShareUserID != participantID {
		t.Fatalf("featured_share_user_id = %v, want %s", c.FeaturedShareUserID, participantID)
	}
	if got := countShareEvents(t, pub.captures, event.TypeCallFeaturedShareUpdated); got != 1 {
		t.Fatalf("featured_share.updated events = %d, want 1", got)
	}
}

func TestSetPinnedParticipantHostPersistsAndBroadcasts(t *testing.T) {
	ctx := context.Background()
	svc, calls, _, pub, workspaceID, callID, hostID, participantID := shareTestHarness(t, entity.CallStatusActive)

	if err := svc.SetPinnedParticipant(ctx, workspaceID, callID, hostID, &participantID); err != nil {
		t.Fatalf("SetPinnedParticipant error: %v", err)
	}
	c := calls.calls[callID]
	if c.PinnedParticipantUserID == nil || *c.PinnedParticipantUserID != participantID {
		t.Fatalf("pinned_participant_user_id = %v, want %s", c.PinnedParticipantUserID, participantID)
	}
	if got := countShareEvents(t, pub.captures, event.TypeCallPinnedChanged); got != 1 {
		t.Fatalf("call.pinned.changed events = %d, want 1", got)
	}
}

func TestPinnedParticipantClearsWhenTargetLeaves(t *testing.T) {
	ctx := context.Background()
	svc, calls, _, pub, workspaceID, callID, _, participantID := shareTestHarness(t, entity.CallStatusActive)
	calls.calls[callID].PinnedParticipantUserID = &participantID

	if _, err := svc.LeaveCall(ctx, workspaceID, callID, participantID); err != nil {
		t.Fatalf("LeaveCall error: %v", err)
	}
	if calls.calls[callID].PinnedParticipantUserID != nil {
		t.Fatalf("pinned_participant_user_id = %v, want nil", calls.calls[callID].PinnedParticipantUserID)
	}
	if got := countShareEvents(t, pub.captures, event.TypeCallPinnedChanged); got != 1 {
		t.Fatalf("call.pinned.changed events = %d, want 1", got)
	}
}

func TestPinnedParticipantClearsWhenTargetRemoved(t *testing.T) {
	ctx := context.Background()
	svc, calls, _, pub, workspaceID, callID, hostID, participantID := shareTestHarness(t, entity.CallStatusActive)
	calls.calls[callID].PinnedParticipantUserID = &participantID

	if err := svc.RemoveParticipant(ctx, workspaceID, callID, hostID, participantID); err != nil {
		t.Fatalf("RemoveParticipant error: %v", err)
	}
	if calls.calls[callID].PinnedParticipantUserID != nil {
		t.Fatalf("pinned_participant_user_id = %v, want nil", calls.calls[callID].PinnedParticipantUserID)
	}
	if got := countShareEvents(t, pub.captures, event.TypeCallPinnedChanged); got != 1 {
		t.Fatalf("call.pinned.changed events = %d, want 1", got)
	}
}

func TestSetPinnedParticipantRejectsNonHost(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _, workspaceID, callID, _, participantID := shareTestHarness(t, entity.CallStatusActive)
	if err := svc.SetPinnedParticipant(ctx, workspaceID, callID, participantID, &participantID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("non-host SetPinnedParticipant error = %v, want FORBIDDEN", err)
	}
}

func TestSetPinnedParticipantRejectsNonParticipantTarget(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _, workspaceID, callID, hostID, _ := shareTestHarness(t, entity.CallStatusActive)
	stranger := uuid.New()
	if err := svc.SetPinnedParticipant(ctx, workspaceID, callID, hostID, &stranger); !hasCode(err, cerrors.CodeInvalidInput) {
		t.Fatalf("non-participant pinned target error = %v, want INVALID_INPUT", err)
	}
}

func TestSetFeaturedShareRejectsNonHost(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _, workspaceID, callID, _, participantID := shareTestHarness(t, entity.CallStatusActive)
	if err := svc.SetFeaturedShare(ctx, workspaceID, callID, participantID, &participantID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("non-host SetFeaturedShare error = %v, want FORBIDDEN", err)
	}
}

func TestSetFeaturedShareRejectsNonParticipantTarget(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _, workspaceID, callID, hostID, _ := shareTestHarness(t, entity.CallStatusActive)
	stranger := uuid.New()
	if err := svc.SetFeaturedShare(ctx, workspaceID, callID, hostID, &stranger); !hasCode(err, cerrors.CodeInvalidInput) {
		t.Fatalf("non-participant target error = %v, want INVALID_INPUT", err)
	}
}

func TestUpdateMediaRejectsUngrantedScreenShare(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _, workspaceID, callID, _, participantID := shareTestHarness(t, entity.CallStatusActive)
	// Enable screen sharing at the call level so the per-participant gate is what trips.
	svc.calls.(*fakeCallRepo).calls[callID].Settings.ScreenSharing = true

	on := true
	err := svc.UpdateMedia(ctx, workspaceID, callID, participantID, nil, nil, &on)
	if !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("ungranted screen-share UpdateMedia error = %v, want FORBIDDEN", err)
	}
	appErr, _ := cerrors.AsAppError(err)
	if appErr == nil || appErr.Message != "screen_share_not_granted" {
		t.Fatalf("error message = %v, want screen_share_not_granted", err)
	}
}

func TestUpdateMediaRejectsMemberUnmuteWhenPolicyDenies(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _, workspaceID, callID, _, participantID := shareTestHarness(t, entity.CallStatusActive)
	denied := false
	svc.calls.(*fakeCallRepo).calls[callID].Settings.MembersCanUnmuteMic = &denied

	unmute := false // audio_muted=false → attempting to unmute
	if err := svc.UpdateMedia(ctx, workspaceID, callID, participantID, &unmute, nil, nil); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("member unmute under deny policy error = %v, want FORBIDDEN", err)
	}
}

func TestUpdateMediaRejectsMemberCameraWhenPolicyDenies(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _, workspaceID, callID, _, participantID := shareTestHarness(t, entity.CallStatusActive)
	denied := false
	svc.calls.(*fakeCallRepo).calls[callID].Settings.MembersCanEnableCamera = &denied

	cameraOn := false // video_muted=false → attempting to enable the camera
	if err := svc.UpdateMedia(ctx, workspaceID, callID, participantID, nil, &cameraOn, nil); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("member camera under deny policy error = %v, want FORBIDDEN", err)
	}
}

func TestUpdateMediaAllowsHostUnmuteWhenMemberPolicyDenies(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _, workspaceID, callID, hostID, _ := shareTestHarness(t, entity.CallStatusActive)
	denied := false
	svc.calls.(*fakeCallRepo).calls[callID].Settings.MembersCanUnmuteMic = &denied
	svc.calls.(*fakeCallRepo).calls[callID].Settings.MembersCanEnableCamera = &denied

	unmute := false
	cameraOn := false
	if err := svc.UpdateMedia(ctx, workspaceID, callID, hostID, &unmute, &cameraOn, nil); err != nil {
		t.Fatalf("host should be exempt from the member-permission policy, got %v", err)
	}
}

func TestHandleLiveKitTrackUnpublishClearsFeaturedShare(t *testing.T) {
	ctx := context.Background()
	svc, calls, _, pub, _, callID, _, participantID := shareTestHarness(t, entity.CallStatusActive)

	// Featured user is currently sharing.
	calls.calls[callID].FeaturedShareUserID = &participantID
	calls.participants[[2]uuid.UUID{callID, participantID}].ScreenSharing = true

	track := &livekitpb.TrackInfo{Source: livekitpb.TrackSource_SCREEN_SHARE}
	p := &livekitpb.ParticipantInfo{Identity: participantID.String()}
	if err := svc.handleLiveKitTrackChanged(ctx, callID, p, track, false); err != nil {
		t.Fatalf("handleLiveKitTrackChanged error: %v", err)
	}
	if calls.calls[callID].FeaturedShareUserID != nil {
		t.Fatalf("featured_share_user_id not cleared: %v", calls.calls[callID].FeaturedShareUserID)
	}
	if got := countShareEvents(t, pub.captures, event.TypeCallFeaturedShareUpdated); got != 1 {
		t.Fatalf("featured_share.updated events = %d, want 1", got)
	}
}
