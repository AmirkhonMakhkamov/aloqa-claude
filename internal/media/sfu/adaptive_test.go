package sfu

import (
	"testing"
	"time"
)

func TestAdaptiveControllerAudioPriorityForcesLowAndRecommendsVideoSuspend(t *testing.T) {
	controller := NewAdaptiveController(AdaptiveOptions{})
	now := time.Now().UTC()

	decision := controller.Decide(NetworkSample{
		UserID:               "user-1",
		StreamID:             "camera",
		AvailableBitrateKbps: 80,
		PacketLossPct:        18,
		RoundTripTimeMs:      850,
		JitterMs:             140,
		AudioPacketLossPct:   12,
		AudioJitterMs:        130,
		NACKCountDelta:       25,
		PLICountDelta:        4,
		Timestamp:            now,
	}, QualityHigh, []QualityLayer{QualityHigh, QualityMedium, QualityLow})

	if decision.TargetQuality != QualityLow {
		t.Fatalf("TargetQuality = %q, want %q", decision.TargetQuality, QualityLow)
	}
	if !decision.Changed {
		t.Fatalf("Changed = false, want true")
	}
	if !decision.AudioPriority {
		t.Fatalf("AudioPriority = false, want true")
	}
	if !decision.VideoSuspended {
		t.Fatalf("VideoSuspended = false, want true")
	}
	if decision.SyncMode != "audio_clock_master" {
		t.Fatalf("SyncMode = %q, want audio_clock_master", decision.SyncMode)
	}
	if decision.VideoDegradeMode != "suspend_video_until_audio_recovers" {
		t.Fatalf("VideoDegradeMode = %q, want suspend_video_until_audio_recovers", decision.VideoDegradeMode)
	}
	if decision.MaxVideoFPS > 12 {
		t.Fatalf("MaxVideoFPS = %d, want <= 12", decision.MaxVideoFPS)
	}
	if decision.TargetVideoBufferMs != 0 {
		t.Fatalf("TargetVideoBufferMs = %d, want 0 while video suspend is recommended", decision.TargetVideoBufferMs)
	}
}

func TestAdaptiveControllerLegacyDeviceCapBypassesDownswitchDelay(t *testing.T) {
	controller := NewAdaptiveController(AdaptiveOptions{})
	now := time.Now().UTC()

	decision := controller.Decide(NetworkSample{
		UserID:               "user-1",
		StreamID:             "camera",
		AvailableBitrateKbps: 6000,
		PacketLossPct:        0.1,
		RoundTripTimeMs:      35,
		JitterMs:             3,
		FramesPerSecond:      30,
		DeviceClass:          DeviceClassLegacy,
		Timestamp:            now,
	}, QualityHigh, []QualityLayer{QualityHigh, QualityMedium, QualityLow})

	if decision.TargetQuality != QualityLow {
		t.Fatalf("TargetQuality = %q, want %q", decision.TargetQuality, QualityLow)
	}
	if !decision.Changed {
		t.Fatalf("Changed = false, want true")
	}
	if decision.Hold {
		t.Fatalf("Hold = true, want immediate downgrade for legacy device")
	}
	if decision.VideoDegradeMode != "reduce_resolution_first" {
		t.Fatalf("VideoDegradeMode = %q, want reduce_resolution_first", decision.VideoDegradeMode)
	}
}

func TestAdaptiveControllerHysteresisBlocksFastUpgrade(t *testing.T) {
	controller := NewAdaptiveController(AdaptiveOptions{
		MinUpswitchInterval:   10 * time.Second,
		GoodSamplesForUpgrade: 2,
		EWMAAlpha:             1,
	})
	now := time.Now().UTC()

	first := controller.Decide(NetworkSample{
		UserID:               "user-1",
		StreamID:             "camera",
		AvailableBitrateKbps: 90,
		PacketLossPct:        16,
		RoundTripTimeMs:      720,
		JitterMs:             130,
		Timestamp:            now,
	}, QualityHigh, []QualityLayer{QualityHigh, QualityMedium, QualityLow})
	if first.TargetQuality != QualityLow {
		t.Fatalf("first TargetQuality = %q, want %q", first.TargetQuality, QualityLow)
	}

	second := controller.Decide(NetworkSample{
		UserID:               "user-1",
		StreamID:             "camera",
		AvailableBitrateKbps: 5000,
		PacketLossPct:        0.1,
		RoundTripTimeMs:      35,
		JitterMs:             3,
		FramesPerSecond:      30,
		Timestamp:            now.Add(2 * time.Second),
	}, QualityLow, []QualityLayer{QualityHigh, QualityMedium, QualityLow})
	if second.TargetQuality != QualityLow {
		t.Fatalf("second TargetQuality = %q, want %q while upgrade hysteresis holds", second.TargetQuality, QualityLow)
	}
	if !second.Hold {
		t.Fatalf("second Hold = false, want true")
	}
	if second.Changed {
		t.Fatalf("second Changed = true, want false")
	}
}

func TestAdaptiveControllerForgetsUserAndStreamState(t *testing.T) {
	controller := NewAdaptiveController(AdaptiveOptions{})
	now := time.Now().UTC()

	controller.Decide(NetworkSample{
		UserID:               "user-1",
		StreamID:             "camera",
		AvailableBitrateKbps: 90,
		Timestamp:            now,
	}, QualityHigh, []QualityLayer{QualityHigh, QualityMedium, QualityLow})
	controller.Decide(NetworkSample{
		UserID:               "user-2",
		StreamID:             "camera",
		AvailableBitrateKbps: 90,
		Timestamp:            now,
	}, QualityHigh, []QualityLayer{QualityHigh, QualityMedium, QualityLow})

	controller.ForgetUser("user-1")
	if _, ok := controller.state["user-1:camera"]; ok {
		t.Fatalf("user state was not removed")
	}
	if _, ok := controller.state["user-2:camera"]; !ok {
		t.Fatalf("other user state was removed unexpectedly")
	}

	controller.ForgetStream("camera")
	if len(controller.state) != 0 {
		t.Fatalf("state length = %d, want 0", len(controller.state))
	}
}
