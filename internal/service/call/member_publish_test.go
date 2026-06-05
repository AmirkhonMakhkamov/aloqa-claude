package call

import (
	"testing"

	"aloqa/internal/domain/entity"
)

func mp(b bool) *bool { return &b }

// TestResolveMemberPublish covers the meeting-level member-permission policy
// (ALK-812): host/co-host publish everything, viewers nothing, and every other
// role ("member") is gated per-source by the policy, with an empty allowed set
// clearing CanPublish (LiveKit treats an empty CanPublishSources as no restriction).
func TestResolveMemberPublish(t *testing.T) {
	permissive := entity.CallSettings{ScreenSharing: true} // mic/cam nil → resolve true
	for _, tc := range []struct {
		name        string
		role        entity.CallRole
		settings    entity.CallSettings
		hasGrant    bool
		wantMic     bool
		wantCam     bool
		wantShare   bool
		wantPublish bool
	}{
		{name: "host: all", role: entity.CallRoleHost, settings: entity.CallSettings{}, wantMic: true, wantCam: true, wantShare: true, wantPublish: true},
		{name: "co_host: all", role: entity.CallRoleCoHost, settings: entity.CallSettings{}, wantMic: true, wantCam: true, wantShare: true, wantPublish: true},
		{name: "viewer: none", role: entity.CallRoleViewer, settings: permissive, hasGrant: true, wantMic: false, wantCam: false, wantShare: false, wantPublish: false},
		{name: "member permissive, no grant: mic+cam, no screen", role: entity.CallRoleParticipant, settings: permissive, hasGrant: false, wantMic: true, wantCam: true, wantShare: false, wantPublish: true},
		{name: "member permissive, with grant: mic+cam+screen", role: entity.CallRoleParticipant, settings: permissive, hasGrant: true, wantMic: true, wantCam: true, wantShare: true, wantPublish: true},
		{name: "presenter permissive, with grant: mic+cam+screen", role: entity.CallRolePresenter, settings: permissive, hasGrant: true, wantMic: true, wantCam: true, wantShare: true, wantPublish: true},
		{name: "member mic denied: cam only", role: entity.CallRoleParticipant, settings: entity.CallSettings{ScreenSharing: true, MembersCanUnmuteMic: mp(false)}, hasGrant: false, wantMic: false, wantCam: true, wantShare: false, wantPublish: true},
		{name: "member cam denied: mic only", role: entity.CallRoleParticipant, settings: entity.CallSettings{ScreenSharing: true, MembersCanEnableCamera: mp(false)}, hasGrant: false, wantMic: true, wantCam: false, wantShare: false, wantPublish: true},
		{name: "member screen off by policy even with grant", role: entity.CallRoleParticipant, settings: entity.CallSettings{ScreenSharing: false}, hasGrant: true, wantMic: true, wantCam: true, wantShare: false, wantPublish: true},
		{
			name: "member all denied: empty ⇒ CanPublish false",
			role: entity.CallRoleParticipant,
			// mic+cam denied, screen policy on but no grant ⇒ no source at all.
			settings:    entity.CallSettings{ScreenSharing: true, MembersCanUnmuteMic: mp(false), MembersCanEnableCamera: mp(false)},
			hasGrant:    false,
			wantMic:     false,
			wantCam:     false,
			wantShare:   false,
			wantPublish: false,
		},
		{
			name:        "member mic+cam denied, screen off: empty ⇒ CanPublish false",
			role:        entity.CallRoleParticipant,
			settings:    entity.CallSettings{ScreenSharing: false, MembersCanUnmuteMic: mp(false), MembersCanEnableCamera: mp(false)},
			hasGrant:    true,
			wantMic:     false,
			wantCam:     false,
			wantShare:   false,
			wantPublish: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			canMic, canCam, canShare, canPublish := resolveMemberPublish(tc.role, tc.settings, tc.hasGrant)
			if canMic != tc.wantMic || canCam != tc.wantCam || canShare != tc.wantShare || canPublish != tc.wantPublish {
				t.Fatalf("resolveMemberPublish = (mic=%v cam=%v share=%v publish=%v), want (mic=%v cam=%v share=%v publish=%v)",
					canMic, canCam, canShare, canPublish, tc.wantMic, tc.wantCam, tc.wantShare, tc.wantPublish)
			}
		})
	}
}

// TestPublishSourcesEmptyWhenAllDenied confirms the source list is empty so the
// caller knows to clear CanPublish.
func TestPublishSourcesEmptyWhenAllDenied(t *testing.T) {
	if got := publishSources(false, false, false); len(got) != 0 {
		t.Fatalf("publishSources(false,false,false) = %v, want empty", got)
	}
	if got := publishSources(true, true, true); len(got) != 4 {
		t.Fatalf("publishSources(true,true,true) = %v, want 4 sources", got)
	}
}
