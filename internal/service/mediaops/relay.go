package mediaops

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pion/rtp"

	"aloqa/internal/domain/entity"
	"aloqa/internal/media/sfu"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/platform/reliability"
)

const (
	relayHeaderCallID      = "x-aloqa-call-id"
	relayHeaderWorkspaceID = "x-aloqa-workspace-id"
	relayHeaderSourceNode  = "x-aloqa-source-node"
	relayHeaderSourcePeer  = "x-aloqa-source-peer"
	relayHeaderStreamID    = "x-aloqa-stream-id"
	relayHeaderTrackID     = "x-aloqa-track-id"
	relayHeaderLayer       = "x-aloqa-layer"
	relayHeaderMimeType    = "x-aloqa-mime-type"
	relayHeaderSignature   = "x-aloqa-relay-sig"
)

// signRelayHeaders computes an HMAC-SHA256 over the relay headers to prevent
// packet injection from untrusted sources. The signature covers call ID,
// workspace ID, source node, and track ID.
func signRelayHeaders(secret string, headers map[string]string) string {
	if secret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(headers[relayHeaderCallID]))
	mac.Write([]byte(headers[relayHeaderWorkspaceID]))
	mac.Write([]byte(headers[relayHeaderSourceNode]))
	mac.Write([]byte(headers[relayHeaderTrackID]))
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyRelaySignature checks that the HMAC in the relay headers matches the
// expected value. Returns true if the secret is empty (signing disabled) or
// if the signature is valid.
func verifyRelaySignature(secret string, headers map[string]string) bool {
	if secret == "" {
		return true
	}
	expected := signRelayHeaders(secret, headers)
	actual := headers[relayHeaderSignature]
	return hmac.Equal([]byte(expected), []byte(actual))
}

func (s *Service) ResolveParticipantPlacement(ctx context.Context, call *entity.Call, participant *entity.CallParticipant, preferredNodeID string) (*entity.MediaRoomPlacement, error) {
	placement, err := s.EnsurePlacement(ctx, call, sfu.RoomOptions{})
	if err != nil || placement == nil || participant == nil || s.repo == nil {
		return placement, err
	}
	if participant.Role != entity.CallRoleViewer || placement.FanoutStrategy == entity.MediaFanoutSingleNode {
		return placement, nil
	}

	edges, err := s.repo.ListRelayEdgesByCall(ctx, call.ID)
	if err != nil {
		return nil, cerrors.Internal("failed to resolve media relay edges", err)
	}
	active := make([]entity.MediaRelayEdge, 0, len(edges))
	for _, edge := range edges {
		if edge.Status == entity.MediaRelayStatusActive && edge.RoleScope == entity.MediaRelayRoleScopeViewers {
			active = append(active, edge)
		}
	}
	if len(active) == 0 {
		return placement, nil
	}
	if preferredNodeID != "" {
		for _, edge := range active {
			if edge.TargetNodeID == preferredNodeID {
				return edgeAsPlacement(placement, edge), nil
			}
		}
	}

	sameRegion := make([]entity.MediaRelayEdge, 0, len(active))
	for _, edge := range active {
		if edge.TargetRegion == s.config.Region {
			sameRegion = append(sameRegion, edge)
		}
	}
	pool := active
	if len(sameRegion) > 0 {
		pool = sameRegion
	}
	sort.Slice(pool, func(i, j int) bool {
		if pool[i].Priority == pool[j].Priority {
			return pool[i].TargetNodeID < pool[j].TargetNodeID
		}
		return pool[i].Priority < pool[j].Priority
	})
	index := int(crc32.ChecksumIEEE([]byte(participant.UserID.String())) % uint32(len(pool)))
	return edgeAsPlacement(placement, pool[index]), nil
}

func (s *Service) CanServeNode(ctx context.Context, call *entity.Call, nodeID string) (bool, error) {
	if call == nil {
		return false, cerrors.InvalidInput("call is required")
	}
	placement, err := s.EnsurePlacement(ctx, call, sfu.RoomOptions{})
	if err != nil {
		return false, err
	}
	if placement == nil {
		return true, nil
	}
	if placement.NodeID == nodeID {
		return true, nil
	}
	if s.repo == nil {
		return false, nil
	}
	edges, err := s.repo.ListRelayEdgesByCall(ctx, call.ID)
	if err != nil {
		return false, cerrors.Internal("failed to load media relay edges", err)
	}
	for _, edge := range edges {
		if edge.Status == entity.MediaRelayStatusActive && edge.TargetNodeID == nodeID {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) RunRelayFabric(ctx context.Context) {
	if s == nil || s.sfu == nil || s.repo == nil || s.relay == nil {
		return
	}
	subject := relaySubjectWildcard(s.config.NodeID)
	sub, err := s.relay.SubscribeTransient(subject, func(data []byte, _ string, headers map[string]string) {
		s.handleRelayPacket(data, headers)
	})
	if err != nil {
		slog.ErrorContext(ctx, "failed to subscribe relay fabric", "node_id", s.config.NodeID, "error", err)
		return
	}
	s.mu.Lock()
	s.relaySub = sub
	s.mu.Unlock()
	defer func() {
		if sub != nil {
			_ = sub.Close()
		}
	}()

	s.reconcileRelayObservers(ctx)
	ticker := time.NewTicker(s.config.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.observer != nil {
				s.observer.RecordWorkerHeartbeat("media_relay_fabric")
			}
			opCtx, cancel := reliability.WithTimeout(ctx, s.config.OperationTimeout)
			s.reconcileRelayObservers(opCtx)
			cancel()
		}
	}
}

func (s *Service) reconcileRelayEdges(ctx context.Context, call *entity.Call, placement *entity.MediaRoomPlacement, nodes []entity.MediaNodeSnapshot, policy entity.MediaCallPolicy) error {
	if s.repo == nil || call == nil || placement == nil {
		return nil
	}
	edges, relayMeta, err := s.buildRelayEdges(call, placement, nodes, policy)
	if err != nil {
		return err
	}
	if placement.Metadata == nil {
		placement.Metadata = map[string]any{}
	}
	for key, value := range relayMeta {
		placement.Metadata[key] = value
	}
	if err := s.repo.UpsertPlacement(ctx, placement); err != nil {
		return cerrors.Internal("failed to refresh media placement", err)
	}
	if err := s.repo.ReplaceRelayEdges(ctx, call.ID, edges); err != nil {
		return cerrors.Internal("failed to refresh media relay edges", err)
	}
	return nil
}

func (s *Service) buildRelayEdges(call *entity.Call, placement *entity.MediaRoomPlacement, nodes []entity.MediaNodeSnapshot, policy entity.MediaCallPolicy) ([]entity.MediaRelayEdge, map[string]any, error) {
	meta := map[string]any{
		"relay_edge_count": 0,
	}
	if call == nil || placement == nil || s.repo == nil || policy.FanoutStrategy == entity.MediaFanoutSingleNode {
		return nil, meta, nil
	}

	candidates := make([]entity.MediaNodeSnapshot, 0, len(nodes))
	for _, node := range s.filterAvailableNodes(nodes) {
		if node.NodeID == placement.NodeID {
			continue
		}
		candidates = append(candidates, node)
	}
	if len(candidates) == 0 {
		return nil, meta, nil
	}

	selected := selectRelayNodes(candidates, placement.Region, policy, s.config.WebinarFanoutThreshold)
	now := time.Now().UTC()
	edges := make([]entity.MediaRelayEdge, 0, len(selected))
	for idx, node := range selected {
		edgeMeta := map[string]any{
			"overflow": node.Region != placement.Region,
		}
		edges = append(edges, entity.MediaRelayEdge{
			CallID:          call.ID,
			WorkspaceID:     call.WorkspaceID,
			SourceNodeID:    placement.NodeID,
			TargetNodeID:    node.NodeID,
			TargetRegion:    node.Region,
			ControlURL:      node.ControlURL,
			MediaURL:        node.MediaURL,
			FanoutStrategy:  policy.FanoutStrategy,
			RoleScope:       entity.MediaRelayRoleScopeViewers,
			Status:          entity.MediaRelayStatusActive,
			Sticky:          true,
			MaxParticipants: relayCapacity(policy, s.config.WebinarFanoutThreshold),
			Priority:        idx,
			Metadata:        edgeMeta,
			AssignedAt:      now,
			UpdatedAt:       now,
		})
	}
	meta["relay_edge_count"] = len(edges)
	meta["relay_strategy"] = string(policy.FanoutStrategy)
	return edges, meta, nil
}

func selectRelayNodes(candidates []entity.MediaNodeSnapshot, sourceRegion string, policy entity.MediaCallPolicy, threshold int) []entity.MediaNodeSnapshot {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].LoadScore == candidates[j].LoadScore {
			return candidates[i].NodeID < candidates[j].NodeID
		}
		return candidates[i].LoadScore < candidates[j].LoadScore
	})

	want := 0
	switch policy.FanoutStrategy {
	case entity.MediaFanoutRegionalCascade:
		seen := map[string]struct{}{}
		for _, node := range candidates {
			if node.Region == sourceRegion {
				continue
			}
			if _, ok := seen[node.Region]; ok {
				continue
			}
			seen[node.Region] = struct{}{}
			want++
		}
		if want == 0 && len(candidates) > 0 {
			want = 1
		}
	default:
		audience := policy.MaxViewers
		if audience <= 0 {
			audience = policy.MaxParticipants
		}
		if threshold <= 0 {
			threshold = 1500
		}
		want = maxInt(1, int((audience+threshold-1)/threshold)-1)
	}
	if want > len(candidates) {
		want = len(candidates)
	}
	if want <= 0 {
		return nil
	}

	selected := make([]entity.MediaNodeSnapshot, 0, want)
	seenRegions := map[string]struct{}{}
	for _, node := range candidates {
		if len(selected) >= want {
			break
		}
		if _, ok := seenRegions[node.Region]; ok && policy.FanoutStrategy == entity.MediaFanoutRegionalCascade {
			continue
		}
		selected = append(selected, node)
		seenRegions[node.Region] = struct{}{}
	}
	if len(selected) < want {
		chosen := map[string]struct{}{}
		for _, node := range selected {
			chosen[node.NodeID] = struct{}{}
		}
		for _, node := range candidates {
			if len(selected) >= want {
				break
			}
			if _, ok := chosen[node.NodeID]; ok {
				continue
			}
			selected = append(selected, node)
		}
	}
	return selected
}

func relayCapacity(policy entity.MediaCallPolicy, threshold int) int {
	if threshold <= 0 {
		threshold = 1500
	}
	if policy.MaxViewers > 0 && policy.MaxViewers < threshold {
		return policy.MaxViewers
	}
	return threshold
}

func edgeAsPlacement(base *entity.MediaRoomPlacement, edge entity.MediaRelayEdge) *entity.MediaRoomPlacement {
	if base == nil {
		return nil
	}
	placement := *base
	placement.NodeID = edge.TargetNodeID
	placement.Region = edge.TargetRegion
	placement.ControlURL = edge.ControlURL
	placement.MediaURL = edge.MediaURL
	placement.Sticky = edge.Sticky
	placement.Metadata = cloneMap(base.Metadata)
	placement.Metadata["relay"] = true
	placement.Metadata["relay_source_node_id"] = edge.SourceNodeID
	placement.Metadata["relay_priority"] = edge.Priority
	return &placement
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func relaySubject(targetNodeID string, callID uuid.UUID) string {
	return fmt.Sprintf("aloqa.media.relay.%s.%s", targetNodeID, callID)
}

func relaySubjectWildcard(targetNodeID string) string {
	return fmt.Sprintf("aloqa.media.relay.%s.*", targetNodeID)
}

type relayObserver struct {
	transport   RelayTransport
	callID      uuid.UUID
	workspaceID uuid.UUID
	sourceNode  string
	targetNode  string
	relaySecret string
}

func (o relayObserver) OnTrack(track sfu.ObservedTrack) (sfu.PacketSink, error) {
	headers := map[string]string{
		relayHeaderCallID:      o.callID.String(),
		relayHeaderWorkspaceID: o.workspaceID.String(),
		relayHeaderSourceNode:  o.sourceNode,
		relayHeaderSourcePeer:  track.SourcePeer,
		relayHeaderStreamID:    track.StreamID,
		relayHeaderTrackID:     track.TrackID,
		relayHeaderLayer:       track.Layer,
		relayHeaderMimeType:    track.MimeType,
	}
	// Sign headers so the receiving node can verify the packet origin.
	if sig := signRelayHeaders(o.relaySecret, headers); sig != "" {
		headers[relayHeaderSignature] = sig
	}
	return &relayPacketSink{
		transport: o.transport,
		subject:   relaySubject(o.targetNode, o.callID),
		headers:   headers,
	}, nil
}

type relayPacketSink struct {
	transport RelayTransport
	subject   string
	headers   map[string]string
}

func (s *relayPacketSink) WriteRTP(packet *rtp.Packet) error {
	if s.transport == nil || packet == nil {
		return nil
	}
	raw, err := packet.Marshal()
	if err != nil {
		return err
	}
	return s.transport.PublishTransient(context.Background(), s.subject, raw, s.headers)
}

func (s *relayPacketSink) Close() error { return nil }

func (s *Service) reconcileRelayObservers(ctx context.Context) {
	if s.relay == nil || s.repo == nil || s.sfu == nil {
		return
	}
	desired := map[string]relayObserver{}
	for _, room := range s.sfu.Rooms() {
		if room == nil {
			continue
		}
		callID, err := uuid.Parse(room.ID)
		if err != nil {
			continue
		}
		placement, err := s.loadPlacement(ctx, callID)
		if err != nil || placement == nil || placement.NodeID != s.config.NodeID {
			continue
		}
		edges, err := s.repo.ListRelayEdgesByCall(ctx, callID)
		if err != nil {
			continue
		}
		for _, edge := range edges {
			if edge.Status != entity.MediaRelayStatusActive {
				continue
			}
			key := callID.String() + ":" + edge.TargetNodeID
			desired[key] = relayObserver{
				transport:   s.relay,
				callID:      callID,
				workspaceID: placement.WorkspaceID,
				sourceNode:  s.config.NodeID,
				targetNode:  edge.TargetNodeID,
				relaySecret: s.config.RelaySecret,
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for key, observerID := range s.relayObservers {
		if _, ok := desired[key]; ok {
			continue
		}
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			delete(s.relayObservers, key)
			continue
		}
		if room, ok := s.sfu.GetRoom(parts[0]); ok {
			room.RemoveObserver(observerID)
		}
		delete(s.relayObservers, key)
	}
	for key, observer := range desired {
		if _, ok := s.relayObservers[key]; ok {
			continue
		}
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}
		room, ok := s.sfu.GetRoom(parts[0])
		if !ok {
			continue
		}
		if observerID := room.AddObserver(observer); observerID != "" {
			s.relayObservers[key] = observerID
		}
	}
}

func (s *Service) handleRelayPacket(data []byte, headers map[string]string) {
	// Verify HMAC signature before processing any relay packet.
	if !verifyRelaySignature(s.config.RelaySecret, headers) {
		slog.Warn("rejected relay packet with invalid signature",
			"source_node", headers[relayHeaderSourceNode],
			"call_id", headers[relayHeaderCallID])
		return
	}

	callID, err := uuid.Parse(strings.TrimSpace(headers[relayHeaderCallID]))
	if err != nil {
		return
	}
	if headers[relayHeaderSourceNode] == s.config.NodeID {
		return
	}
	packet := &rtp.Packet{}
	if err := packet.Unmarshal(data); err != nil {
		return
	}
	placement, err := s.loadPlacement(context.Background(), callID)
	if err != nil || placement == nil {
		return
	}
	room, err := s.ensureRelayRoom(placement)
	if err != nil {
		slog.Warn("failed to ensure relay room", "call_id", callID, "error", err)
		return
	}
	desc := sfu.RelayTrackDescriptor{
		TrackID:    headers[relayHeaderTrackID],
		StreamID:   headers[relayHeaderStreamID],
		SourcePeer: headers[relayHeaderSourcePeer],
		MimeType:   headers[relayHeaderMimeType],
		Layer:      headers[relayHeaderLayer],
	}
	if err := room.InjectRelayPacket(desc, packet); err != nil && err != io.ErrClosedPipe {
		slog.Warn("failed to inject relay packet", "call_id", callID, "track_id", desc.TrackID, "error", err)
	}
}

func (s *Service) ensureRelayRoom(placement *entity.MediaRoomPlacement) (*sfu.Room, error) {
	if placement == nil || s.sfu == nil {
		return nil, cerrors.Unavailable("relay room is not available")
	}
	if room, ok := s.sfu.GetRoom(placement.CallID.String()); ok {
		return room, nil
	}
	room, err := s.sfu.CreateRoom(placement.CallID.String(), sfu.RoomOptions{
		MaxPresenters:         maxInt(placement.MaxPresenters, 1),
		MaxViewers:            maxInt(placement.MaxViewers, maxInt(placement.MaxParticipants, 1)),
		MaxTracksPerPresenter: sfu.DefaultMaxTracks,
		Simulcast:             true,
	})
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return nil, err
	}
	if room != nil {
		return room, nil
	}
	if room, ok := s.sfu.GetRoom(placement.CallID.String()); ok {
		return room, nil
	}
	return nil, cerrors.Unavailable("relay room is not available")
}
