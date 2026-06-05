package call

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/livekit/protocol/auth"
	livekitpb "github.com/livekit/protocol/livekit"

	"aloqa/internal/domain/entity"
)

// TestIssueLiveKitJoinInfoGatesScreenShareSources verifies the join-token's
// CanPublishSources is derived from the participant's role + per-participant
// screen-share grant: hosts/co-hosts and granted participants get the full
// screen + screen-audio source set; non-granted publishers get camera + mic
// only; viewers cannot publish at all. (ALK-697)
func TestIssueLiveKitJoinInfoGatesScreenShareSources(t *testing.T) {
	const apiSecret = "aloqa-livekit-test-secret"

	hasSource := func(sources []livekitpb.TrackSource, want livekitpb.TrackSource) bool {
		for _, s := range sources {
			if s == want {
				return true
			}
		}
		return false
	}

	for _, tc := range []struct {
		name           string
		role           entity.CallRole
		canScreenShare bool
		wantPublish    bool
		wantScreen     bool
	}{
		{name: "host gets screen sources", role: entity.CallRoleHost, wantPublish: true, wantScreen: true},
		{name: "co-host gets screen sources", role: entity.CallRoleCoHost, wantPublish: true, wantScreen: true},
		{name: "granted participant gets screen sources", role: entity.CallRoleParticipant, canScreenShare: true, wantPublish: true, wantScreen: true},
		{name: "non-granted participant has no screen source", role: entity.CallRoleParticipant, wantPublish: true, wantScreen: false},
		{name: "non-granted presenter has no screen source", role: entity.CallRolePresenter, wantPublish: true, wantScreen: false},
		{name: "viewer cannot publish", role: entity.CallRoleViewer, wantPublish: false, wantScreen: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			workspaceID := uuid.New()
			callID := uuid.New()
			userID := uuid.New()

			calls := &fakeCallRepo{
				calls: map[uuid.UUID]*entity.Call{
					// ScreenSharing=true: the meeting allows screen-share at the meeting
					// level (ALK-812); a member's screen source is the AND of this toggle
					// and their per-participant ALK-697 grant, while host/co-host always
					// share. (Consistent with the existing UpdateMedia ScreenSharing gate.)
					callID: {ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeMeeting, Status: entity.CallStatusActive, Settings: entity.CallSettings{ScreenSharing: true}},
				},
				participants: map[[2]uuid.UUID]*entity.CallParticipant{
					{callID, userID}: {
						ID:             uuid.New(),
						CallID:         callID,
						UserID:         userID,
						Role:           tc.role,
						Status:         entity.ParticipantStatusConnected,
						CanScreenShare: tc.canScreenShare,
					},
				},
			}
			svc := NewService(calls, &fakeBreakoutRepo{}, &fakeChannelRepo{}, &fakeWorkspaceRepo{}, noopPublisher{}, nil, mediaTestConfig(), nil, nil)
			svc.SetLiveKit(LiveKitSettings{URL: "ws://livekit.test", APIKey: "APItest", APISecret: apiSecret, TokenTTL: time.Hour})

			info, err := svc.IssueLiveKitJoinInfo(ctx, calls.calls[callID], userID, "")
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
			if grants.Video.CanPublish == nil || *grants.Video.CanPublish != tc.wantPublish {
				t.Fatalf("CanPublish = %v, want %v", grants.Video.CanPublish, tc.wantPublish)
			}

			sources := grants.Video.GetCanPublishSources()
			if !tc.wantPublish {
				// Viewers never publish; no source set is granted.
				if len(sources) != 0 {
					t.Fatalf("viewer CanPublishSources = %v, want empty", sources)
				}
				return
			}

			// Every publisher always carries camera + microphone.
			if !hasSource(sources, livekitpb.TrackSource_CAMERA) || !hasSource(sources, livekitpb.TrackSource_MICROPHONE) {
				t.Fatalf("CanPublishSources = %v, want camera + microphone", sources)
			}
			gotScreen := hasSource(sources, livekitpb.TrackSource_SCREEN_SHARE)
			gotScreenAudio := hasSource(sources, livekitpb.TrackSource_SCREEN_SHARE_AUDIO)
			if gotScreen != tc.wantScreen || gotScreenAudio != tc.wantScreen {
				t.Fatalf("screen sources = (%v,%v), want %v", gotScreen, gotScreenAudio, tc.wantScreen)
			}
			if tc.wantScreen && len(sources) != 4 {
				t.Fatalf("sharer CanPublishSources = %v, want 4 sources", sources)
			}
			if !tc.wantScreen && len(sources) != 2 {
				t.Fatalf("non-sharer CanPublishSources = %v, want 2 sources", sources)
			}
		})
	}
}
