package sfu

import (
	"testing"

	"github.com/pion/webrtc/v4"
)

// TestNewInjectedTrackRouterForcesStreamIDToUserID verifies the
// ALOQA-245 / BE-PR3 §B1 contract: the outbound local track's StreamID
// equals sourceUserID regardless of the caller-supplied streamID. The
// same invariant must hold inside NewTrackRouter (covered by integration
// tests via room_test.go), but the production branch there requires a
// real webrtc.TrackRemote that cannot be constructed in a unit test.
// ALK-465 (formerly ALOQA-256) adds explicit publisher-leak inequality
// assertions and an observed.MimeType check.
func TestNewInjectedTrackRouterForcesStreamIDToUserID(t *testing.T) {
	const (
		sourceUserID      = "user-42"
		trackID           = "track-xyz"
		publisherStreamID = "browser-original-stream"
		mimeType          = "audio/opus"
	)

	router, err := NewInjectedTrackRouter(sourceUserID, trackID, publisherStreamID, mimeType)
	if err != nil {
		t.Fatalf("NewInjectedTrackRouter: %v", err)
	}

	if got := router.localTrack.StreamID(); got != sourceUserID {
		t.Fatalf("localTrack.StreamID() = %q, want sourceUserID %q (publisher streamID %q must be ignored)",
			got, sourceUserID, publisherStreamID)
	}
	// ALK-465 F2: explicit inequality — guards against a regression where the
	// publisher's StreamID leaks even if a future refactor happens to also
	// match sourceUserID coincidentally.
	if got := router.localTrack.StreamID(); got == publisherStreamID {
		t.Fatalf("localTrack.StreamID() = %q, must NOT equal publisher streamID %q", got, publisherStreamID)
	}
	if router.streamID != sourceUserID {
		t.Fatalf("router.streamID = %q, want %q", router.streamID, sourceUserID)
	}
	if router.streamID == publisherStreamID {
		t.Fatalf("router.streamID = %q, must NOT equal publisher streamID %q", router.streamID, publisherStreamID)
	}
	if router.observed.StreamID != sourceUserID {
		t.Fatalf("router.observed.StreamID = %q, want %q", router.observed.StreamID, sourceUserID)
	}
	if router.observed.StreamID == publisherStreamID {
		t.Fatalf("router.observed.StreamID = %q, must NOT equal publisher streamID %q", router.observed.StreamID, publisherStreamID)
	}
	if router.observed.SourcePeer != sourceUserID {
		t.Fatalf("router.observed.SourcePeer = %q, want %q", router.observed.SourcePeer, sourceUserID)
	}
	if router.observed.TrackID != trackID {
		t.Fatalf("router.observed.TrackID = %q, want %q", router.observed.TrackID, trackID)
	}
	// ALK-465 F2: assert observed.MimeType is propagated from the constructor
	// argument unchanged.
	if router.observed.MimeType != mimeType {
		t.Fatalf("router.observed.MimeType = %q, want %q", router.observed.MimeType, mimeType)
	}
}

// TestNewLocalTrackForSourceContract verifies the §B1 invariant on the
// shared helper that BOTH NewTrackRouter and NewInjectedTrackRouter delegate
// to. Direct unit coverage of the helper closes the F3 gap from the ALOQA-245
// review — previously the production NewTrackRouter branch could only be
// exercised through room_test.go integration. ALK-465.
func TestNewLocalTrackForSourceContract(t *testing.T) {
	t.Run("opus audio", func(t *testing.T) {
		const (
			sourceUserID = "user-A"
			trackID      = "track-audio-1"
		)
		capability := webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  2,
		}
		localTrack, outboundStreamID, err := newLocalTrackForSource(sourceUserID, trackID, capability)
		if err != nil {
			t.Fatalf("newLocalTrackForSource: %v", err)
		}
		if outboundStreamID != sourceUserID {
			t.Fatalf("outboundStreamID = %q, want %q", outboundStreamID, sourceUserID)
		}
		if got := localTrack.StreamID(); got != sourceUserID {
			t.Fatalf("localTrack.StreamID() = %q, want %q", got, sourceUserID)
		}
		if got := localTrack.ID(); got != trackID {
			t.Fatalf("localTrack.ID() = %q, want %q", got, trackID)
		}
		if got := localTrack.Codec().MimeType; got != webrtc.MimeTypeOpus {
			t.Fatalf("localTrack.Codec().MimeType = %q, want %q", got, webrtc.MimeTypeOpus)
		}
	})

	t.Run("vp8 video", func(t *testing.T) {
		const (
			sourceUserID = "user-B"
			trackID      = "track-video-1"
		)
		capability := webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeVP8,
			ClockRate: 90000,
		}
		localTrack, outboundStreamID, err := newLocalTrackForSource(sourceUserID, trackID, capability)
		if err != nil {
			t.Fatalf("newLocalTrackForSource: %v", err)
		}
		if outboundStreamID != sourceUserID {
			t.Fatalf("outboundStreamID = %q, want %q", outboundStreamID, sourceUserID)
		}
		if got := localTrack.StreamID(); got != sourceUserID {
			t.Fatalf("localTrack.StreamID() = %q, want %q", got, sourceUserID)
		}
		if got := localTrack.Codec().MimeType; got != webrtc.MimeTypeVP8 {
			t.Fatalf("localTrack.Codec().MimeType = %q, want %q", got, webrtc.MimeTypeVP8)
		}
	})

	t.Run("ignores caller-supplied stream context", func(t *testing.T) {
		// Even if the caller could somehow communicate a different streamID
		// through the codec capability (it can't — capability has no streamID
		// field), the helper must still ground StreamID on sourceUserID. We
		// assert by constructing two helpers with identical capabilities but
		// different sourceUserIDs and confirming each gets its own StreamID.
		capability := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}

		a, aStream, err := newLocalTrackForSource("user-alpha", "track-1", capability)
		if err != nil {
			t.Fatalf("alpha: %v", err)
		}
		b, bStream, err := newLocalTrackForSource("user-bravo", "track-2", capability)
		if err != nil {
			t.Fatalf("bravo: %v", err)
		}
		if aStream == bStream {
			t.Fatalf("two sources must produce distinct StreamIDs, both got %q", aStream)
		}
		if a.StreamID() != "user-alpha" {
			t.Fatalf("a.StreamID() = %q, want user-alpha", a.StreamID())
		}
		if b.StreamID() != "user-bravo" {
			t.Fatalf("b.StreamID() = %q, want user-bravo", b.StreamID())
		}
	})
}

// TestSimulcastAddInjectedLayerForcesStreamIDToUserID verifies the §B1
// contract extends to simulcast injected layers: outbound local track
// StreamID must equal SimulcastTrack.SourcePeer (the user ID) even when
// SimulcastTrack.StreamID is set to the publisher's original group key.
func TestSimulcastAddInjectedLayerForcesStreamIDToUserID(t *testing.T) {
	const (
		sourcePeer        = "user-42"
		publisherStreamID = "browser-original-stream"
		trackID           = "track-screen-low"
		mimeType          = "video/VP8"
		quality           = QualityLow
	)

	st := NewSimulcastTrack(publisherStreamID, sourcePeer)
	if err := st.AddInjectedLayer(quality, trackID, mimeType); err != nil {
		t.Fatalf("AddInjectedLayer: %v", err)
	}

	st.mu.RLock()
	layer, ok := st.layers[quality]
	st.mu.RUnlock()
	if !ok {
		t.Fatalf("layer %q not registered on simulcast track", quality)
	}
	if got := layer.localTrack.StreamID(); got != sourcePeer {
		t.Fatalf("localTrack.StreamID() = %q, want sourcePeer %q (publisher streamID %q must not leak)",
			got, sourcePeer, publisherStreamID)
	}
	if layer.observed.StreamID != sourcePeer {
		t.Fatalf("layer.observed.StreamID = %q, want %q", layer.observed.StreamID, sourcePeer)
	}
}
