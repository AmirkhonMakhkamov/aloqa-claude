package sfu

import (
	"io"
	"log/slog"
	"sync"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"aloqa/internal/platform/reliability"
)

// TrackRouter reads RTP packets from a presenter's remote track and writes
// them to a local track that can be added to any number of viewer peer
// connections. Pion's TrackLocalStaticRTP handles the fan-out: a single
// Write call delivers the packet to every subscribed PeerConnection.
type TrackRouter struct {
	// ID uniquely identifies this track router (same as the remote track ID).
	ID string
	// SourcePeer is the user ID of the presenter who published this track.
	SourcePeer string

	track      *webrtc.TrackRemote
	localTrack *webrtc.TrackLocalStaticRTP
	streamID   string
	mimeType   string
	observed   ObservedTrack

	mu          sync.RWMutex
	subscribers map[string]bool // user IDs of peers receiving this track
	sinks       []PacketSink

	done chan struct{}
}

// NewTrackRouter creates a TrackRouter that mirrors a remote track into a
// local static RTP track with the same codec parameters. It immediately
// starts a forwarding goroutine that copies RTP packets from the remote
// track to the local track.
func NewTrackRouter(sourceUserID string, remoteTrack *webrtc.TrackRemote) (*TrackRouter, error) {
	localTrack, err := webrtc.NewTrackLocalStaticRTP(
		remoteTrack.Codec().RTPCodecCapability,
		remoteTrack.ID(),
		remoteTrack.StreamID(),
	)
	if err != nil {
		return nil, err
	}

	tr := &TrackRouter{
		ID:         remoteTrack.ID(),
		SourcePeer: sourceUserID,
		track:      remoteTrack,
		localTrack: localTrack,
		streamID:   remoteTrack.StreamID(),
		mimeType:   remoteTrack.Codec().MimeType,
		observed: ObservedTrack{
			TrackID:    remoteTrack.ID(),
			StreamID:   remoteTrack.StreamID(),
			SourcePeer: sourceUserID,
			MimeType:   remoteTrack.Codec().MimeType,
		},
		subscribers: make(map[string]bool),
		done:        make(chan struct{}),
	}

	reliability.SafeGo("sfu_track_forward", tr.forward)

	slog.Info("track router started",
		"track_id", tr.ID,
		"source_peer", sourceUserID,
		"codec", remoteTrack.Codec().MimeType,
	)

	return tr, nil
}

// NewInjectedTrackRouter creates a local-only track router that can receive
// RTP from an inter-node relay instead of a local PeerConnection publisher.
func NewInjectedTrackRouter(sourceUserID, trackID, streamID, mimeType string) (*TrackRouter, error) {
	localTrack, err := webrtc.NewTrackLocalStaticRTP(codecCapabilityForMimeType(mimeType), trackID, streamID)
	if err != nil {
		return nil, err
	}
	return &TrackRouter{
		ID:         trackID,
		SourcePeer: sourceUserID,
		localTrack: localTrack,
		streamID:   streamID,
		mimeType:   mimeType,
		observed: ObservedTrack{
			TrackID:    trackID,
			StreamID:   streamID,
			SourcePeer: sourceUserID,
			MimeType:   mimeType,
		},
		subscribers: make(map[string]bool),
		done:        make(chan struct{}),
	}, nil
}

// LocalTrack returns the local track that should be added to subscriber
// PeerConnections via AddTrack.
func (tr *TrackRouter) LocalTrack() *webrtc.TrackLocalStaticRTP {
	return tr.localTrack
}

func (tr *TrackRouter) ObservedTrack() ObservedTrack {
	return tr.observed
}

// AddSubscriber marks a user as subscribed to this track.
func (tr *TrackRouter) AddSubscriber(userID string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.subscribers[userID] = true
}

// RemoveSubscriber removes a user's subscription from this track.
func (tr *TrackRouter) RemoveSubscriber(userID string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	delete(tr.subscribers, userID)
}

// SubscriberCount returns the current number of subscribers.
func (tr *TrackRouter) SubscriberCount() int {
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	return len(tr.subscribers)
}

func (tr *TrackRouter) AddSink(sink PacketSink) {
	if sink == nil {
		return
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.sinks = append(tr.sinks, sink)
}

// WriteRTP injects an RTP packet into a relay-backed track router.
func (tr *TrackRouter) WriteRTP(packet *rtp.Packet) error {
	if packet == nil {
		return nil
	}
	for _, sink := range tr.snapshotSinks() {
		if err := sink.WriteRTP(packet); err != nil {
			slog.Warn("track sink write error",
				"track_id", tr.ID,
				"source_peer", tr.SourcePeer,
				"error", err,
			)
		}
	}
	raw, err := packet.Marshal()
	if err != nil {
		return err
	}
	if _, err := tr.localTrack.Write(raw); err != nil && err != io.ErrClosedPipe {
		return err
	}
	return nil
}

// Close stops the forwarding goroutine and releases resources.
func (tr *TrackRouter) Close() {
	select {
	case <-tr.done:
		// Already closed.
	default:
		close(tr.done)
		for _, sink := range tr.snapshotSinks() {
			if err := sink.Close(); err != nil {
				slog.Warn("failed to close track sink", "track_id", tr.ID, "source_peer", tr.SourcePeer, "error", err)
			}
		}
		slog.Info("track router closed",
			"track_id", tr.ID,
			"source_peer", tr.SourcePeer,
		)
	}
}

// forward is the core SFU loop. It reads RTP packets from the remote track
// and writes them to the local track with minimal allocation. The local track
// handles fan-out to all subscribed PeerConnections internally.
func (tr *TrackRouter) forward() {
	for {
		select {
		case <-tr.done:
			return
		default:
			packet, _, readErr := tr.track.ReadRTP()
			if readErr != nil {
				if readErr == io.EOF {
					slog.Info("track ended (EOF)",
						"track_id", tr.ID,
						"source_peer", tr.SourcePeer,
					)
				} else {
					slog.Warn("track read error",
						"track_id", tr.ID,
						"source_peer", tr.SourcePeer,
						"error", readErr,
					)
				}
				return
			}

			if err := tr.WriteRTP(packet); err != nil {
				if err == io.ErrClosedPipe {
					slog.Info("all subscribers gone, stopping forward",
						"track_id", tr.ID,
						"source_peer", tr.SourcePeer,
					)
				} else {
					slog.Warn("track write error",
						"track_id", tr.ID,
						"source_peer", tr.SourcePeer,
						"error", err,
					)
				}
				return
			}
		}
	}
}

func (tr *TrackRouter) snapshotSinks() []PacketSink {
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	if len(tr.sinks) == 0 {
		return nil
	}
	sinks := make([]PacketSink, len(tr.sinks))
	copy(sinks, tr.sinks)
	return sinks
}

func codecCapabilityForMimeType(mimeType string) webrtc.RTPCodecCapability {
	switch mimeType {
	case webrtc.MimeTypeOpus:
		return webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   48000,
			Channels:    2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		}
	case "audio/LYRA":
		return webrtc.RTPCodecCapability{
			MimeType:  "audio/LYRA",
			ClockRate: 48000,
			Channels:  1,
		}
	case webrtc.MimeTypeVP9:
		return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP9, ClockRate: 90000, SDPFmtpLine: "profile-id=0"}
	case webrtc.MimeTypeAV1:
		return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeAV1, ClockRate: 90000}
	default:
		return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}
	}
}
