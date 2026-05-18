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
