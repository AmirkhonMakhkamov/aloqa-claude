package call

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	livekitpb "github.com/livekit/protocol/livekit"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
)

// micTrack builds a LiveKit ParticipantInfo with a single live microphone track.
func participantWithTracks(userID uuid.UUID, sources ...livekitpb.TrackSource) *livekitpb.ParticipantInfo {
	tracks := make([]*livekitpb.TrackInfo, 0, len(sources))
	for i, src := range sources {
		tracks = append(tracks, &livekitpb.TrackInfo{Sid: uuid.New().String() + string(rune('a'+i)), Source: src})
	}
	return &livekitpb.ParticipantInfo{Identity: userID.String(), Tracks: tracks}
}

func hasUpdateFor(updated []updatedLiveKitParticipant, userID uuid.UUID) bool {
	for _, u := range updated {
		if u.identity == userID.String() {
			return true
		}
	}
	return false
}

func mutedFor(muted []mutedLiveKitTrack, userID uuid.UUID) int {
	n := 0
	for _, m := range muted {
		if m.identity == userID.String() {
			n++
		}
	}
	return n
}

// A restrictive mic flip revokes the member's publish source and force-stops the
// live mic track, persists, and broadcasts — and never touches the host.
func TestUpdateCallSettingsRestrictiveMicFlipEnforcesThenPersists(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID, hostID, memberID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	call := breakoutCall(callID, workspaceID, hostID)
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: call},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}:   connectedParticipant(callID, hostID, entity.CallRoleHost),
			{callID, memberID}: connectedParticipant(callID, memberID, entity.CallRoleParticipant),
		},
	}
	rooms := &fakeLiveKitRoomClient{
		participantsByCall: map[uuid.UUID][]*livekitpb.ParticipantInfo{
			callID: {participantWithTracks(memberID, livekitpb.TrackSource_MICROPHONE)},
		},
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)
	svc.SetLiveKitRoomClient(rooms)

	_, err := svc.UpdateCallSettings(ctx, callID, hostID, CallSettingsPatch{MembersCanUnmuteMic: boolPtr(false)})
	if err != nil {
		t.Fatalf("UpdateCallSettings returned error: %v", err)
	}
	if !hasUpdateFor(rooms.updatedParticipants, memberID) {
		t.Fatalf("member permission was not re-applied: %+v", rooms.updatedParticipants)
	}
	if hasUpdateFor(rooms.updatedParticipants, hostID) {
		t.Fatalf("host should not be enforced")
	}
	if got := mutedFor(rooms.mutedTracks, memberID); got != 1 {
		t.Fatalf("member muted tracks = %d, want 1", got)
	}
	if calls.settingsUpdates != 1 {
		t.Fatalf("settingsUpdates = %d, want 1", calls.settingsUpdates)
	}
	if call.Settings.ResolvedMembersCanUnmuteMic() {
		t.Fatalf("persisted members_can_unmute_mic = true, want false")
	}
	if !pub.called {
		t.Fatalf("expected call.settings.changed broadcast")
	}
}

// If the live-track stop fails on a restrictive flip, the PATCH aborts: the
// stored setting is unchanged and no broadcast is emitted.
func TestUpdateCallSettingsRestrictiveFlipEnforcementFailureLeavesSettingsUnchanged(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID, hostID, memberID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	call := breakoutCall(callID, workspaceID, hostID)
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: call},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}:   connectedParticipant(callID, hostID, entity.CallRoleHost),
			{callID, memberID}: connectedParticipant(callID, memberID, entity.CallRoleParticipant),
		},
	}
	rooms := &fakeLiveKitRoomClient{
		participantsByCall: map[uuid.UUID][]*livekitpb.ParticipantInfo{
			callID: {participantWithTracks(memberID, livekitpb.TrackSource_MICROPHONE)},
		},
		mutePublishedTrackErr: errors.New("livekit mute failed"),
	}
	pub := &capturingPublisher{}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, pub, nil, mediaTestConfig(), nil, nil)
	svc.SetLiveKitRoomClient(rooms)

	_, err := svc.UpdateCallSettings(ctx, callID, hostID, CallSettingsPatch{MembersCanUnmuteMic: boolPtr(false)})
	if err == nil {
		t.Fatalf("expected enforcement error")
	}
	if calls.settingsUpdates != 0 {
		t.Fatalf("settingsUpdates = %d, want 0 (no persist on enforcement failure)", calls.settingsUpdates)
	}
	if !call.Settings.ResolvedMembersCanUnmuteMic() {
		t.Fatalf("stored members_can_unmute_mic flipped to false despite enforcement failure")
	}
	if pub.called {
		t.Fatalf("settings.changed broadcast emitted despite enforcement failure")
	}
}

// A permissive flip (false→true) re-applies the widened permission to connected
// members at the media plane (re-granting MICROPHONE) without a rejoin.
func TestUpdateCallSettingsPermissiveMicFlipReappliesGrant(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID, hostID, memberID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	call := breakoutCall(callID, workspaceID, hostID)
	denied := false
	call.Settings.MembersCanUnmuteMic = &denied // start restricted
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: call},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}:   connectedParticipant(callID, hostID, entity.CallRoleHost),
			{callID, memberID}: connectedParticipant(callID, memberID, entity.CallRoleParticipant),
		},
	}
	rooms := &fakeLiveKitRoomClient{
		participantsByCall: map[uuid.UUID][]*livekitpb.ParticipantInfo{callID: {participantWithTracks(memberID)}},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, &capturingPublisher{}, nil, mediaTestConfig(), nil, nil)
	svc.SetLiveKitRoomClient(rooms)

	if _, err := svc.UpdateCallSettings(ctx, callID, hostID, CallSettingsPatch{MembersCanUnmuteMic: boolPtr(true)}); err != nil {
		t.Fatalf("UpdateCallSettings returned error: %v", err)
	}
	if !hasUpdateFor(rooms.updatedParticipants, memberID) {
		t.Fatalf("permissive flip did not re-apply the member permission (reconcile dead path)")
	}
	if !call.Settings.ResolvedMembersCanUnmuteMic() {
		t.Fatalf("persisted members_can_unmute_mic = false, want true")
	}
}

// A connected viewer is not enforced (they publish nothing; no redundant RPC).
func TestUpdateCallSettingsRestrictiveFlipSkipsViewers(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID, hostID, viewerID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	call := breakoutCall(callID, workspaceID, hostID)
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: call},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}:   connectedParticipant(callID, hostID, entity.CallRoleHost),
			{callID, viewerID}: connectedParticipant(callID, viewerID, entity.CallRoleViewer),
		},
	}
	rooms := &fakeLiveKitRoomClient{
		participantsByCall: map[uuid.UUID][]*livekitpb.ParticipantInfo{callID: {participantWithTracks(viewerID)}},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, &capturingPublisher{}, nil, mediaTestConfig(), nil, nil)
	svc.SetLiveKitRoomClient(rooms)

	if _, err := svc.UpdateCallSettings(ctx, callID, hostID, CallSettingsPatch{MembersCanUnmuteMic: boolPtr(false)}); err != nil {
		t.Fatalf("UpdateCallSettings returned error: %v", err)
	}
	if hasUpdateFor(rooms.updatedParticipants, viewerID) {
		t.Fatalf("viewer should not be enforced")
	}
}

// A member already inside a breakout room is skipped during enforcement (they
// re-mint via the resolver on return to main).
func TestUpdateCallSettingsRestrictiveFlipSkipsBreakoutMembers(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID, hostID, memberID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	call := breakoutCall(callID, workspaceID, hostID)
	breakoutMember := connectedParticipant(callID, memberID, entity.CallRoleParticipant)
	roomID := uuid.New()
	breakoutMember.BreakoutRoomID = &roomID
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: call},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, hostID}:   connectedParticipant(callID, hostID, entity.CallRoleHost),
			{callID, memberID}: breakoutMember,
		},
	}
	rooms := &fakeLiveKitRoomClient{
		participantsByCall: map[uuid.UUID][]*livekitpb.ParticipantInfo{
			callID: {participantWithTracks(memberID, livekitpb.TrackSource_MICROPHONE)},
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, &capturingPublisher{}, nil, mediaTestConfig(), nil, nil)
	svc.SetLiveKitRoomClient(rooms)

	if _, err := svc.UpdateCallSettings(ctx, callID, hostID, CallSettingsPatch{MembersCanUnmuteMic: boolPtr(false)}); err != nil {
		t.Fatalf("UpdateCallSettings returned error: %v", err)
	}
	if hasUpdateFor(rooms.updatedParticipants, memberID) {
		t.Fatalf("breakout member should be skipped during enforcement")
	}
	if mutedFor(rooms.mutedTracks, memberID) != 0 {
		t.Fatalf("breakout member's track should not be muted")
	}
}

// A co-host may patch the policy and all four fields are applied.
func TestUpdateCallSettingsCoHostAppliesAllPolicyFields(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID, hostID, coHostID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	call := breakoutCall(callID, workspaceID, hostID)
	call.Settings.ScreenSharing = true
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: call},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, coHostID}: connectedParticipant(callID, coHostID, entity.CallRoleCoHost),
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, &capturingPublisher{}, nil, mediaTestConfig(), nil, nil)

	updated, err := svc.UpdateCallSettings(ctx, callID, coHostID, CallSettingsPatch{
		ScreenSharing:          boolPtr(false),
		Chat:                   boolPtr(false),
		MembersCanUnmuteMic:    boolPtr(false),
		MembersCanEnableCamera: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("co-host UpdateCallSettings returned error: %v", err)
	}
	if updated.Settings.ScreenSharing || updated.Settings.Chat ||
		updated.Settings.ResolvedMembersCanUnmuteMic() || updated.Settings.ResolvedMembersCanEnableCamera() {
		t.Fatalf("policy fields not all applied: %+v", updated.Settings)
	}
}

// A non-host / non-co-host actor is rejected.
func TestUpdateCallSettingsPolicyRejectsMember(t *testing.T) {
	ctx := context.Background()
	workspaceID, callID, hostID, memberID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	calls := &fakeCallRepo{
		calls: map[uuid.UUID]*entity.Call{callID: breakoutCall(callID, workspaceID, hostID)},
		participants: map[[2]uuid.UUID]*entity.CallParticipant{
			{callID, memberID}: connectedParticipant(callID, memberID, entity.CallRoleParticipant),
		},
	}
	svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), nil, nil)

	_, err := svc.UpdateCallSettings(ctx, callID, memberID, CallSettingsPatch{MembersCanUnmuteMic: boolPtr(false)})
	if !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("member policy patch error = %v, want FORBIDDEN", err)
	}
	if calls.settingsUpdates != 0 {
		t.Fatalf("settingsUpdates = %d, want 0", calls.settingsUpdates)
	}
}
