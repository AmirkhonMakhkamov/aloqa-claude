package mediaops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/domain/repository"
	"aloqa/internal/media/sfu"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/platform/reliability"
)

const mediaNodeSetKey = "media:nodes:active"

type Config struct {
	NodeID                  string
	Region                  string
	ControlURL              string
	MediaURL                string
	RelaySecret             string // Shared secret for HMAC signing relay packets between nodes
	HeartbeatInterval       time.Duration
	NodeTTL                 time.Duration
	TelemetryInterval       time.Duration
	OneToOneParticipantCap  int
	GroupParticipantCap     int
	MeetingParticipantCap   int
	WebinarParticipantCap   int
	SelectorParticipantCap  int
	WebinarPresenterCap     int
	SelectorPresenterCap    int
	WebinarFanoutThreshold  int
	MaxRoomsPerNode         int
	TURNStrategy            string
	DefaultQualityMode      entity.MediaQualityPolicyMode
	AlertPacketLossPct      float64
	AlertJitterMs           float64
	AlertRoundTripTimeMs    float64
	CorrelationTolerancePct float64
	CorrelationToleranceMs  float64
	ServerDrivenEnabled     bool
	ServerDrivenMinInterval time.Duration
	OperationTimeout        time.Duration
}

type EventPublisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

type RelayTransport interface {
	PublishTransient(ctx context.Context, subject string, data []byte, headers map[string]string) error
	SubscribeTransient(subject string, handler func(data []byte, subject string, headers map[string]string)) (io.Closer, error)
}

type Service struct {
	repo      repository.MediaRepository
	rdb       *redis.Client
	sfu       *sfu.SFU
	publisher EventPublisher
	relay     RelayTransport
	config    Config

	mu                sync.Mutex
	telemetryCounters map[string]telemetryCounterState
	publishedDecision map[string]publishedQualityDecision
	relayObservers    map[string]string
	relaySub          io.Closer
	observer          interface {
		RecordCallQualityAggregate(workspaceID, callID uuid.UUID, server, client entity.MediaQoSSummary, degraded, mismatch bool)
		RecordWorkerHeartbeat(name string)
	}
}

type telemetryCounterState struct {
	BytesReceived  int64
	FramesReceived int64
	FramesDropped  int64
	FreezeCount    int64
	NACKCount      int64
	PLICount       int64
	SampledAt      time.Time
}

type publishedQualityDecision struct {
	TargetQuality       sfu.QualityLayer
	NetworkGrade        sfu.NetworkGrade
	AudioPriority       bool
	VideoSuspended      bool
	MaxVideoBitrateKbps int
	MaxVideoFPS         int
	UpdatedAt           time.Time
}

func NewService(repo repository.MediaRepository, rdb *redis.Client, sfuServer *sfu.SFU, cfg Config) *Service {
	if cfg.NodeID == "" {
		if hostname, err := os.Hostname(); err == nil && hostname != "" {
			cfg.NodeID = hostname
		} else {
			cfg.NodeID = "media-node-local"
		}
	}
	if cfg.Region == "" {
		cfg.Region = "global"
	}
	if cfg.ControlURL == "" {
		cfg.ControlURL = "local"
	}
	if cfg.MediaURL == "" {
		cfg.MediaURL = cfg.ControlURL
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 3 * time.Second
	}
	if cfg.NodeTTL <= 0 {
		cfg.NodeTTL = 10 * time.Second
	}
	if cfg.TelemetryInterval <= 0 {
		cfg.TelemetryInterval = 5 * time.Second
	}
	if cfg.OneToOneParticipantCap <= 0 {
		cfg.OneToOneParticipantCap = 2
	}
	if cfg.GroupParticipantCap <= 0 {
		cfg.GroupParticipantCap = 32
	}
	if cfg.MeetingParticipantCap <= 0 {
		cfg.MeetingParticipantCap = 500
	}
	if cfg.WebinarParticipantCap <= 0 {
		cfg.WebinarParticipantCap = 10000
	}
	if cfg.SelectorParticipantCap <= 0 {
		cfg.SelectorParticipantCap = cfg.WebinarParticipantCap
	}
	if cfg.WebinarPresenterCap <= 0 {
		cfg.WebinarPresenterCap = 50
	}
	if cfg.SelectorPresenterCap <= 0 {
		cfg.SelectorPresenterCap = 12
	}
	if cfg.WebinarFanoutThreshold <= 0 {
		cfg.WebinarFanoutThreshold = 1500
	}
	if cfg.TURNStrategy == "" {
		cfg.TURNStrategy = "regional_turn_pool"
	}
	if cfg.DefaultQualityMode == "" {
		cfg.DefaultQualityMode = entity.MediaQualityPolicyAuto
	}
	if cfg.AlertPacketLossPct <= 0 {
		cfg.AlertPacketLossPct = 5
	}
	if cfg.AlertJitterMs <= 0 {
		cfg.AlertJitterMs = 70
	}
	if cfg.AlertRoundTripTimeMs <= 0 {
		cfg.AlertRoundTripTimeMs = 400
	}
	if cfg.CorrelationTolerancePct <= 0 {
		cfg.CorrelationTolerancePct = 8
	}
	if cfg.CorrelationToleranceMs <= 0 {
		cfg.CorrelationToleranceMs = 80
	}
	if cfg.ServerDrivenMinInterval <= 0 {
		cfg.ServerDrivenMinInterval = 4 * time.Second
	}
	if cfg.OperationTimeout <= 0 {
		cfg.OperationTimeout = 5 * time.Second
	}
	return &Service{
		repo:              repo,
		rdb:               rdb,
		sfu:               sfuServer,
		config:            cfg,
		telemetryCounters: make(map[string]telemetryCounterState),
		publishedDecision: make(map[string]publishedQualityDecision),
		relayObservers:    make(map[string]string),
	}
}

func (s *Service) SetEventPublisher(publisher EventPublisher) {
	s.publisher = publisher
}

func (s *Service) SetRelayTransport(relay RelayTransport) {
	s.relay = relay
}

func (s *Service) SetObserver(observer interface {
	RecordCallQualityAggregate(workspaceID, callID uuid.UUID, server, client entity.MediaQoSSummary, degraded, mismatch bool)
	RecordWorkerHeartbeat(name string)
}) {
	s.observer = observer
}

func (s *Service) LocalNodeID() string {
	return s.config.NodeID
}

func (s *Service) IsLocalNode(nodeID string) bool {
	return strings.TrimSpace(nodeID) == s.config.NodeID
}

func (s *Service) PolicyForCall(call *entity.Call) entity.MediaCallPolicy {
	if call == nil {
		return entity.MediaCallPolicy{}
	}

	hardCap := s.participantCapByType(call.Type)
	maxParticipants := hardCap
	if requested := call.Settings.MaxParticipants; requested > 0 && (hardCap == 0 || requested < hardCap) {
		maxParticipants = requested
	}
	if maxParticipants <= 0 {
		maxParticipants = hardCap
	}

	policy := entity.MediaCallPolicy{
		MaxParticipants: maxParticipants,
		RoutingMode:     entity.MediaRoutingStickyEdge,
		TURNStrategy:    s.config.TURNStrategy,
		Sticky:          true,
	}

	switch call.Type {
	case entity.CallTypeOneToOne:
		policy.MaxPresenters = minPositive(maxParticipants, 2)
		policy.MaxViewers = 0
		policy.FanoutStrategy = entity.MediaFanoutSingleNode
		policy.OverflowPolicy = entity.MediaOverflowReject
		policy.ScreenSharePriority = entity.MediaScreenShareBalanced
	case entity.CallTypeGroup:
		policy.MaxPresenters = minPositive(maxParticipants, 8)
		policy.MaxViewers = 0
		policy.FanoutStrategy = entity.MediaFanoutSingleNode
		policy.OverflowPolicy = entity.MediaOverflowReject
		policy.ScreenSharePriority = entity.MediaScreenShareBalanced
	case entity.CallTypeMeeting:
		policy.MaxPresenters = minPositive(maxParticipants, 32)
		policy.MaxViewers = 0
		policy.FanoutStrategy = entity.MediaFanoutRegionalCascade
		policy.OverflowPolicy = entity.MediaOverflowRegionalMove
		policy.ScreenSharePriority = entity.MediaScreenShareProtected
	case entity.CallTypeWebinar:
		policy.MaxPresenters = minPositive(maxParticipants, s.config.WebinarPresenterCap)
		policy.MaxViewers = maxParticipants
		policy.OverflowPolicy = entity.MediaOverflowWebinarEdge
		policy.ScreenSharePriority = entity.MediaScreenShareProtected
		if maxParticipants >= s.config.WebinarFanoutThreshold {
			policy.RoutingMode = entity.MediaRoutingRegionalEdge
			policy.FanoutStrategy = entity.MediaFanoutWebinarEdges
		} else {
			policy.FanoutStrategy = entity.MediaFanoutRegionalCascade
		}
	case entity.CallTypeSelector:
		policy.MaxPresenters = minPositive(maxParticipants, s.config.SelectorPresenterCap)
		policy.MaxViewers = maxParticipants
		policy.OverflowPolicy = entity.MediaOverflowWebinarEdge
		policy.ScreenSharePriority = entity.MediaScreenShareProtected
		if maxParticipants >= s.config.WebinarFanoutThreshold {
			policy.RoutingMode = entity.MediaRoutingRegionalEdge
			policy.FanoutStrategy = entity.MediaFanoutWebinarEdges
		} else {
			policy.FanoutStrategy = entity.MediaFanoutRegionalCascade
		}
	default:
		policy.MaxPresenters = minPositive(maxParticipants, s.config.WebinarPresenterCap)
		policy.MaxViewers = 0
		policy.FanoutStrategy = entity.MediaFanoutSingleNode
		policy.OverflowPolicy = entity.MediaOverflowReject
		policy.ScreenSharePriority = entity.MediaScreenShareBalanced
	}

	if call.Settings.ScreenSharing && policy.ScreenSharePriority == "" {
		policy.ScreenSharePriority = entity.MediaScreenShareProtected
	}
	if policy.ScreenSharePriority == "" {
		policy.ScreenSharePriority = entity.MediaScreenShareBalanced
	}

	return policy
}

func (s *Service) EnsurePlacement(ctx context.Context, call *entity.Call, _ sfu.RoomOptions) (*entity.MediaRoomPlacement, error) {
	if call == nil {
		return nil, cerrors.InvalidInput("call is required")
	}
	policy := s.PolicyForCall(call)
	nodes, err := s.ListNodes(ctx)
	if err != nil {
		return nil, cerrors.Internal("failed to list media nodes", err)
	}
	if s.repo != nil {
		existing, err := s.repo.GetPlacement(ctx, call.ID)
		if err == nil {
			if len(nodes) > 1 || s.rdb != nil {
				if err := s.reconcileRelayEdges(ctx, call, existing, nodes, policy); err != nil {
					slog.WarnContext(ctx, "failed to reconcile media relay edges", "call_id", call.ID, "error", err)
				}
			}
			return existing, nil
		}
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code != cerrors.CodeNotFound {
			return nil, err
		}
	}
	selected, err := s.selectNode(call.ID, nodes, policy)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	placement := &entity.MediaRoomPlacement{
		CallID:              call.ID,
		WorkspaceID:         call.WorkspaceID,
		NodeID:              selected.NodeID,
		Region:              selected.Region,
		ControlURL:          selected.ControlURL,
		MediaURL:            selected.MediaURL,
		RoutingMode:         policy.RoutingMode,
		FanoutStrategy:      policy.FanoutStrategy,
		OverflowPolicy:      policy.OverflowPolicy,
		ScreenSharePriority: policy.ScreenSharePriority,
		TURNStrategy:        policy.TURNStrategy,
		Sticky:              policy.Sticky,
		MaxParticipants:     policy.MaxParticipants,
		MaxPresenters:       policy.MaxPresenters,
		MaxViewers:          policy.MaxViewers,
		Metadata: map[string]any{
			"selected_via":       "consistent_hash",
			"selected_node_load": selected.LoadScore,
			"call_type":          string(call.Type),
		},
		AssignedAt: now,
		UpdatedAt:  now,
	}
	relayEdges, relayMeta, err := s.buildRelayEdges(call, placement, nodes, policy)
	if err != nil {
		return nil, err
	}
	for key, value := range relayMeta {
		placement.Metadata[key] = value
	}
	if s.repo != nil {
		if err := s.repo.UpsertPlacement(ctx, placement); err != nil {
			return nil, cerrors.Internal("failed to store media placement", err)
		}
		if err := s.repo.ReplaceRelayEdges(ctx, call.ID, relayEdges); err != nil {
			return nil, cerrors.Internal("failed to store media relay edges", err)
		}
	}
	return placement, nil
}

func (s *Service) ListNodes(ctx context.Context) ([]entity.MediaNodeSnapshot, error) {
	if s.rdb == nil {
		return []entity.MediaNodeSnapshot{s.localNodeSnapshot()}, nil
	}

	nodeIDs, err := s.rdb.SMembers(ctx, mediaNodeSetKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("mediaops: list nodes: %w", err)
	}
	if len(nodeIDs) == 0 {
		return []entity.MediaNodeSnapshot{s.localNodeSnapshot()}, nil
	}

	nodes := make([]entity.MediaNodeSnapshot, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		raw, err := s.rdb.Get(ctx, mediaNodeKey(nodeID)).Bytes()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				_ = s.rdb.SRem(ctx, mediaNodeSetKey, nodeID).Err()
				continue
			}
			return nil, fmt.Errorf("mediaops: get node %s: %w", nodeID, err)
		}
		var snapshot entity.MediaNodeSnapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return nil, fmt.Errorf("mediaops: decode node %s: %w", nodeID, err)
		}
		nodes = append(nodes, snapshot)
	}
	if len(nodes) == 0 {
		return []entity.MediaNodeSnapshot{s.localNodeSnapshot()}, nil
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Region == nodes[j].Region {
			return nodes[i].NodeID < nodes[j].NodeID
		}
		return nodes[i].Region < nodes[j].Region
	})
	return nodes, nil
}

func (s *Service) GetWorkspaceTopology(ctx context.Context, workspaceID uuid.UUID) (*entity.WorkspaceMediaTopology, error) {
	nodes, err := s.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	var placements []entity.MediaRoomPlacement
	var relayEdges []entity.MediaRelayEdge
	if s.repo != nil {
		placements, err = s.repo.ListPlacementsByWorkspace(ctx, workspaceID)
		if err != nil {
			return nil, cerrors.Internal("failed to list workspace media placements", err)
		}
		relayEdges, err = s.repo.ListRelayEdgesByWorkspace(ctx, workspaceID)
		if err != nil {
			return nil, cerrors.Internal("failed to list workspace media relay edges", err)
		}
	}
	return &entity.WorkspaceMediaTopology{
		WorkspaceID: workspaceID,
		Nodes:       nodes,
		Placements:  placements,
		RelayEdges:  relayEdges,
	}, nil
}

func (s *Service) GetCallQoSHistory(ctx context.Context, workspaceID, callID uuid.UUID, limit int) (*entity.CallQoSHistory, error) {
	if s.repo == nil {
		return &entity.CallQoSHistory{
			WorkspaceID: workspaceID,
			CallID:      callID,
			Summary: entity.MediaQoSSummary{
				WorkspaceID: workspaceID,
				CallID:      callID,
			},
		}, nil
	}
	summary, err := s.repo.SummarizeQoS(ctx, workspaceID, callID)
	if err != nil {
		return nil, cerrors.Internal("failed to summarize call qos", err)
	}
	samples, err := s.repo.ListQoSSamples(ctx, workspaceID, callID, limit)
	if err != nil {
		return nil, cerrors.Internal("failed to list call qos samples", err)
	}
	return &entity.CallQoSHistory{
		WorkspaceID: workspaceID,
		CallID:      callID,
		Summary:     *summary,
		Samples:     samples,
	}, nil
}

func (s *Service) GetCallQualityPolicy(ctx context.Context, workspaceID, callID uuid.UUID) (*entity.MediaQualityPolicy, error) {
	defaultPolicy := s.defaultQualityPolicy(workspaceID, callID)
	if s.repo == nil {
		return defaultPolicy, nil
	}
	policy, err := s.repo.GetQualityPolicy(ctx, workspaceID, callID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return defaultPolicy, nil
		}
		return nil, cerrors.Internal("failed to get media quality policy", err)
	}
	return policy, nil
}

func (s *Service) UpdateCallQualityPolicy(ctx context.Context, policy *entity.MediaQualityPolicy) (*entity.MediaQualityPolicy, error) {
	if policy == nil {
		return nil, cerrors.InvalidInput("quality policy is required")
	}
	if policy.WorkspaceID == uuid.Nil || policy.CallID == uuid.Nil || policy.UpdatedBy == uuid.Nil {
		return nil, cerrors.InvalidInput("workspace_id, call_id, and updated_by are required")
	}
	normalized := s.normalizeQualityPolicy(*policy)
	if s.repo != nil {
		if err := s.repo.UpsertQualityPolicy(ctx, &normalized); err != nil {
			return nil, cerrors.Internal("failed to update media quality policy", err)
		}
	}
	return &normalized, nil
}

func (s *Service) ListQualityAlerts(ctx context.Context, workspaceID, callID uuid.UUID, limit int) ([]entity.MediaQualityAlert, error) {
	if s.repo == nil {
		return nil, nil
	}
	alerts, err := s.repo.ListQualityAlerts(ctx, workspaceID, callID, limit)
	if err != nil {
		return nil, cerrors.Internal("failed to list media quality alerts", err)
	}
	return alerts, nil
}

func (s *Service) GetCallQualityReport(ctx context.Context, workspaceID, callID uuid.UUID, limit int) (*entity.CallQualityReport, error) {
	policy, err := s.GetCallQualityPolicy(ctx, workspaceID, callID)
	if err != nil {
		return nil, err
	}
	snapshots := []entity.MediaQoSSample{}
	if s.repo != nil {
		snapshots, err = s.repo.ListQoSSamples(ctx, workspaceID, callID, limit)
		if err != nil {
			return nil, cerrors.Internal("failed to list quality snapshots", err)
		}
	}
	serverSummary := summarizeSnapshots(workspaceID, callID, filterSnapshotsBySource(snapshots, entity.MediaTelemetrySourceServer))
	clientSummary := summarizeSnapshots(workspaceID, callID, filterSnapshotsBySource(snapshots, entity.MediaTelemetrySourceClient))
	correlations := correlateSnapshots(snapshots, *policy)
	alerts, err := s.ListQualityAlerts(ctx, workspaceID, callID, 50)
	if err != nil {
		return nil, err
	}
	return &entity.CallQualityReport{
		WorkspaceID:  workspaceID,
		CallID:       callID,
		Policy:       *policy,
		Server:       serverSummary,
		Client:       clientSummary,
		Correlations: correlations,
		Alerts:       alerts,
		Snapshots:    snapshots,
	}, nil
}

func (s *Service) RecordQualitySnapshot(ctx context.Context, sample entity.MediaQoSSample) error {
	if s.repo == nil {
		return nil
	}
	if sample.ID == uuid.Nil {
		sample.ID = uuid.New()
	}
	if sample.SampledAt.IsZero() {
		sample.SampledAt = time.Now().UTC()
	}
	if err := s.repo.AppendQoSSamples(ctx, []entity.MediaQoSSample{sample}); err != nil {
		return cerrors.Internal("failed to append quality snapshot", err)
	}
	return s.evaluateQualityAlerts(ctx, sample.WorkspaceID, sample.CallID)
}

func (s *Service) RunNodeHeartbeat(ctx context.Context) {
	if s == nil {
		return
	}
	s.publishNodeHeartbeat(ctx)
	ticker := time.NewTicker(s.config.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.observer != nil {
				s.observer.RecordWorkerHeartbeat("media_node_heartbeat")
			}
			opCtx, cancel := reliability.WithTimeout(ctx, s.config.OperationTimeout)
			s.publishNodeHeartbeat(opCtx)
			cancel()
		}
	}
}

func (s *Service) RunTelemetryCollector(ctx context.Context) {
	if s == nil || s.sfu == nil || s.repo == nil {
		return
	}
	s.collectTelemetry(ctx)
	ticker := time.NewTicker(s.config.TelemetryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.observer != nil {
				s.observer.RecordWorkerHeartbeat("media_telemetry")
			}
			if reporter, ok := s.repo.(interface{ Pressure() reliability.Pressure }); ok {
				if pressure := reporter.Pressure(); pressure.Saturated {
					slog.WarnContext(ctx, "media telemetry collector backpressure active", "utilization", pressure.Utilization, "queued_waiters", pressure.QueuedWaiters)
					continue
				}
			}
			opCtx, cancel := reliability.WithTimeout(ctx, s.config.OperationTimeout)
			s.collectTelemetry(opCtx)
			cancel()
		}
	}
}

func (s *Service) publishNodeHeartbeat(ctx context.Context) {
	if s.rdb == nil {
		return
	}
	snapshot := s.localNodeSnapshot()
	raw, err := json.Marshal(snapshot)
	if err != nil {
		slog.ErrorContext(ctx, "failed to marshal media node heartbeat", "node_id", snapshot.NodeID, "error", err)
		return
	}
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, mediaNodeKey(snapshot.NodeID), raw, s.config.NodeTTL)
	pipe.SAdd(ctx, mediaNodeSetKey, snapshot.NodeID)
	pipe.Expire(ctx, mediaNodeSetKey, s.config.NodeTTL*2)
	if _, err := pipe.Exec(ctx); err != nil {
		slog.ErrorContext(ctx, "failed to publish media node heartbeat", "node_id", snapshot.NodeID, "error", err)
	}
}

func (s *Service) collectTelemetry(ctx context.Context) {
	rooms := s.sfu.Rooms()
	for _, room := range rooms {
		if room == nil {
			continue
		}
		callID, err := uuid.Parse(room.ID)
		if err != nil {
			continue
		}
		snapshot := room.TelemetrySnapshot()
		placement, err := s.loadPlacement(ctx, callID)
		if err != nil {
			slog.WarnContext(ctx, "failed to load media placement for telemetry collection", "call_id", callID, "error", err)
		}
		samples := s.qosSamplesForRoom(callID, placement, snapshot)
		if len(samples) == 0 {
			continue
		}
		if err := s.repo.AppendQoSSamples(ctx, samples); err != nil {
			slog.ErrorContext(ctx, "failed to append media qos samples", "call_id", callID, "error", err)
			continue
		}
		if placement != nil {
			if err := s.applyServerDrivenAdaptation(ctx, room, placement, snapshot); err != nil {
				slog.ErrorContext(ctx, "failed to apply server-driven adaptation", "call_id", callID, "error", err)
			}
		}
		if err := s.evaluateQualityAlerts(ctx, samples[0].WorkspaceID, callID); err != nil {
			slog.ErrorContext(ctx, "failed to evaluate media quality alerts", "call_id", callID, "error", err)
		}
	}
}

func (s *Service) loadPlacement(ctx context.Context, callID uuid.UUID) (*entity.MediaRoomPlacement, error) {
	if s.repo == nil {
		return nil, nil
	}
	placement, err := s.repo.GetPlacement(ctx, callID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, nil
		}
		return nil, err
	}
	return placement, nil
}

func (s *Service) qosSamplesForRoom(callID uuid.UUID, placement *entity.MediaRoomPlacement, snapshot sfu.RoomTelemetry) []entity.MediaQoSSample {
	workspaceID := uuid.Nil
	region := s.config.Region
	nodeID := s.config.NodeID
	if placement != nil {
		workspaceID = placement.WorkspaceID
		if placement.Region != "" {
			region = placement.Region
		}
		if placement.NodeID != "" {
			nodeID = placement.NodeID
		}
	}
	if workspaceID == uuid.Nil {
		return nil
	}

	samples := make([]entity.MediaQoSSample, 0, len(snapshot.Participants)*2)
	for _, participant := range snapshot.Participants {
		userID, err := uuid.Parse(participant.UserID)
		if err != nil {
			continue
		}
		if len(participant.Samples) == 0 {
			samples = append(samples, entity.MediaQoSSample{
				ID:              uuid.New(),
				WorkspaceID:     workspaceID,
				CallID:          callID,
				UserID:          userID,
				NodeID:          nodeID,
				Region:          region,
				Source:          entity.MediaTelemetrySourceServer,
				ParticipantRole: string(participant.Role),
				Metadata: map[string]any{
					"connection_state":     participant.ConnectionState,
					"ice_connection_state": participant.ICEConnectionState,
				},
				SampledAt: snapshot.SampledAt,
			})
			continue
		}
		for _, sample := range participant.Samples {
			metadata := map[string]any{
				"connection_state":     participant.ConnectionState,
				"ice_connection_state": participant.ICEConnectionState,
			}
			for key, value := range sample.Metadata {
				metadata[key] = value
			}
			samples = append(samples, entity.MediaQoSSample{
				ID:                           uuid.New(),
				WorkspaceID:                  workspaceID,
				CallID:                       callID,
				UserID:                       userID,
				NodeID:                       nodeID,
				Region:                       region,
				StreamID:                     sample.StreamID,
				Source:                       entity.MediaTelemetrySourceServer,
				ParticipantRole:              string(participant.Role),
				MediaKind:                    sample.MediaKind,
				PacketLossPct:                sample.PacketLossPct,
				JitterMs:                     sample.JitterMs,
				RoundTripTimeMs:              sample.RoundTripTimeMs,
				AvailableOutgoingBitrateKbps: sample.AvailableOutgoingBitrateKbps,
				AvailableIncomingBitrateKbps: sample.AvailableIncomingBitrateKbps,
				BytesSent:                    sample.BytesSent,
				BytesReceived:                sample.BytesReceived,
				Metadata:                     metadata,
				SampledAt:                    snapshot.SampledAt,
			})
		}
	}
	return samples
}

func (s *Service) applyServerDrivenAdaptation(ctx context.Context, room *sfu.Room, placement *entity.MediaRoomPlacement, snapshot sfu.RoomTelemetry) error {
	if room == nil || placement == nil {
		return nil
	}
	policy, err := s.GetCallQualityPolicy(ctx, placement.WorkspaceID, placement.CallID)
	if err != nil {
		return err
	}
	if policy == nil || !policy.ServerDrivenEnabled {
		return nil
	}

	for _, participant := range snapshot.Participants {
		if participant.UserID == "" {
			continue
		}
		if participant.ConnectionState == "failed" || participant.ICEConnectionState == "failed" {
			continue
		}
		targets := room.SubscriberTargets(participant.UserID)
		if len(targets) == 0 {
			continue
		}
		for _, sample := range s.serverDrivenSamplesForParticipant(placement.CallID, participant, targets, snapshot.SampledAt) {
			decision, err := room.PlanSubscriberAdaptation(sample)
			if err != nil {
				if isMissingAdaptiveTarget(err) {
					continue
				}
				slog.WarnContext(ctx, "failed to plan server-driven adaptation", "call_id", placement.CallID, "user_id", participant.UserID, "stream_id", sample.StreamID, "error", err)
				continue
			}
			decision = applyServerDrivenPolicy(decision, policy)
			if err := room.ApplyAdaptiveDecision(decision); err != nil && !isMissingAdaptiveTarget(err) {
				slog.WarnContext(ctx, "failed to apply server-driven adaptation", "call_id", placement.CallID, "user_id", participant.UserID, "stream_id", sample.StreamID, "error", err)
				continue
			}

			userID, err := uuid.Parse(participant.UserID)
			if err != nil {
				continue
			}
			if s.shouldPublishQualityDecision(placement.CallID, userID, decision, policy) {
				s.publishQualityDecision(ctx, placement, userID, decision)
			}
		}
	}
	return nil
}

func (s *Service) serverDrivenSamplesForParticipant(callID uuid.UUID, participant sfu.PeerTelemetry, targets []sfu.SubscriberStreamTarget, sampledAt time.Time) []sfu.NetworkSample {
	audioSample, videoSample := splitTelemetrySamples(participant.Samples)
	if audioSample == nil && videoSample == nil {
		return nil
	}

	base := sfu.NetworkSample{
		UserID:    participant.UserID,
		Timestamp: sampledAt,
	}
	if videoSample != nil {
		delta := s.serverTelemetryDelta(callID, participant.UserID, normalizedMediaKindFromTelemetry(*videoSample), *videoSample, sampledAt)
		base.AvailableBitrateKbps = maxInt(videoSample.AvailableIncomingBitrateKbps, videoSample.AvailableOutgoingBitrateKbps)
		base.ObservedBitrateKbps = delta.ObservedBitrateKbps
		base.PacketLossPct = videoSample.PacketLossPct
		base.RoundTripTimeMs = int(math.Round(videoSample.RoundTripTimeMs))
		base.JitterMs = videoSample.JitterMs
		base.FramesPerSecond = delta.FramesPerSecond
		base.DroppedFramesPct = delta.DroppedFramesPct
		base.DecodeTimeMs = telemetryMetadataFloat(videoSample.Metadata, "avg_decode_time_ms")
		base.FreezeCountDelta = delta.FreezeCountDelta
		base.NACKCountDelta = delta.NACKCountDelta
		base.PLICountDelta = delta.PLICountDelta
	}
	if audioSample != nil {
		base.AudioPacketLossPct = audioSample.PacketLossPct
		base.AudioJitterMs = audioSample.JitterMs
		if base.AvailableBitrateKbps <= 0 {
			base.AvailableBitrateKbps = maxInt(audioSample.AvailableIncomingBitrateKbps, audioSample.AvailableOutgoingBitrateKbps)
		}
		if base.RoundTripTimeMs <= 0 {
			base.RoundTripTimeMs = int(math.Round(audioSample.RoundTripTimeMs))
		}
		if base.JitterMs <= 0 {
			base.JitterMs = audioSample.JitterMs
		}
		if base.PacketLossPct <= 0 {
			base.PacketLossPct = audioSample.PacketLossPct
		}
	}

	samples := make([]sfu.NetworkSample, 0, len(targets))
	for _, target := range targets {
		sample := base
		sample.StreamID = target.StreamID
		sample.ScreenShare = target.ScreenShare
		samples = append(samples, sample)
	}
	return samples
}

type telemetryDerivedMetrics struct {
	ObservedBitrateKbps int
	FramesPerSecond     float64
	DroppedFramesPct    float64
	FreezeCountDelta    int
	NACKCountDelta      int
	PLICountDelta       int
}

func (s *Service) serverTelemetryDelta(callID uuid.UUID, userID, mediaKind string, sample sfu.PeerMediaTelemetry, sampledAt time.Time) telemetryDerivedMetrics {
	stateKey := strings.Join([]string{callID.String(), userID, mediaKind}, ":")
	current := telemetryCounterState{
		BytesReceived:  maxInt64(sample.BytesReceived, 0),
		FramesReceived: int64(telemetryMetadataInt(sample.Metadata, "frames_received")),
		FramesDropped:  int64(telemetryMetadataInt(sample.Metadata, "frames_dropped")),
		FreezeCount:    int64(telemetryMetadataInt(sample.Metadata, "freeze_count")),
		NACKCount:      int64(telemetryMetadataInt(sample.Metadata, "nack_count")),
		PLICount:       int64(telemetryMetadataInt(sample.Metadata, "pli_count")),
		SampledAt:      sampledAt,
	}

	s.mu.Lock()
	previous := s.telemetryCounters[stateKey]
	s.telemetryCounters[stateKey] = current
	s.mu.Unlock()

	metrics := telemetryDerivedMetrics{}
	if previous.SampledAt.IsZero() || !current.SampledAt.After(previous.SampledAt) {
		return metrics
	}

	elapsed := current.SampledAt.Sub(previous.SampledAt).Seconds()
	if elapsed <= 0 {
		return metrics
	}

	if deltaBytes := maxInt64(current.BytesReceived-previous.BytesReceived, 0); deltaBytes > 0 {
		metrics.ObservedBitrateKbps = int(math.Round((float64(deltaBytes) * 8) / elapsed / 1000))
	}
	if deltaFrames := maxInt64(current.FramesReceived-previous.FramesReceived, 0); deltaFrames > 0 {
		metrics.FramesPerSecond = float64(deltaFrames) / elapsed
		totalFrames := deltaFrames + maxInt64(current.FramesDropped-previous.FramesDropped, 0)
		if totalFrames > 0 {
			metrics.DroppedFramesPct = float64(maxInt64(current.FramesDropped-previous.FramesDropped, 0)) * 100 / float64(totalFrames)
		}
	}
	metrics.FreezeCountDelta = int(maxInt64(current.FreezeCount-previous.FreezeCount, 0))
	metrics.NACKCountDelta = int(maxInt64(current.NACKCount-previous.NACKCount, 0))
	metrics.PLICountDelta = int(maxInt64(current.PLICount-previous.PLICount, 0))
	return metrics
}

func splitTelemetrySamples(samples []sfu.PeerMediaTelemetry) (audio *sfu.PeerMediaTelemetry, video *sfu.PeerMediaTelemetry) {
	for i := range samples {
		sample := &samples[i]
		switch strings.ToLower(strings.TrimSpace(sample.MediaKind)) {
		case "audio":
			audio = sample
		case "video", "screen":
			video = sample
		default:
			if video == nil {
				video = sample
			}
		}
	}
	return audio, video
}

func telemetryMetadataInt(metadata map[string]any, key string) int {
	if metadata == nil {
		return 0
	}
	switch value := metadata[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float32:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func telemetryMetadataFloat(metadata map[string]any, key string) float64 {
	if metadata == nil {
		return 0
	}
	switch value := metadata[key].(type) {
	case float32:
		return float64(value)
	case float64:
		return value
	case int:
		return float64(value)
	case int32:
		return float64(value)
	case int64:
		return float64(value)
	default:
		return 0
	}
}

func applyServerDrivenPolicy(decision sfu.AdaptiveDecision, policy *entity.MediaQualityPolicy) sfu.AdaptiveDecision {
	if policy == nil {
		return decision
	}
	switch policy.Mode {
	case entity.MediaQualityPolicyConserveBandwidth:
		if decision.TargetQuality == sfu.QualityHigh {
			decision.TargetQuality = sfu.QualityMedium
		}
		if decision.MaxVideoFPS > 20 {
			decision.MaxVideoFPS = 20
		}
		if decision.MaxVideoBitrateKbps > 900 {
			decision.MaxVideoBitrateKbps = 900
		}
		decision.Reasons = append(decision.Reasons, "server-driven conserve-bandwidth policy applied")
	case entity.MediaQualityPolicyForceLow:
		decision.TargetQuality = sfu.QualityLow
		decision.MaxVideoFPS = 12
		if decision.MaxVideoBitrateKbps > 250 || decision.MaxVideoBitrateKbps == 0 {
			decision.MaxVideoBitrateKbps = 250
		}
		decision.Reasons = append(decision.Reasons, "server-driven force-low policy applied")
	case entity.MediaQualityPolicyAudioOnly:
		decision.TargetQuality = sfu.QualityLow
		decision.VideoSuspended = true
		decision.MaxVideoFPS = 0
		decision.MaxVideoBitrateKbps = 0
		decision.TargetVideoBufferMs = 0
		decision.VideoDegradeMode = "suspend_video_until_audio_recovers"
		decision.Reasons = append(decision.Reasons, "server-driven audio-only policy applied")
	}
	decision.Changed = decision.TargetQuality != decision.PreviousQuality
	return decision
}

func (s *Service) shouldPublishQualityDecision(callID, userID uuid.UUID, decision sfu.AdaptiveDecision, policy *entity.MediaQualityPolicy) bool {
	if userID == uuid.Nil || decision.StreamID == "" {
		return false
	}
	minInterval := s.config.ServerDrivenMinInterval
	if policy != nil && policy.ServerDrivenMinInterval > 0 {
		minInterval = time.Duration(policy.ServerDrivenMinInterval) * time.Millisecond
	}
	if minInterval <= 0 {
		minInterval = 4 * time.Second
	}

	key := strings.Join([]string{callID.String(), userID.String(), decision.StreamID}, ":")
	next := publishedQualityDecision{
		TargetQuality:       decision.TargetQuality,
		NetworkGrade:        decision.NetworkGrade,
		AudioPriority:       decision.AudioPriority,
		VideoSuspended:      decision.VideoSuspended,
		MaxVideoBitrateKbps: decision.MaxVideoBitrateKbps,
		MaxVideoFPS:         decision.MaxVideoFPS,
		UpdatedAt:           time.Now().UTC(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	current, ok := s.publishedDecision[key]
	if ok &&
		current.TargetQuality == next.TargetQuality &&
		current.NetworkGrade == next.NetworkGrade &&
		current.AudioPriority == next.AudioPriority &&
		current.VideoSuspended == next.VideoSuspended &&
		current.MaxVideoBitrateKbps == next.MaxVideoBitrateKbps &&
		current.MaxVideoFPS == next.MaxVideoFPS {
		return false
	}
	qualityChanged := !ok ||
		current.TargetQuality != next.TargetQuality ||
		current.AudioPriority != next.AudioPriority ||
		current.VideoSuspended != next.VideoSuspended
	if ok && !qualityChanged && next.UpdatedAt.Sub(current.UpdatedAt) < minInterval &&
		decision.NetworkGrade != sfu.NetworkGradeCritical && !decision.VideoSuspended {
		return false
	}
	s.publishedDecision[key] = next
	return true
}

func (s *Service) publishQualityDecision(ctx context.Context, placement *entity.MediaRoomPlacement, userID uuid.UUID, decision sfu.AdaptiveDecision) {
	if s.publisher == nil || placement == nil || userID == uuid.Nil {
		return
	}

	subject := fmt.Sprintf("aloqa.signal.%s", userID)
	definition := event.DefinitionForType(event.TypeCallQualityAdapted)
	evt := event.Event{
		ID:               uuid.New(),
		Version:          definition.Version,
		Type:             event.TypeCallQualityAdapted,
		Subject:          subject,
		WorkspaceID:      placement.WorkspaceID,
		UserID:           userID,
		DeliverySemantic: definition.DeliverySemantic,
		Replayable:       definition.Replayable,
		Timestamp:        time.Now().UTC(),
		Payload: event.CallQualityPayload{
			CallID:              placement.CallID,
			UserID:              userID,
			StreamID:            decision.StreamID,
			Source:              "server_telemetry",
			PreviousQuality:     string(decision.PreviousQuality),
			TargetQuality:       string(decision.TargetQuality),
			NetworkGrade:        string(decision.NetworkGrade),
			AudioPriority:       decision.AudioPriority,
			VideoSuspended:      decision.VideoSuspended,
			SyncMode:            decision.SyncMode,
			VideoDegradeMode:    decision.VideoDegradeMode,
			MaxVideoBitrateKbps: decision.MaxVideoBitrateKbps,
			MaxVideoFPS:         decision.MaxVideoFPS,
			TargetAudioBufferMs: decision.TargetAudioBufferMs,
			TargetVideoBufferMs: decision.TargetVideoBufferMs,
			LipSyncWindowMs:     decision.LipSyncWindowMs,
			Reasons:             decision.Reasons,
		},
	}
	body, err := json.Marshal(evt)
	if err != nil {
		slog.WarnContext(ctx, "failed to marshal server-driven quality event", "call_id", placement.CallID, "user_id", userID, "error", err)
		return
	}
	if err := s.publisher.Publish(ctx, subject, body); err != nil {
		slog.WarnContext(ctx, "failed to publish server-driven quality event", "call_id", placement.CallID, "user_id", userID, "error", err)
	}
}

func normalizedMediaKindFromTelemetry(sample sfu.PeerMediaTelemetry) string {
	kind := strings.TrimSpace(strings.ToLower(sample.MediaKind))
	if kind != "" {
		return kind
	}
	return "video"
}

func isMissingAdaptiveTarget(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "layer")
}

func (s *Service) localNodeSnapshot() entity.MediaNodeSnapshot {
	stats := sfu.SFUStats{}
	if s.sfu != nil {
		stats = s.sfu.Stats()
	}
	status := entity.MediaNodeStatusActive
	maxRooms := s.config.MaxRoomsPerNode
	if maxRooms > 0 && stats.Rooms >= maxRooms {
		status = entity.MediaNodeStatusOverloaded
	}
	loadScore := 0.0
	if maxRooms > 0 {
		loadScore = float64(stats.Rooms) / float64(maxRooms)
	}
	return entity.MediaNodeSnapshot{
		NodeID:          s.config.NodeID,
		Region:          s.config.Region,
		ControlURL:      s.config.ControlURL,
		MediaURL:        s.config.MediaURL,
		Status:          status,
		MaxRooms:        maxRooms,
		Rooms:           stats.Rooms,
		Presenters:      stats.Presenters,
		Viewers:         stats.Viewers,
		Tracks:          stats.Tracks,
		SimulcastTracks: stats.SimulcastTracks,
		LoadScore:       loadScore,
		LastHeartbeatAt: time.Now().UTC(),
		Capabilities: []string{
			"sfu",
			"simulcast",
			"adaptive_quality",
			"server_telemetry",
		},
	}
}

func (s *Service) selectNode(callID uuid.UUID, nodes []entity.MediaNodeSnapshot, policy entity.MediaCallPolicy) (entity.MediaNodeSnapshot, error) {
	if len(nodes) == 0 {
		return entity.MediaNodeSnapshot{}, cerrors.Unavailable("no media nodes are available")
	}

	preferred := filterNodesByRegion(nodes, s.config.Region)
	candidates := s.filterAvailableNodes(preferred)
	if len(candidates) == 0 {
		switch policy.OverflowPolicy {
		case entity.MediaOverflowRegionalMove, entity.MediaOverflowWebinarEdge:
			candidates = s.filterAvailableNodes(nodes)
		default:
			return entity.MediaNodeSnapshot{}, cerrors.Unavailable("preferred media region is at capacity")
		}
	}
	if len(candidates) == 0 {
		return entity.MediaNodeSnapshot{}, cerrors.Unavailable("no media nodes have room capacity")
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].LoadScore == candidates[j].LoadScore {
			return candidates[i].NodeID < candidates[j].NodeID
		}
		return candidates[i].LoadScore < candidates[j].LoadScore
	})

	index := int(crc32.ChecksumIEEE([]byte(callID.String())) % uint32(len(candidates)))
	return candidates[index], nil
}

func (s *Service) filterAvailableNodes(nodes []entity.MediaNodeSnapshot) []entity.MediaNodeSnapshot {
	filtered := make([]entity.MediaNodeSnapshot, 0, len(nodes))
	for _, node := range nodes {
		if node.Status == entity.MediaNodeStatusDraining || node.Status == entity.MediaNodeStatusOverloaded {
			continue
		}
		if node.MaxRooms > 0 && node.Rooms >= node.MaxRooms {
			continue
		}
		filtered = append(filtered, node)
	}
	return filtered
}

func filterNodesByRegion(nodes []entity.MediaNodeSnapshot, region string) []entity.MediaNodeSnapshot {
	if strings.TrimSpace(region) == "" {
		return nodes
	}
	filtered := make([]entity.MediaNodeSnapshot, 0, len(nodes))
	for _, node := range nodes {
		if node.Region == region {
			filtered = append(filtered, node)
		}
	}
	if len(filtered) == 0 {
		return nodes
	}
	return filtered
}

func (s *Service) participantCapByType(callType entity.CallType) int {
	switch callType {
	case entity.CallTypeOneToOne:
		return s.config.OneToOneParticipantCap
	case entity.CallTypeGroup:
		return s.config.GroupParticipantCap
	case entity.CallTypeMeeting:
		return s.config.MeetingParticipantCap
	case entity.CallTypeWebinar:
		return s.config.WebinarParticipantCap
	case entity.CallTypeSelector:
		return s.config.SelectorParticipantCap
	default:
		return s.config.MeetingParticipantCap
	}
}

func mediaNodeKey(nodeID string) string {
	return "media:nodes:" + nodeID
}

func minPositive(a, b int) int {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case a < b:
		return a
	default:
		return b
	}
}

func (s *Service) defaultQualityPolicy(workspaceID, callID uuid.UUID) *entity.MediaQualityPolicy {
	return &entity.MediaQualityPolicy{
		WorkspaceID:             workspaceID,
		CallID:                  callID,
		Mode:                    s.config.DefaultQualityMode,
		AlertPacketLossPct:      s.config.AlertPacketLossPct,
		AlertJitterMs:           s.config.AlertJitterMs,
		AlertRoundTripTimeMs:    s.config.AlertRoundTripTimeMs,
		CorrelationTolerancePct: s.config.CorrelationTolerancePct,
		CorrelationToleranceMs:  s.config.CorrelationToleranceMs,
		ServerDrivenEnabled:     s.config.ServerDrivenEnabled,
		ServerDrivenMinInterval: int(s.config.ServerDrivenMinInterval / time.Millisecond),
		MeetingWideDowngrade:    false,
		AlertingEnabled:         true,
		UpdatedAt:               time.Now().UTC(),
	}
}

func (s *Service) normalizeQualityPolicy(policy entity.MediaQualityPolicy) entity.MediaQualityPolicy {
	defaults := s.defaultQualityPolicy(policy.WorkspaceID, policy.CallID)
	if policy.Mode == "" {
		policy.Mode = defaults.Mode
	}
	if policy.AlertPacketLossPct <= 0 {
		policy.AlertPacketLossPct = defaults.AlertPacketLossPct
	}
	if policy.AlertJitterMs <= 0 {
		policy.AlertJitterMs = defaults.AlertJitterMs
	}
	if policy.AlertRoundTripTimeMs <= 0 {
		policy.AlertRoundTripTimeMs = defaults.AlertRoundTripTimeMs
	}
	if policy.CorrelationTolerancePct <= 0 {
		policy.CorrelationTolerancePct = defaults.CorrelationTolerancePct
	}
	if policy.CorrelationToleranceMs <= 0 {
		policy.CorrelationToleranceMs = defaults.CorrelationToleranceMs
	}
	if policy.ServerDrivenMinInterval <= 0 {
		policy.ServerDrivenMinInterval = defaults.ServerDrivenMinInterval
	}
	if policy.UpdatedAt.IsZero() {
		policy.UpdatedAt = time.Now().UTC()
	}
	return policy
}

func (s *Service) evaluateQualityAlerts(ctx context.Context, workspaceID, callID uuid.UUID) error {
	if s.repo == nil || workspaceID == uuid.Nil || callID == uuid.Nil {
		return nil
	}
	policy, err := s.GetCallQualityPolicy(ctx, workspaceID, callID)
	if err != nil {
		return err
	}
	if !policy.AlertingEnabled {
		if err := s.repo.ResolveQualityAlert(ctx, workspaceID, callID, "degraded_call"); err != nil {
			return cerrors.Internal("failed to resolve degraded-call alert", err)
		}
		if err := s.repo.ResolveQualityAlert(ctx, workspaceID, callID, "server_client_mismatch"); err != nil {
			return cerrors.Internal("failed to resolve quality mismatch alert", err)
		}
		if s.observer != nil {
			s.observer.RecordCallQualityAggregate(workspaceID, callID, entity.MediaQoSSummary{WorkspaceID: workspaceID, CallID: callID}, entity.MediaQoSSummary{WorkspaceID: workspaceID, CallID: callID}, false, false)
		}
		return nil
	}

	snapshots, err := s.repo.ListQoSSamples(ctx, workspaceID, callID, 250)
	if err != nil {
		return cerrors.Internal("failed to load quality snapshots", err)
	}
	serverSummary := summarizeSnapshots(workspaceID, callID, filterSnapshotsBySource(snapshots, entity.MediaTelemetrySourceServer))
	clientSummary := summarizeSnapshots(workspaceID, callID, filterSnapshotsBySource(snapshots, entity.MediaTelemetrySourceClient))
	if shouldAlertOnSummary(serverSummary, clientSummary, *policy) {
		severity := alertSeverity(serverSummary, clientSummary, *policy)
		now := time.Now().UTC()
		if err := s.repo.UpsertQualityAlert(ctx, &entity.MediaQualityAlert{
			ID:          uuid.New(),
			WorkspaceID: workspaceID,
			CallID:      callID,
			Kind:        "degraded_call",
			Severity:    severity,
			Status:      entity.MediaQualityAlertStatusActive,
			Message:     "Call quality is degraded beyond policy thresholds",
			Metadata: map[string]any{
				"server_summary": serverSummary,
				"client_summary": clientSummary,
			},
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			return cerrors.Internal("failed to upsert degraded-call alert", err)
		}
	} else if err := s.repo.ResolveQualityAlert(ctx, workspaceID, callID, "degraded_call"); err != nil {
		return cerrors.Internal("failed to resolve degraded-call alert", err)
	}

	correlations := correlateSnapshots(snapshots, *policy)
	hasMismatch := false
	for _, correlation := range correlations {
		if !correlation.Healthy {
			hasMismatch = true
			break
		}
	}
	if hasMismatch {
		now := time.Now().UTC()
		if err := s.repo.UpsertQualityAlert(ctx, &entity.MediaQualityAlert{
			ID:          uuid.New(),
			WorkspaceID: workspaceID,
			CallID:      callID,
			Kind:        "server_client_mismatch",
			Severity:    entity.MediaQualityAlertSeverityWarning,
			Status:      entity.MediaQualityAlertStatusActive,
			Message:     "Client quality reports diverge from server-side media truth",
			Metadata: map[string]any{
				"correlations": correlations,
			},
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			return cerrors.Internal("failed to upsert quality mismatch alert", err)
		}
	} else if err := s.repo.ResolveQualityAlert(ctx, workspaceID, callID, "server_client_mismatch"); err != nil {
		return cerrors.Internal("failed to resolve quality mismatch alert", err)
	}
	if s.observer != nil {
		s.observer.RecordCallQualityAggregate(workspaceID, callID, serverSummary, clientSummary, shouldAlertOnSummary(serverSummary, clientSummary, *policy), hasMismatch)
	}
	return nil
}

func filterSnapshotsBySource(snapshots []entity.MediaQoSSample, source entity.MediaTelemetrySource) []entity.MediaQoSSample {
	filtered := make([]entity.MediaQoSSample, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot.Source == source {
			filtered = append(filtered, snapshot)
		}
	}
	return filtered
}

func summarizeSnapshots(workspaceID, callID uuid.UUID, snapshots []entity.MediaQoSSample) entity.MediaQoSSummary {
	summary := entity.MediaQoSSummary{
		WorkspaceID: workspaceID,
		CallID:      callID,
	}
	if len(snapshots) == 0 {
		return summary
	}
	for _, snapshot := range snapshots {
		summary.SampleCount++
		if snapshot.SampledAt.After(summary.LastSampledAt) {
			summary.LastSampledAt = snapshot.SampledAt
		}
		summary.AvgPacketLossPct += snapshot.PacketLossPct
		summary.AvgJitterMs += snapshot.JitterMs
		summary.AvgRoundTripTimeMs += snapshot.RoundTripTimeMs
		summary.MaxPacketLossPct = math.Max(summary.MaxPacketLossPct, snapshot.PacketLossPct)
		summary.MaxJitterMs = math.Max(summary.MaxJitterMs, snapshot.JitterMs)
		summary.MaxRoundTripTimeMs = math.Max(summary.MaxRoundTripTimeMs, snapshot.RoundTripTimeMs)
	}
	count := float64(summary.SampleCount)
	summary.AvgPacketLossPct /= count
	summary.AvgJitterMs /= count
	summary.AvgRoundTripTimeMs /= count
	return summary
}

func correlateSnapshots(snapshots []entity.MediaQoSSample, policy entity.MediaQualityPolicy) []entity.MediaQualityCorrelation {
	serverLatest := map[string]entity.MediaQoSSample{}
	clientLatest := map[string]entity.MediaQoSSample{}
	for _, snapshot := range snapshots {
		key := snapshot.UserID.String() + ":" + normalizedMediaKind(snapshot)
		switch snapshot.Source {
		case entity.MediaTelemetrySourceServer:
			if current, ok := serverLatest[key]; !ok || snapshot.SampledAt.After(current.SampledAt) {
				serverLatest[key] = snapshot
			}
		case entity.MediaTelemetrySourceClient:
			if current, ok := clientLatest[key]; !ok || snapshot.SampledAt.After(current.SampledAt) {
				clientLatest[key] = snapshot
			}
		}
	}
	keys := make(map[string]struct{}, len(serverLatest)+len(clientLatest))
	for key := range serverLatest {
		keys[key] = struct{}{}
	}
	for key := range clientLatest {
		keys[key] = struct{}{}
	}

	result := make([]entity.MediaQualityCorrelation, 0, len(keys))
	for key := range keys {
		server := serverLatest[key]
		client := clientLatest[key]
		correlation := entity.MediaQualityCorrelation{
			UserID:    server.UserID,
			MediaKind: normalizedMediaKind(server),
			Healthy:   true,
		}
		if correlation.UserID == uuid.Nil {
			correlation.UserID = client.UserID
			correlation.MediaKind = normalizedMediaKind(client)
		}
		if server.ID != uuid.Nil {
			serverCopy := server
			correlation.ServerSample = &serverCopy
		}
		if client.ID != uuid.Nil {
			clientCopy := client
			correlation.ClientSample = &clientCopy
		}
		if correlation.ServerSample == nil || correlation.ClientSample == nil {
			correlation.Healthy = false
			result = append(result, correlation)
			continue
		}
		correlation.PacketLossDeltaPct = math.Abs(server.PacketLossPct - client.PacketLossPct)
		correlation.JitterDeltaMs = math.Abs(server.JitterMs - client.JitterMs)
		correlation.RoundTripTimeDeltaMs = math.Abs(server.RoundTripTimeMs - client.RoundTripTimeMs)
		correlation.SampleSkewMs = math.Abs(float64(server.SampledAt.Sub(client.SampledAt).Milliseconds()))
		if correlation.PacketLossDeltaPct > policy.CorrelationTolerancePct ||
			correlation.JitterDeltaMs > policy.CorrelationToleranceMs ||
			correlation.RoundTripTimeDeltaMs > policy.CorrelationToleranceMs {
			correlation.Healthy = false
		}
		result = append(result, correlation)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UserID == result[j].UserID {
			return result[i].MediaKind < result[j].MediaKind
		}
		return result[i].UserID.String() < result[j].UserID.String()
	})
	return result
}

func shouldAlertOnSummary(server, client entity.MediaQoSSummary, policy entity.MediaQualityPolicy) bool {
	packetLoss := math.Max(server.MaxPacketLossPct, client.MaxPacketLossPct)
	jitter := math.Max(server.MaxJitterMs, client.MaxJitterMs)
	rtt := math.Max(server.MaxRoundTripTimeMs, client.MaxRoundTripTimeMs)
	return packetLoss >= policy.AlertPacketLossPct ||
		jitter >= policy.AlertJitterMs ||
		rtt >= policy.AlertRoundTripTimeMs
}

func alertSeverity(server, client entity.MediaQoSSummary, policy entity.MediaQualityPolicy) entity.MediaQualityAlertSeverity {
	packetLoss := math.Max(server.MaxPacketLossPct, client.MaxPacketLossPct)
	jitter := math.Max(server.MaxJitterMs, client.MaxJitterMs)
	rtt := math.Max(server.MaxRoundTripTimeMs, client.MaxRoundTripTimeMs)
	if packetLoss >= policy.AlertPacketLossPct*2 ||
		jitter >= policy.AlertJitterMs*2 ||
		rtt >= policy.AlertRoundTripTimeMs*2 {
		return entity.MediaQualityAlertSeverityCritical
	}
	return entity.MediaQualityAlertSeverityWarning
}

func normalizedMediaKind(snapshot entity.MediaQoSSample) string {
	kind := strings.TrimSpace(strings.ToLower(snapshot.MediaKind))
	if kind != "" {
		return kind
	}
	if strings.TrimSpace(snapshot.StreamID) != "" {
		return strings.ToLower(snapshot.StreamID)
	}
	return "video"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
