package sfu

import (
	"io"
	"log/slog"
	"sync"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"aloqa/internal/platform/reliability"
)

// QualityLayer identifies a simulcast quality level. Values correspond to
// standard RTP Stream IDs (RIDs) used by browsers.
type QualityLayer string

const (
	QualityHigh   QualityLayer = "f" // full resolution
	QualityMedium QualityLayer = "h" // half resolution
	QualityLow    QualityLayer = "q" // quarter resolution
)

// QualityLayerFromRID maps an RID string to a QualityLayer. Returns
// QualityHigh for unrecognized RIDs.
func QualityLayerFromRID(rid string) QualityLayer {
	switch rid {
	case "q":
		return QualityLow
	case "h":
		return QualityMedium
	case "f":
		return QualityHigh
	default:
		return QualityHigh
	}
}

// simulcastLayer represents one quality tier of a simulcast track.
type simulcastLayer struct {
	remote     *webrtc.TrackRemote
	localTrack *webrtc.TrackLocalStaticRTP
	sinks      []PacketSink
	done       chan struct{}
	observed   ObservedTrack
}

// SimulcastTrack groups multiple quality layers of the same video source.
// Each layer has its own forwarding goroutine. Subscribers are attached to
// exactly one layer at a time and can switch dynamically.
type SimulcastTrack struct {
	// StreamID groups the layers (shared across all RIDs from the same source).
	StreamID string
	// SourcePeer is the user ID of the presenter.
	SourcePeer string

	mu     sync.RWMutex
	layers map[QualityLayer]*simulcastLayer

	// subscribers maps user ID to their current layer assignment.
	subscribers map[string]QualityLayer

	closed bool
}

// NewSimulcastTrack creates a new SimulcastTrack for a given source.
func NewSimulcastTrack(streamID, sourcePeer string) *SimulcastTrack {
	return &SimulcastTrack{
		StreamID:    streamID,
		SourcePeer:  sourcePeer,
		layers:      make(map[QualityLayer]*simulcastLayer),
		subscribers: make(map[string]QualityLayer),
	}
}

// AddLayer adds a quality layer to this simulcast track and starts forwarding.
// Called when Pion fires OnTrack for each simulcast RID.
//
// The outbound local track's StreamID is set to st.SourcePeer (the user ID)
// so subscribers can map remote streams back to a user via stream.id.
// See ALOQA-245 / BE-PR3 §B1. SimulcastTrack.StreamID is retained as the
// internal group key for the layers and is not exposed on the wire.
func (st *SimulcastTrack) AddLayer(quality QualityLayer, remote *webrtc.TrackRemote) error {
	outboundStreamID := st.SourcePeer
	localTrack, err := webrtc.NewTrackLocalStaticRTP(
		remote.Codec().RTPCodecCapability,
		remote.ID(),
		outboundStreamID,
	)
	if err != nil {
		return err
	}

	layer := &simulcastLayer{
		remote:     remote,
		localTrack: localTrack,
		done:       make(chan struct{}),
		observed: ObservedTrack{
			TrackID:    remote.ID(),
			StreamID:   outboundStreamID,
			SourcePeer: st.SourcePeer,
			MimeType:   remote.Codec().MimeType,
			Layer:      string(quality),
		},
	}

	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return io.ErrClosedPipe
	}
	st.layers[quality] = layer
	st.mu.Unlock()

	reliability.SafeGo("sfu_simulcast_forward", func() { st.forwardLayer(layer, quality) })

	slog.Info("simulcast layer added",
		"stream_id", st.StreamID,
		"source_peer", st.SourcePeer,
		"quality", quality,
		"codec", remote.Codec().MimeType,
	)

	return nil
}

// AddInjectedLayer adds a simulcast layer backed by inter-node relay packets.
//
// Outbound StreamID is forced to st.SourcePeer for the same reason as
// AddLayer — see ALOQA-245 / BE-PR3 §B1.
func (st *SimulcastTrack) AddInjectedLayer(quality QualityLayer, trackID, mimeType string) error {
	outboundStreamID := st.SourcePeer
	localTrack, err := webrtc.NewTrackLocalStaticRTP(codecCapabilityForMimeType(mimeType), trackID, outboundStreamID)
	if err != nil {
		return err
	}
	layer := &simulcastLayer{
		localTrack: localTrack,
		done:       make(chan struct{}),
		observed: ObservedTrack{
			TrackID:    trackID,
			StreamID:   outboundStreamID,
			SourcePeer: st.SourcePeer,
			MimeType:   mimeType,
			Layer:      string(quality),
		},
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed {
		return io.ErrClosedPipe
	}
	if _, exists := st.layers[quality]; exists {
		return nil
	}
	st.layers[quality] = layer
	return nil
}

// HasLayer returns true if the given quality layer is available.
func (st *SimulcastTrack) HasLayer(quality QualityLayer) bool {
	st.mu.RLock()
	defer st.mu.RUnlock()
	_, ok := st.layers[quality]
	return ok
}

// LocalTrackForLayer returns the local track for a specific quality layer.
// Returns nil if the layer doesn't exist.
func (st *SimulcastTrack) LocalTrackForLayer(quality QualityLayer) *webrtc.TrackLocalStaticRTP {
	st.mu.RLock()
	defer st.mu.RUnlock()

	layer, ok := st.layers[quality]
	if !ok {
		return nil
	}
	return layer.localTrack
}

// BestAvailableLayer returns the highest quality layer that is currently
// available, up to the requested maximum. Falls back to lower layers if
// the requested one isn't available.
func (st *SimulcastTrack) BestAvailableLayer(preferred QualityLayer) QualityLayer {
	st.mu.RLock()
	defer st.mu.RUnlock()

	order := layerPriority(preferred)
	for _, q := range order {
		if _, ok := st.layers[q]; ok {
			return q
		}
	}

	// Fallback: return any available layer.
	for q := range st.layers {
		return q
	}

	return QualityHigh
}

// SetSubscriberLayer records which layer a subscriber is currently receiving.
func (st *SimulcastTrack) SetSubscriberLayer(userID string, quality QualityLayer) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.subscribers[userID] = quality
}

// SubscriberLayer returns the current layer a subscriber is receiving.
func (st *SimulcastTrack) SubscriberLayer(userID string) (QualityLayer, bool) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	q, ok := st.subscribers[userID]
	return q, ok
}

// RemoveSubscriber removes a subscriber from tracking.
func (st *SimulcastTrack) RemoveSubscriber(userID string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.subscribers, userID)
}

func (st *SimulcastTrack) AddSink(quality QualityLayer, sink PacketSink) error {
	if sink == nil {
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	layer, ok := st.layers[quality]
	if !ok {
		return io.ErrClosedPipe
	}
	layer.sinks = append(layer.sinks, sink)
	return nil
}

func (st *SimulcastTrack) ObservedTrack(quality QualityLayer) (*ObservedTrack, bool) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	layer, ok := st.layers[quality]
	if !ok {
		return nil, false
	}
	return &layer.observed, true
}

// SubscriberCount returns the number of subscribers.
func (st *SimulcastTrack) SubscriberCount() int {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return len(st.subscribers)
}

// LayerCount returns the number of quality layers available.
func (st *SimulcastTrack) LayerCount() int {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return len(st.layers)
}

// AvailableLayers returns which quality layers are currently active.
func (st *SimulcastTrack) AvailableLayers() []QualityLayer {
	st.mu.RLock()
	defer st.mu.RUnlock()

	layers := make([]QualityLayer, 0, len(st.layers))
	for q := range st.layers {
		layers = append(layers, q)
	}
	return layers
}

// Close stops all forwarding goroutines.
func (st *SimulcastTrack) Close() {
	st.mu.Lock()
	defer st.mu.Unlock()

	if st.closed {
		return
	}
	st.closed = true

	for quality, layer := range st.layers {
		for _, sink := range snapshotLayerSinks(layer) {
			if err := sink.Close(); err != nil {
				slog.Warn("failed to close simulcast track sink", "stream_id", st.StreamID, "quality", quality, "error", err)
			}
		}
		close(layer.done)
		slog.Info("simulcast layer closed",
			"stream_id", st.StreamID,
			"source_peer", st.SourcePeer,
			"quality", quality,
		)
	}
}

// forwardLayer is the per-layer RTP forwarding loop.
func (st *SimulcastTrack) forwardLayer(layer *simulcastLayer, quality QualityLayer) {
	for {
		select {
		case <-layer.done:
			return
		default:
			packet, _, err := layer.remote.ReadRTP()
			if err != nil {
				if err == io.EOF {
					slog.Info("simulcast layer ended (EOF)",
						"stream_id", st.StreamID,
						"quality", quality,
					)
				} else {
					slog.Warn("simulcast layer read error",
						"stream_id", st.StreamID,
						"quality", quality,
						"error", err,
					)
				}
				return
			}

			if err := st.WriteRTP(quality, packet); err != nil {
				if err == io.ErrClosedPipe {
					slog.Info("simulcast layer: all subscribers gone",
						"stream_id", st.StreamID,
						"quality", quality,
					)
				}
				return
			}
		}
	}
}

func (st *SimulcastTrack) WriteRTP(quality QualityLayer, packet *rtp.Packet) error {
	if packet == nil {
		return nil
	}
	st.mu.RLock()
	layer, ok := st.layers[quality]
	st.mu.RUnlock()
	if !ok || layer == nil {
		return io.ErrClosedPipe
	}
	for _, sink := range snapshotLayerSinks(layer) {
		if err := sink.WriteRTP(packet); err != nil {
			slog.Warn("simulcast track sink write error",
				"stream_id", st.StreamID,
				"quality", quality,
				"error", err,
			)
		}
	}
	raw, err := packet.Marshal()
	if err != nil {
		return err
	}
	if _, err := layer.localTrack.Write(raw); err != nil && err != io.ErrClosedPipe {
		return err
	}
	return nil
}

func snapshotLayerSinks(layer *simulcastLayer) []PacketSink {
	if len(layer.sinks) == 0 {
		return nil
	}
	sinks := make([]PacketSink, len(layer.sinks))
	copy(sinks, layer.sinks)
	return sinks
}

// layerPriority returns quality layers to try in order, starting from the
// preferred one and falling back to lower qualities.
func layerPriority(preferred QualityLayer) []QualityLayer {
	switch preferred {
	case QualityHigh:
		return []QualityLayer{QualityHigh, QualityMedium, QualityLow}
	case QualityMedium:
		return []QualityLayer{QualityMedium, QualityLow, QualityHigh}
	case QualityLow:
		return []QualityLayer{QualityLow, QualityMedium, QualityHigh}
	default:
		return []QualityLayer{QualityHigh, QualityMedium, QualityLow}
	}
}
