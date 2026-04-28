// Package sfu implements a Selective Forwarding Unit for WebRTC media routing.
//
// The SFU uses a tiered participant model where presenters (up to 50) can
// send and receive audio/video, while viewers (up to 10,000+) only receive
// media. This design enables large-scale meetings similar to webinars and
// town halls.
package sfu

// RoomEventType identifies the kind of room lifecycle event.
type RoomEventType string

const (
	// EventPeerJoined is emitted when a presenter or viewer joins a room.
	EventPeerJoined RoomEventType = "peer.joined"
	// EventPeerLeft is emitted when a peer disconnects or is removed.
	EventPeerLeft RoomEventType = "peer.left"
	// EventTrackPublished is emitted when a presenter publishes a new media track.
	EventTrackPublished RoomEventType = "track.published"
	// EventTrackUnpublished is emitted when a presenter's track is removed.
	EventTrackUnpublished RoomEventType = "track.unpublished"
)

// RoomEvent represents a lifecycle event within an SFU room.
type RoomEvent struct {
	Type    RoomEventType
	RoomID  string
	UserID  string
	TrackID string
	Role    PeerRole
}
