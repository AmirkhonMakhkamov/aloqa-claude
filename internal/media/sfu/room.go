package sfu

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"aloqa/internal/platform/reliability"
)

// Default capacity limits.
const (
	DefaultMaxPresenters = 50
	DefaultMaxViewers    = 10000
	DefaultMaxTracks     = 8
)

// RoomOptions configures a room's capacity and feature flags.
type RoomOptions struct {
	MaxPresenters         int  // Maximum number of presenters (default 50).
	MaxViewers            int  // Maximum number of viewers (default 10000).
	MaxTracksPerPresenter int  // Maximum audio/video/screen tracks per presenter.
	Simulcast             bool // Enable simulcast layer forwarding.
	Recording             bool // Whether the room is being recorded.
	Adaptive              AdaptiveOptions
}

// Room is a media session containing presenters and viewers. Presenters
// publish tracks that are forwarded to all other participants. Viewers
// receive tracks but cannot publish.
type Room struct {
	// ID uniquely identifies this room.
	ID      string
	options RoomOptions

	presenters      map[string]*Peer           // user_id -> presenter peer
	viewers         map[string]*Peer           // user_id -> viewer peer
	tracks          map[string]*TrackRouter    // track_id -> router (non-simulcast)
	simulcastTracks map[string]*SimulcastTrack // stream_id -> simulcast track group
	adaptive        *AdaptiveController
	observers       map[string]TrackObserver

	mu      sync.RWMutex
	closed  chan struct{}
	onEvent func(RoomEvent)
}

type SubscriberStreamTarget struct {
	StreamID       string
	SourcePeer     string
	CurrentQuality QualityLayer
	Available      []QualityLayer
	ScreenShare    bool
}

type RelayTrackDescriptor struct {
	TrackID    string
	StreamID   string
	SourcePeer string
	MimeType   string
	Layer      string
}

// newRoom creates a Room with the given options, applying defaults where
// values are zero.
func newRoom(id string, opts RoomOptions) *Room {
	if opts.MaxPresenters <= 0 {
		opts.MaxPresenters = DefaultMaxPresenters
	}
	if opts.MaxViewers <= 0 {
		opts.MaxViewers = DefaultMaxViewers
	}
	if opts.MaxTracksPerPresenter <= 0 {
		opts.MaxTracksPerPresenter = DefaultMaxTracks
	}

	return &Room{
		ID:              id,
		options:         opts,
		presenters:      make(map[string]*Peer),
		viewers:         make(map[string]*Peer),
		tracks:          make(map[string]*TrackRouter),
		simulcastTracks: make(map[string]*SimulcastTrack),
		adaptive:        NewAdaptiveController(opts.Adaptive),
		observers:       make(map[string]TrackObserver),
		closed:          make(chan struct{}),
	}
}

// SetEventHandler registers a callback for room lifecycle events.
// Not thread-safe; call before adding peers.
func (r *Room) SetEventHandler(handler func(RoomEvent)) {
	r.onEvent = handler
}

func (r *Room) Done() <-chan struct{} {
	return r.closed
}

func (r *Room) AddObserver(observer TrackObserver) string {
	if observer == nil {
		return ""
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	select {
	case <-r.closed:
		return ""
	default:
	}

	id := uuid.NewString()
	r.observers[id] = observer
	for _, router := range r.tracks {
		r.attachObserverToTrackLocked(observer, router.ObservedTrack(), router.AddSink)
	}
	for _, st := range r.simulcastTracks {
		for _, quality := range st.AvailableLayers() {
			track, ok := st.ObservedTrack(quality)
			if !ok {
				continue
			}
			layer := quality
			r.attachObserverToTrackLocked(observer, *track, func(sink PacketSink) {
				_ = st.AddSink(layer, sink)
			})
		}
	}
	return id
}

func (r *Room) RemoveObserver(id string) {
	if id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.observers, id)
}

// AddPresenter registers a PeerConnection as a presenter in the room.
// Presenters can publish tracks (via OnTrack) and receive all existing
// presenter tracks.
func (r *Room) AddPresenter(userID string, pc *webrtc.PeerConnection) (*Peer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	select {
	case <-r.closed:
		return nil, errors.New("room is closed")
	default:
	}

	if _, exists := r.presenters[userID]; exists {
		return nil, fmt.Errorf("presenter %s already in room %s", userID, r.ID)
	}
	if _, exists := r.viewers[userID]; exists {
		return nil, fmt.Errorf("user %s already in room %s as viewer", userID, r.ID)
	}

	if len(r.presenters) >= r.options.MaxPresenters {
		return nil, fmt.Errorf("room %s has reached the presenter limit (%d)", r.ID, r.options.MaxPresenters)
	}

	peer := newPeer(userID, PeerRolePresenter, pc, r)
	r.presenters[userID] = peer

	// Subscribe the new presenter to all existing tracks so they can see
	// other presenters.
	r.subscribePeerToExistingTracks(peer)

	r.emitEvent(RoomEvent{
		Type:   EventPeerJoined,
		RoomID: r.ID,
		UserID: userID,
		Role:   PeerRolePresenter,
	})

	slog.Info("presenter added to room",
		"user_id", userID,
		"room_id", r.ID,
		"presenter_count", len(r.presenters),
	)

	return peer, nil
}

// AddViewer registers a PeerConnection as a viewer in the room. Viewers
// receive all existing presenter tracks but cannot publish.
func (r *Room) AddViewer(userID string, pc *webrtc.PeerConnection) (*Peer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	select {
	case <-r.closed:
		return nil, errors.New("room is closed")
	default:
	}

	if _, exists := r.viewers[userID]; exists {
		return nil, fmt.Errorf("viewer %s already in room %s", userID, r.ID)
	}
	if _, exists := r.presenters[userID]; exists {
		return nil, fmt.Errorf("user %s already in room %s as presenter", userID, r.ID)
	}

	if len(r.viewers) >= r.options.MaxViewers {
		return nil, fmt.Errorf("room %s has reached the viewer limit (%d)", r.ID, r.options.MaxViewers)
	}

	peer := newPeer(userID, PeerRoleViewer, pc, r)
	r.viewers[userID] = peer

	// Subscribe the viewer to all existing presenter tracks.
	r.subscribePeerToExistingTracks(peer)

	r.emitEvent(RoomEvent{
		Type:   EventPeerJoined,
		RoomID: r.ID,
		UserID: userID,
		Role:   PeerRoleViewer,
	})

	slog.Info("viewer added to room",
		"user_id", userID,
		"room_id", r.ID,
		"viewer_count", len(r.viewers),
	)

	return peer, nil
}

// RemovePeer disconnects and removes a peer (presenter or viewer) from the
// room. If the peer is a presenter, their published tracks are closed and
// removed from all subscribers.
func (r *Room) RemovePeer(userID string) {
	r.mu.Lock()

	// Check if already closed or peer already removed.
	select {
	case <-r.closed:
		r.mu.Unlock()
		return
	default:
	}

	peer, role := r.findPeerLocked(userID)
	if peer == nil {
		r.mu.Unlock()
		return
	}

	// Remove the peer from the appropriate map.
	switch role {
	case PeerRolePresenter:
		delete(r.presenters, userID)
		// Close all tracks published by this presenter.
		r.removePresenterTracksLocked(userID)
	case PeerRoleViewer:
		delete(r.viewers, userID)
		// Remove this viewer from all track subscriber lists.
		r.removeViewerSubscriptionsLocked(userID)
	}
	r.adaptive.ForgetUser(userID)

	r.mu.Unlock()

	// Close the PeerConnection outside the lock to avoid deadlocks.
	if err := peer.Close(); err != nil {
		slog.Warn("error closing peer on remove",
			"user_id", userID,
			"room_id", r.ID,
			"error", err,
		)
	}

	r.emitEvent(RoomEvent{
		Type:   EventPeerLeft,
		RoomID: r.ID,
		UserID: userID,
		Role:   role,
	})

	slog.Info("peer removed from room",
		"user_id", userID,
		"role", role,
		"room_id", r.ID,
	)
}

// Presenters returns a snapshot of all presenter peers.
func (r *Room) Presenters() []*Peer {
	r.mu.RLock()
	defer r.mu.RUnlock()

	peers := make([]*Peer, 0, len(r.presenters))
	for _, p := range r.presenters {
		peers = append(peers, p)
	}
	return peers
}

// Viewers returns a snapshot of all viewer peers.
func (r *Room) Viewers() []*Peer {
	r.mu.RLock()
	defer r.mu.RUnlock()

	peers := make([]*Peer, 0, len(r.viewers))
	for _, p := range r.viewers {
		peers = append(peers, p)
	}
	return peers
}

// PeerCount returns the number of presenters and viewers in the room.
func (r *Room) PeerCount() (presenters, viewers int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.presenters), len(r.viewers)
}

// Peer returns a peer and role by user ID, if present.
func (r *Room) Peer(userID string) (*Peer, PeerRole, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	peer, role := r.findPeerLocked(userID)
	return peer, role, peer != nil
}

// SubscriberTargets returns the simulcast streams the user is currently
// subscribed to, along with the current and available layers for each stream.
func (r *Room) SubscriberTargets(userID string) []SubscriberStreamTarget {
	r.mu.RLock()
	defer r.mu.RUnlock()

	targets := make([]SubscriberStreamTarget, 0, len(r.simulcastTracks))
	for streamID, st := range r.simulcastTracks {
		current, ok := st.SubscriberLayer(userID)
		if !ok {
			continue
		}
		targets = append(targets, SubscriberStreamTarget{
			StreamID:       streamID,
			SourcePeer:     st.SourcePeer,
			CurrentQuality: current,
			Available:      st.AvailableLayers(),
			ScreenShare:    looksLikeScreenShare(streamID),
		})
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].ScreenShare != targets[j].ScreenShare {
			return targets[i].ScreenShare
		}
		return targets[i].StreamID < targets[j].StreamID
	})
	return targets
}

// InjectRelayPacket forwards an RTP packet received from another media node
// into this room as if it had been published locally.
func (r *Room) InjectRelayPacket(desc RelayTrackDescriptor, packet *rtp.Packet) error {
	if packet == nil {
		return nil
	}
	if desc.StreamID == "" {
		desc.StreamID = desc.TrackID
	}
	if desc.TrackID == "" {
		desc.TrackID = desc.StreamID
	}
	if desc.SourcePeer == "" {
		desc.SourcePeer = "relay"
	}

	if desc.Layer != "" {
		return r.injectRelaySimulcastPacket(desc, packet)
	}
	return r.injectRelayTrackPacket(desc, packet)
}

// Close shuts down the room, disconnecting all peers and stopping all
// track routers. Safe to call multiple times.
func (r *Room) Close() {
	r.mu.Lock()

	select {
	case <-r.closed:
		r.mu.Unlock()
		return
	default:
		close(r.closed)
	}

	// Collect all peers and tracks to close outside the lock.
	peersToClose := make([]*Peer, 0, len(r.presenters)+len(r.viewers))
	for _, p := range r.presenters {
		peersToClose = append(peersToClose, p)
	}
	for _, p := range r.viewers {
		peersToClose = append(peersToClose, p)
	}

	tracksToClose := make([]*TrackRouter, 0, len(r.tracks))
	for _, tr := range r.tracks {
		tracksToClose = append(tracksToClose, tr)
	}

	simulcastToClose := make([]*SimulcastTrack, 0, len(r.simulcastTracks))
	for _, st := range r.simulcastTracks {
		simulcastToClose = append(simulcastToClose, st)
	}

	// Clear internal maps.
	r.presenters = make(map[string]*Peer)
	r.viewers = make(map[string]*Peer)
	r.tracks = make(map[string]*TrackRouter)
	r.simulcastTracks = make(map[string]*SimulcastTrack)

	r.mu.Unlock()

	// Close resources outside the lock to avoid deadlocks from
	// PeerConnection callbacks.
	for _, tr := range tracksToClose {
		tr.Close()
	}
	for _, st := range simulcastToClose {
		st.Close()
	}
	for _, p := range peersToClose {
		if err := p.Close(); err != nil {
			slog.Warn("failed to close peer while closing room", "room_id", r.ID, "user_id", p.UserID, "error", err)
		}
	}

	slog.Info("room closed", "room_id", r.ID)
}

// handleNewTrack is called when a presenter's PeerConnection fires OnTrack.
// It detects whether the track is part of a simulcast group (has an RID) and
// either creates a SimulcastTrack or a plain TrackRouter accordingly.
func (r *Room) handleNewTrack(peer *Peer, track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
	rid := track.RID()

	// Simulcast track: group by stream ID.
	if rid != "" {
		r.handleSimulcastTrack(peer, track, rid)
		return
	}

	// Non-simulcast: create a regular TrackRouter.
	if !r.canPublishTrack(peer.UserID, track.ID()) {
		slog.Warn("presenter track rejected by room policy",
			"user_id", peer.UserID,
			"track_id", track.ID(),
			"room_id", r.ID,
			"max_tracks", r.options.MaxTracksPerPresenter,
		)
		return
	}

	router, err := NewTrackRouter(peer.UserID, track)
	if err != nil {
		slog.Error("failed to create track router",
			"user_id", peer.UserID,
			"track_id", track.ID(),
			"room_id", r.ID,
			"error", err,
		)
		return
	}

	r.mu.Lock()

	select {
	case <-r.closed:
		r.mu.Unlock()
		router.Close()
		return
	default:
	}

	r.tracks[router.ID] = router
	r.attachObserversToTrackLocked(router.ObservedTrack(), router.AddSink)

	// Subscribe all presenters (except the source) and all viewers.
	var subscribeErrors []error
	for uid, p := range r.presenters {
		if uid == peer.UserID {
			continue // Don't subscribe the source to their own track.
		}
		if err := p.Subscribe(router); err != nil {
			subscribeErrors = append(subscribeErrors, fmt.Errorf("presenter %s: %w", uid, err))
		}
	}
	for uid, p := range r.viewers {
		if err := p.Subscribe(router); err != nil {
			subscribeErrors = append(subscribeErrors, fmt.Errorf("viewer %s: %w", uid, err))
		}
	}

	r.mu.Unlock()

	for _, subErr := range subscribeErrors {
		slog.Warn("failed to subscribe peer to new track",
			"track_id", router.ID,
			"room_id", r.ID,
			"error", subErr,
		)
	}

	r.emitEvent(RoomEvent{
		Type:    EventTrackPublished,
		RoomID:  r.ID,
		UserID:  peer.UserID,
		TrackID: router.ID,
		Role:    PeerRolePresenter,
	})

	slog.Info("track published and subscribers notified",
		"track_id", router.ID,
		"source_peer", peer.UserID,
		"room_id", r.ID,
		"subscriber_count", router.SubscriberCount(),
	)
}

// handleSimulcastTrack handles an incoming simulcast layer. If this is the
// first layer for the stream, it creates a SimulcastTrack and subscribes all
// peers to the best available layer. If layers are added later, existing
// subscribers can switch via SetSubscriberQuality.
func (r *Room) handleSimulcastTrack(peer *Peer, track *webrtc.TrackRemote, rid string) {
	quality := QualityLayerFromRID(rid)
	streamID := track.StreamID()

	r.mu.Lock()

	select {
	case <-r.closed:
		r.mu.Unlock()
		return
	default:
	}

	st, exists := r.simulcastTracks[streamID]
	if !exists {
		if !r.canPublishTrackLocked(peer.UserID, streamID) {
			r.mu.Unlock()
			slog.Warn("presenter simulcast track rejected by room policy",
				"user_id", peer.UserID,
				"stream_id", streamID,
				"room_id", r.ID,
				"max_tracks", r.options.MaxTracksPerPresenter,
			)
			return
		}
		st = NewSimulcastTrack(streamID, peer.UserID)
		r.simulcastTracks[streamID] = st
	}

	r.mu.Unlock()

	if err := st.AddLayer(quality, track); err != nil {
		slog.Error("failed to add simulcast layer",
			"stream_id", streamID,
			"quality", quality,
			"room_id", r.ID,
			"error", err,
		)
		return
	}
	if observed, ok := st.ObservedTrack(quality); ok {
		r.mu.Lock()
		r.attachObserversToTrackLocked(*observed, func(sink PacketSink) {
			_ = st.AddSink(quality, sink)
		})
		r.mu.Unlock()
	}

	// If this is the first layer, subscribe all peers to it.
	if !exists {
		r.mu.Lock()
		var subscribeErrors []error
		for uid, p := range r.presenters {
			if uid == peer.UserID {
				continue
			}
			if err := r.subscribeToSimulcastLocked(p, st, quality); err != nil {
				subscribeErrors = append(subscribeErrors, fmt.Errorf("presenter %s: %w", uid, err))
			}
		}
		for uid, p := range r.viewers {
			if err := r.subscribeToSimulcastLocked(p, st, quality); err != nil {
				subscribeErrors = append(subscribeErrors, fmt.Errorf("viewer %s: %w", uid, err))
			}
		}
		r.mu.Unlock()

		for _, subErr := range subscribeErrors {
			slog.Warn("failed to subscribe peer to simulcast track",
				"stream_id", streamID,
				"room_id", r.ID,
				"error", subErr,
			)
		}

		r.emitEvent(RoomEvent{
			Type:    EventTrackPublished,
			RoomID:  r.ID,
			UserID:  peer.UserID,
			TrackID: streamID,
			Role:    PeerRolePresenter,
		})
	}

	slog.Info("simulcast layer ready",
		"stream_id", streamID,
		"source_peer", peer.UserID,
		"quality", quality,
		"room_id", r.ID,
		"total_layers", st.LayerCount(),
	)
}

func (r *Room) injectRelayTrackPacket(desc RelayTrackDescriptor, packet *rtp.Packet) error {
	r.mu.Lock()
	router, ok := r.tracks[desc.TrackID]
	if !ok {
		var err error
		router, err = NewInjectedTrackRouter(desc.SourcePeer, desc.TrackID, desc.StreamID, desc.MimeType)
		if err != nil {
			r.mu.Unlock()
			return err
		}
		r.tracks[router.ID] = router
		r.attachObserversToTrackLocked(router.ObservedTrack(), router.AddSink)
		for uid, peer := range r.presenters {
			if uid == desc.SourcePeer {
				continue
			}
			if err := peer.Subscribe(router); err != nil {
				slog.Warn("failed to subscribe presenter to relay track", "user_id", uid, "track_id", router.ID, "room_id", r.ID, "error", err)
			}
		}
		for uid, peer := range r.viewers {
			if err := peer.Subscribe(router); err != nil {
				slog.Warn("failed to subscribe viewer to relay track", "user_id", uid, "track_id", router.ID, "room_id", r.ID, "error", err)
			}
		}
	}
	r.mu.Unlock()
	return router.WriteRTP(packet)
}

func (r *Room) injectRelaySimulcastPacket(desc RelayTrackDescriptor, packet *rtp.Packet) error {
	quality := QualityLayer(desc.Layer)
	if quality == "" {
		quality = QualityHigh
	}

	r.mu.Lock()
	st, exists := r.simulcastTracks[desc.StreamID]
	if !exists {
		st = NewSimulcastTrack(desc.StreamID, desc.SourcePeer)
		r.simulcastTracks[desc.StreamID] = st
	}
	hasLayer := st.HasLayer(quality)
	r.mu.Unlock()

	if !hasLayer {
		if err := st.AddInjectedLayer(quality, desc.TrackID, desc.MimeType); err != nil {
			return err
		}
		if observed, ok := st.ObservedTrack(quality); ok {
			r.mu.Lock()
			r.attachObserversToTrackLocked(*observed, func(sink PacketSink) {
				_ = st.AddSink(quality, sink)
			})
			if !exists {
				for uid, peer := range r.presenters {
					if uid == desc.SourcePeer {
						continue
					}
					if err := r.subscribeToSimulcastLocked(peer, st, quality); err != nil {
						slog.Warn("failed to subscribe presenter to relay simulcast", "user_id", uid, "stream_id", desc.StreamID, "room_id", r.ID, "error", err)
					}
				}
				for uid, peer := range r.viewers {
					if err := r.subscribeToSimulcastLocked(peer, st, quality); err != nil {
						slog.Warn("failed to subscribe viewer to relay simulcast", "user_id", uid, "stream_id", desc.StreamID, "room_id", r.ID, "error", err)
					}
				}
			}
			r.mu.Unlock()
		}
	}

	return st.WriteRTP(quality, packet)
}

func (r *Room) canPublishTrack(userID, trackID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.canPublishTrackLocked(userID, trackID)
}

func (r *Room) canPublishTrackLocked(userID, trackID string) bool {
	if r.options.MaxTracksPerPresenter <= 0 {
		return true
	}
	if router, ok := r.tracks[trackID]; ok && router.SourcePeer == userID {
		return true
	}
	if st, ok := r.simulcastTracks[trackID]; ok && st.SourcePeer == userID {
		return true
	}
	return r.publisherTrackCountLocked(userID) < r.options.MaxTracksPerPresenter
}

func (r *Room) publisherTrackCountLocked(userID string) int {
	count := 0
	for _, router := range r.tracks {
		if router.SourcePeer == userID {
			count++
		}
	}
	for _, st := range r.simulcastTracks {
		if st.SourcePeer == userID {
			count++
		}
	}
	return count
}

// subscribeToSimulcastLocked subscribes a peer to a simulcast track at the
// given quality layer. Must be called under r.mu lock.
func (r *Room) subscribeToSimulcastLocked(peer *Peer, st *SimulcastTrack, quality QualityLayer) error {
	localTrack := st.LocalTrackForLayer(quality)
	if localTrack == nil {
		return fmt.Errorf("layer %s not available", quality)
	}

	sender, err := peer.PC.AddTrack(localTrack)
	if err != nil {
		return err
	}

	peer.mu.Lock()
	peer.subscriptions[st.StreamID] = sender
	peer.mu.Unlock()

	st.SetSubscriberLayer(peer.UserID, quality)

	reliability.SafeGo("sfu_room_rtcp_consume", func() { consumeSenderRTCP(sender) })

	return nil
}

// SetSubscriberQuality switches a subscriber to a different simulcast quality
// layer for a specific stream. Existing senders use ReplaceTrack so normal
// adaptive switches avoid SDP renegotiation on the client side.
func (r *Room) SetSubscriberQuality(userID, streamID string, quality QualityLayer) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	st, ok := r.simulcastTracks[streamID]
	if !ok {
		return fmt.Errorf("simulcast track %s not found", streamID)
	}

	// Resolve the best available layer.
	actual := st.BestAvailableLayer(quality)

	// Check if already on this layer.
	current, exists := st.SubscriberLayer(userID)
	if exists && current == actual {
		return nil
	}

	peer, _ := r.findPeerLocked(userID)
	if peer == nil {
		return fmt.Errorf("peer %s not found", userID)
	}

	localTrack := st.LocalTrackForLayer(actual)
	if localTrack == nil {
		return fmt.Errorf("layer %s not available for stream %s", actual, streamID)
	}

	peer.mu.Lock()
	sender, exists := peer.subscriptions[streamID]
	if exists {
		if err := sender.ReplaceTrack(localTrack); err != nil {
			peer.mu.Unlock()
			return fmt.Errorf("replace track for layer %s: %w", actual, err)
		}
	} else {
		var err error
		sender, err = peer.PC.AddTrack(localTrack)
		if err != nil {
			peer.mu.Unlock()
			return fmt.Errorf("add track for layer %s: %w", actual, err)
		}
		peer.subscriptions[streamID] = sender
		reliability.SafeGo("sfu_room_rtcp_consume", func() { consumeSenderRTCP(sender) })
	}
	peer.mu.Unlock()

	st.SetSubscriberLayer(userID, actual)

	slog.Info("subscriber quality switched",
		"user_id", userID,
		"stream_id", streamID,
		"from", current,
		"to", actual,
		"room_id", r.ID,
	)

	return nil
}

func (r *Room) attachObserversToTrackLocked(track ObservedTrack, addSink func(PacketSink)) {
	for _, observer := range r.observers {
		r.attachObserverToTrackLocked(observer, track, addSink)
	}
}

func (r *Room) attachObserverToTrackLocked(observer TrackObserver, track ObservedTrack, addSink func(PacketSink)) {
	if observer == nil || addSink == nil {
		return
	}
	sink, err := observer.OnTrack(track)
	if err != nil {
		slog.Warn("failed to attach track observer",
			"track_id", track.TrackID,
			"stream_id", track.StreamID,
			"layer", track.Layer,
			"error", err,
		)
		return
	}
	if sink != nil {
		addSink(sink)
	}
}

// PlanSubscriberAdaptation ingests current network and device health for a
// subscriber and returns the best next quality decision without applying it.
func (r *Room) PlanSubscriberAdaptation(sample NetworkSample) (AdaptiveDecision, error) {
	if sample.UserID == "" {
		return AdaptiveDecision{}, errors.New("user_id is required")
	}
	r.mu.RLock()
	streamID := sample.StreamID
	if streamID == "" && len(r.simulcastTracks) == 1 {
		for id := range r.simulcastTracks {
			streamID = id
		}
	}
	st, ok := r.simulcastTracks[streamID]
	if !ok {
		r.mu.RUnlock()
		return AdaptiveDecision{}, fmt.Errorf("simulcast track %s not found", streamID)
	}
	if peer, _ := r.findPeerLocked(sample.UserID); peer == nil {
		r.mu.RUnlock()
		return AdaptiveDecision{}, fmt.Errorf("peer %s not found", sample.UserID)
	}
	current, ok := st.SubscriberLayer(sample.UserID)
	if !ok {
		current = QualityMedium
	}
	available := st.AvailableLayers()
	r.mu.RUnlock()

	sample.StreamID = streamID
	decision := r.adaptive.Decide(sample, current, available)
	return decision, nil
}

// ApplyAdaptiveDecision applies a previously calculated quality decision.
func (r *Room) ApplyAdaptiveDecision(decision AdaptiveDecision) error {
	if decision.StreamID == "" || decision.UserID == "" {
		return errors.New("user_id and stream_id are required")
	}
	if !decision.Changed && !decision.VideoSuspended {
		return nil
	}
	if err := r.SetSubscriberQuality(decision.UserID, decision.StreamID, decision.TargetQuality); err != nil {
		return err
	}
	return nil
}

// AdaptSubscriber ingests current network and device health for a subscriber,
// decides the best layer, and applies the layer if needed.
func (r *Room) AdaptSubscriber(sample NetworkSample) (AdaptiveDecision, error) {
	decision, err := r.PlanSubscriberAdaptation(sample)
	if err != nil {
		return AdaptiveDecision{}, err
	}
	if err := r.ApplyAdaptiveDecision(decision); err != nil {
		return decision, err
	}
	return decision, nil
}

func looksLikeScreenShare(streamID string) bool {
	normalized := strings.ToLower(strings.TrimSpace(streamID))
	switch {
	case normalized == "":
		return false
	case strings.Contains(normalized, "screen"),
		strings.Contains(normalized, "share"),
		strings.Contains(normalized, "display"),
		strings.Contains(normalized, "present"):
		return true
	default:
		return false
	}
}

// subscribePeerToExistingTracks adds all current track routers (both regular
// and simulcast) to a peer's PeerConnection. Called under r.mu write lock.
func (r *Room) subscribePeerToExistingTracks(peer *Peer) {
	// Subscribe to regular tracks.
	for _, router := range r.tracks {
		if peer.Role == PeerRolePresenter && router.SourcePeer == peer.UserID {
			continue
		}
		if err := peer.Subscribe(router); err != nil {
			slog.Warn("failed to subscribe new peer to existing track",
				"user_id", peer.UserID,
				"track_id", router.ID,
				"room_id", r.ID,
				"error", err,
			)
		}
	}

	// Subscribe to simulcast tracks at the best available layer.
	// Viewers default to medium quality; presenters get high quality.
	defaultQuality := QualityMedium
	if peer.Role == PeerRolePresenter {
		defaultQuality = QualityHigh
	}
	for _, st := range r.simulcastTracks {
		if peer.Role == PeerRolePresenter && st.SourcePeer == peer.UserID {
			continue
		}
		actual := st.BestAvailableLayer(defaultQuality)
		if err := r.subscribeToSimulcastLocked(peer, st, actual); err != nil {
			slog.Warn("failed to subscribe new peer to simulcast track",
				"user_id", peer.UserID,
				"stream_id", st.StreamID,
				"room_id", r.ID,
				"error", err,
			)
		}
	}
}

// removePresenterTracksLocked closes all TrackRouters and SimulcastTracks
// owned by the given presenter and unsubscribes all peers.
// Must be called under r.mu write lock.
func (r *Room) removePresenterTracksLocked(userID string) {
	// Remove regular tracks.
	for trackID, router := range r.tracks {
		if router.SourcePeer != userID {
			continue
		}

		router.Close()
		delete(r.tracks, trackID)

		for _, p := range r.presenters {
			if err := p.Unsubscribe(trackID); err != nil {
				slog.Warn("failed to unsubscribe presenter from removed track", "room_id", r.ID, "subscriber_id", p.UserID, "track_id", trackID, "error", err)
			}
		}
		for _, p := range r.viewers {
			if err := p.Unsubscribe(trackID); err != nil {
				slog.Warn("failed to unsubscribe viewer from removed track", "room_id", r.ID, "subscriber_id", p.UserID, "track_id", trackID, "error", err)
			}
		}

		r.emitEvent(RoomEvent{
			Type:    EventTrackUnpublished,
			RoomID:  r.ID,
			UserID:  userID,
			TrackID: trackID,
			Role:    PeerRolePresenter,
		})
	}

	// Remove simulcast tracks.
	for streamID, st := range r.simulcastTracks {
		if st.SourcePeer != userID {
			continue
		}

		st.Close()
		delete(r.simulcastTracks, streamID)
		r.adaptive.ForgetStream(streamID)

		for _, p := range r.presenters {
			if err := p.Unsubscribe(streamID); err != nil {
				slog.Warn("failed to unsubscribe presenter from removed simulcast stream", "room_id", r.ID, "subscriber_id", p.UserID, "stream_id", streamID, "error", err)
			}
		}
		for _, p := range r.viewers {
			if err := p.Unsubscribe(streamID); err != nil {
				slog.Warn("failed to unsubscribe viewer from removed simulcast stream", "room_id", r.ID, "subscriber_id", p.UserID, "stream_id", streamID, "error", err)
			}
		}

		r.emitEvent(RoomEvent{
			Type:    EventTrackUnpublished,
			RoomID:  r.ID,
			UserID:  userID,
			TrackID: streamID,
			Role:    PeerRolePresenter,
		})
	}
}

// removeViewerSubscriptionsLocked removes a viewer from all track routers'
// and simulcast tracks' subscriber lists. Must be called under r.mu write lock.
func (r *Room) removeViewerSubscriptionsLocked(userID string) {
	for _, router := range r.tracks {
		router.RemoveSubscriber(userID)
	}
	for _, st := range r.simulcastTracks {
		st.RemoveSubscriber(userID)
	}
}

// findPeerLocked returns the peer and their role, or nil if not found.
// Must be called under r.mu lock (read or write).
func (r *Room) findPeerLocked(userID string) (*Peer, PeerRole) {
	if p, ok := r.presenters[userID]; ok {
		return p, PeerRolePresenter
	}
	if p, ok := r.viewers[userID]; ok {
		return p, PeerRoleViewer
	}
	return nil, ""
}

// emitEvent sends a room event to the registered handler, if any.
func (r *Room) emitEvent(evt RoomEvent) {
	if r.onEvent != nil {
		r.onEvent(evt)
	}
}

func consumeSenderRTCP(sender *webrtc.RTPSender) {
	buf := make([]byte, 1500)
	for {
		if _, _, err := sender.Read(buf); err != nil {
			return
		}
	}
}
