package sfu

import (
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestRoomPlanAndApplyAdaptiveDecision(t *testing.T) {
	server, err := NewSFU(Config{})
	if err != nil {
		t.Fatalf("NewSFU returned error: %v", err)
	}
	defer server.Close()

	room := newRoom("room-1", RoomOptions{
		Adaptive: AdaptiveOptions{
			GoodSamplesForUpgrade:   1,
			PoorSamplesForDowngrade: 1,
			MinUpswitchInterval:     0,
			MinDownswitchInterval:   0,
			EWMAAlpha:               1,
		},
	})
	pc, err := server.NewPeerConnection()
	if err != nil {
		t.Fatalf("NewPeerConnection returned error: %v", err)
	}
	defer pc.Close()
	if _, err := room.AddViewer("viewer-1", pc); err != nil {
		t.Fatalf("AddViewer returned error: %v", err)
	}

	highTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "track-high", "screen-share")
	if err != nil {
		t.Fatalf("NewTrackLocalStaticRTP high returned error: %v", err)
	}
	lowTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "track-low", "screen-share")
	if err != nil {
		t.Fatalf("NewTrackLocalStaticRTP low returned error: %v", err)
	}

	st := NewSimulcastTrack("screen-share", "presenter-1")
	st.layers[QualityHigh] = &simulcastLayer{localTrack: highTrack, done: make(chan struct{})}
	st.layers[QualityLow] = &simulcastLayer{localTrack: lowTrack, done: make(chan struct{})}
	st.subscribers["viewer-1"] = QualityHigh
	room.simulcastTracks["screen-share"] = st

	targets := room.SubscriberTargets("viewer-1")
	if len(targets) != 1 {
		t.Fatalf("SubscriberTargets len = %d, want 1", len(targets))
	}
	if !targets[0].ScreenShare {
		t.Fatalf("ScreenShare = false, want true for screen-share stream")
	}

	decision, err := room.PlanSubscriberAdaptation(NetworkSample{
		UserID:               "viewer-1",
		StreamID:             "screen-share",
		AvailableBitrateKbps: 100,
		PacketLossPct:        18,
		RoundTripTimeMs:      850,
		JitterMs:             140,
		AudioPacketLossPct:   10,
		AudioJitterMs:        120,
		NACKCountDelta:       8,
		PLICountDelta:        2,
		Timestamp:            time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("PlanSubscriberAdaptation returned error: %v", err)
	}
	if decision.TargetQuality != QualityLow {
		t.Fatalf("TargetQuality = %q, want %q", decision.TargetQuality, QualityLow)
	}
	if !decision.Changed {
		t.Fatalf("Changed = false, want true")
	}
	if err := room.ApplyAdaptiveDecision(decision); err != nil {
		t.Fatalf("ApplyAdaptiveDecision returned error: %v", err)
	}
	if current, ok := st.SubscriberLayer("viewer-1"); !ok || current != QualityLow {
		t.Fatalf("SubscriberLayer = %q, want %q", current, QualityLow)
	}
}
