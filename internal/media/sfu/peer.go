package sfu

import (
	"errors"
	"log/slog"
	"sync"

	"github.com/pion/webrtc/v4"

	"aloqa/internal/platform/reliability"
)

// PeerRole distinguishes presenters (send+receive) from viewers (receive-only).
type PeerRole string

const (
	// PeerRolePresenter can publish and receive media tracks.
	PeerRolePresenter PeerRole = "presenter"
	// PeerRoleViewer can only receive media tracks from presenters.
	PeerRoleViewer PeerRole = "viewer"
)

// Peer wraps a WebRTC PeerConnection and tracks the subscriptions (outbound
// tracks) that have been added to it. Each Peer belongs to exactly one Room.
type Peer struct {
	// UserID is the application-level identifier for this participant.
	UserID string
	// Role determines whether this peer can publish tracks.
	Role PeerRole
	// PC is the underlying Pion PeerConnection.
	PC *webrtc.PeerConnection

	room *Room
	mu   sync.Mutex

	closed bool
	// subscriptions maps track IDs to the RTPSender used to deliver that
	// track to this peer. Removing a subscription requires the sender
	// reference so we can call PeerConnection.RemoveTrack.
	subscriptions map[string]*webrtc.RTPSender
}

// newPeer creates a Peer and wires up the PeerConnection callbacks that
// integrate with the room lifecycle.
func newPeer(userID string, role PeerRole, pc *webrtc.PeerConnection, room *Room) *Peer {
	p := &Peer{
		UserID:        userID,
		Role:          role,
		PC:            pc,
		room:          room,
		subscriptions: make(map[string]*webrtc.RTPSender),
	}

	// When a presenter sends a new track, forward it through the room.
	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if role != PeerRolePresenter {
			slog.Warn("viewer attempted to publish track, ignoring",
				"user_id", userID,
				"track_id", track.ID(),
				"room_id", room.ID,
			)
			return
		}

		slog.Info("presenter published track",
			"user_id", userID,
			"track_id", track.ID(),
			"codec", track.Codec().MimeType,
			"room_id", room.ID,
		)

		room.handleNewTrack(p, track, receiver)
	})

	// Handle ICE connection state changes for logging and cleanup.
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		slog.Info("ICE connection state changed",
			"user_id", userID,
			"state", state.String(),
			"room_id", room.ID,
		)

		switch state {
		case webrtc.ICEConnectionStateFailed, webrtc.ICEConnectionStateDisconnected:
			// The peer's network path is broken. Remove them from the room
			// so their resources are cleaned up.
			room.RemovePeer(userID)
		}
	})

	// Handle overall connection state for definitive closure.
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		slog.Info("peer connection state changed",
			"user_id", userID,
			"state", state.String(),
			"room_id", room.ID,
		)

		switch state {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			room.RemovePeer(userID)
		}
	})

	return p
}

// Subscribe adds a track router's local track to this peer's PeerConnection
// so the peer begins receiving the media. The RTPSender is stored so the
// subscription can be removed later.
func (p *Peer) Subscribe(router *TrackRouter) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return errors.New("peer is closed")
	}

	// Already subscribed to this track.
	if _, exists := p.subscriptions[router.ID]; exists {
		return nil
	}

	sender, err := p.PC.AddTrack(router.LocalTrack())
	if err != nil {
		return err
	}

	p.subscriptions[router.ID] = sender
	router.AddSubscriber(p.UserID)

	// Consume RTCP from the sender. This is required by Pion to process
	// receiver reports and NACK feedback from the subscriber.
	reliability.SafeGo("sfu_peer_rtcp_consume", func() {
		buf := make([]byte, 1500)
		for {
			if _, _, rtcpErr := sender.Read(buf); rtcpErr != nil {
				return
			}
		}
	})

	slog.Debug("peer subscribed to track",
		"user_id", p.UserID,
		"track_id", router.ID,
		"source_peer", router.SourcePeer,
	)

	return nil
}

// Unsubscribe removes a track subscription from this peer's PeerConnection.
func (p *Peer) Unsubscribe(trackID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}

	sender, exists := p.subscriptions[trackID]
	if !exists {
		return nil
	}

	if err := p.PC.RemoveTrack(sender); err != nil {
		return err
	}

	delete(p.subscriptions, trackID)

	slog.Debug("peer unsubscribed from track",
		"user_id", p.UserID,
		"track_id", trackID,
	)

	return nil
}

// Close shuts down the PeerConnection and marks the peer as closed.
// It is safe to call multiple times.
func (p *Peer) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}
	p.closed = true

	p.subscriptions = nil

	if err := p.PC.Close(); err != nil {
		slog.Warn("error closing peer connection",
			"user_id", p.UserID,
			"error", err,
		)
		return err
	}

	slog.Info("peer closed",
		"user_id", p.UserID,
		"role", p.Role,
	)

	return nil
}
