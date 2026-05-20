package sfu

import "testing"

// TestNewInjectedTrackRouterForcesStreamIDToUserID verifies the
// ALOQA-245 / BE-PR3 §B1 contract: the outbound local track's StreamID
// equals sourceUserID regardless of the caller-supplied streamID. The
// same invariant must hold inside NewTrackRouter (covered by integration
// tests via room_test.go), but the production branch there requires a
// real webrtc.TrackRemote that cannot be constructed in a unit test.
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
	if router.streamID != sourceUserID {
		t.Fatalf("router.streamID = %q, want %q", router.streamID, sourceUserID)
	}
	if router.observed.StreamID != sourceUserID {
		t.Fatalf("router.observed.StreamID = %q, want %q", router.observed.StreamID, sourceUserID)
	}
	if router.observed.SourcePeer != sourceUserID {
		t.Fatalf("router.observed.SourcePeer = %q, want %q", router.observed.SourcePeer, sourceUserID)
	}
	if router.observed.TrackID != trackID {
		t.Fatalf("router.observed.TrackID = %q, want %q", router.observed.TrackID, trackID)
	}
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
