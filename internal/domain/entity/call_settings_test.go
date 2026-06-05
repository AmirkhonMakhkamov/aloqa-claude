package entity

import (
	"encoding/json"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestCallSettingsResolvedMemberPolicies(t *testing.T) {
	tests := []struct {
		name     string
		settings CallSettings
		wantMic  bool
		wantCam  bool
	}{
		{name: "legacy nil resolves permissive", settings: CallSettings{}, wantMic: true, wantCam: true},
		{
			name:     "explicit false preserved",
			settings: CallSettings{MembersCanUnmuteMic: boolPtr(false), MembersCanEnableCamera: boolPtr(false)},
			wantMic:  false,
			wantCam:  false,
		},
		{
			name:     "explicit true preserved",
			settings: CallSettings{MembersCanUnmuteMic: boolPtr(true), MembersCanEnableCamera: boolPtr(true)},
			wantMic:  true,
			wantCam:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.settings.ResolvedMembersCanUnmuteMic(); got != tc.wantMic {
				t.Fatalf("ResolvedMembersCanUnmuteMic() = %v, want %v", got, tc.wantMic)
			}
			if got := tc.settings.ResolvedMembersCanEnableCamera(); got != tc.wantCam {
				t.Fatalf("ResolvedMembersCanEnableCamera() = %v, want %v", got, tc.wantCam)
			}
		})
	}
}

// A legacy row (nil member-policy pointers) must serialise the resolved booleans
// (true), never null, so the wire/OpenAPI non-null boolean contract holds.
func TestCallSettingsMarshalJSONResolvesLegacyNil(t *testing.T) {
	raw, err := json.Marshal(CallSettings{}) // all-zero, nil member pointers
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	if v, ok := wire["members_can_unmute_mic"]; !ok || v != true {
		t.Fatalf("members_can_unmute_mic = %v (ok=%v), want true", v, ok)
	}
	if v, ok := wire["members_can_enable_camera"]; !ok || v != true {
		t.Fatalf("members_can_enable_camera = %v (ok=%v), want true", v, ok)
	}
}

func TestCallSettingsMarshalJSONExplicitFalse(t *testing.T) {
	raw, err := json.Marshal(CallSettings{
		MembersCanUnmuteMic:    boolPtr(false),
		MembersCanEnableCamera: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	if wire["members_can_unmute_mic"] != false {
		t.Fatalf("members_can_unmute_mic = %v, want false", wire["members_can_unmute_mic"])
	}
	if wire["members_can_enable_camera"] != false {
		t.Fatalf("members_can_enable_camera = %v, want false", wire["members_can_enable_camera"])
	}
}

// The custom marshaler must not drop any sibling CallSettings field.
func TestCallSettingsMarshalJSONPreservesSiblingFields(t *testing.T) {
	raw, err := json.Marshal(CallSettings{
		WaitingRoom:     true,
		MuteOnJoin:      true,
		Recording:       true,
		ScreenSharing:   true,
		Chat:            true,
		BreakoutRooms:   true,
		MaxParticipants: 42,
		E2EE:            true,
		Watermark:       true,
		EntryMode:       EntryModeOpen,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	for _, key := range []string{
		"waiting_room", "mute_on_join", "recording", "screen_sharing", "chat",
		"breakout_rooms", "max_participants", "e2ee", "watermark", "entry_mode",
		"members_can_unmute_mic", "members_can_enable_camera",
	} {
		if _, ok := wire[key]; !ok {
			t.Fatalf("wire missing sibling field %q: %v", key, wire)
		}
	}
	if wire["max_participants"].(float64) != 42 {
		t.Fatalf("max_participants = %v, want 42", wire["max_participants"])
	}
	if wire["entry_mode"] != string(EntryModeOpen) {
		t.Fatalf("entry_mode = %v, want %v", wire["entry_mode"], EntryModeOpen)
	}
}

// Round-trip: a marshaled legacy row, when unmarshaled back, has non-nil
// resolved pointers (the nil-as-unset backfills to the permissive default).
func TestCallSettingsMarshalUnmarshalRoundTrip(t *testing.T) {
	raw, err := json.Marshal(CallSettings{MembersCanUnmuteMic: boolPtr(false)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back CallSettings
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ResolvedMembersCanUnmuteMic() != false {
		t.Fatalf("round-trip mic = %v, want false", back.ResolvedMembersCanUnmuteMic())
	}
	if back.ResolvedMembersCanEnableCamera() != true {
		t.Fatalf("round-trip cam = %v, want true (nil default)", back.ResolvedMembersCanEnableCamera())
	}
}
