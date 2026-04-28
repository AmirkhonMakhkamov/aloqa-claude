package mediaops

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/media/sfu"
	"aloqa/internal/pkg/cerrors"
)

func TestPolicyForWebinarEnablesRegionalFanout(t *testing.T) {
	svc := NewService(nil, nil, nil, Config{
		Region:                 "us-east",
		WebinarParticipantCap:  20000,
		WebinarPresenterCap:    60,
		WebinarFanoutThreshold: 1500,
	})

	call := &entity.Call{
		ID:          uuid.New(),
		WorkspaceID: uuid.New(),
		Type:        entity.CallTypeWebinar,
		Settings: entity.CallSettings{
			MaxParticipants: 8000,
			ScreenSharing:   true,
		},
	}

	policy := svc.PolicyForCall(call)
	if policy.FanoutStrategy != entity.MediaFanoutWebinarEdges {
		t.Fatalf("fanout strategy = %q, want %q", policy.FanoutStrategy, entity.MediaFanoutWebinarEdges)
	}
	if policy.RoutingMode != entity.MediaRoutingRegionalEdge {
		t.Fatalf("routing mode = %q, want %q", policy.RoutingMode, entity.MediaRoutingRegionalEdge)
	}
	if policy.MaxParticipants != 8000 {
		t.Fatalf("max participants = %d, want 8000", policy.MaxParticipants)
	}
	if policy.MaxPresenters != 60 {
		t.Fatalf("max presenters = %d, want 60", policy.MaxPresenters)
	}
	if policy.ScreenSharePriority != entity.MediaScreenShareProtected {
		t.Fatalf("screen share priority = %q, want protected", policy.ScreenSharePriority)
	}
}

func TestSelectNodePrefersSameRegionWhenAvailable(t *testing.T) {
	svc := NewService(nil, nil, nil, Config{Region: "eu-west"})
	callID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	policy := entity.MediaCallPolicy{
		MaxParticipants: 200,
		OverflowPolicy:  entity.MediaOverflowRegionalMove,
	}

	selected, err := svc.selectNode(callID, []entity.MediaNodeSnapshot{
		{NodeID: "node-a", Region: "us-east", Status: entity.MediaNodeStatusActive, MaxRooms: 100, Rooms: 2, LoadScore: 0.02},
		{NodeID: "node-b", Region: "eu-west", Status: entity.MediaNodeStatusActive, MaxRooms: 100, Rooms: 5, LoadScore: 0.05},
	}, policy)
	if err != nil {
		t.Fatalf("selectNode returned error: %v", err)
	}
	if selected.Region != "eu-west" {
		t.Fatalf("selected region = %q, want eu-west", selected.Region)
	}
}

func TestEnsurePlacementStoresDeterministicSelection(t *testing.T) {
	repo := &fakeMediaRepo{}
	svc := NewService(repo, nil, nil, Config{
		NodeID:                 "edge-a",
		Region:                 "eu-west",
		MeetingParticipantCap:  500,
		WebinarFanoutThreshold: 1000,
	})

	call := &entity.Call{
		ID:          uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		WorkspaceID: uuid.New(),
		Type:        entity.CallTypeMeeting,
		Settings: entity.CallSettings{
			MaxParticipants: 120,
			ScreenSharing:   true,
		},
	}

	placement, err := svc.EnsurePlacement(context.Background(), call, sfu.RoomOptions{})
	if err != nil {
		t.Fatalf("EnsurePlacement returned error: %v", err)
	}
	if placement.NodeID != "edge-a" {
		t.Fatalf("placement node = %q, want edge-a", placement.NodeID)
	}
	if repo.placement == nil || repo.placement.CallID != call.ID {
		t.Fatalf("placement was not persisted")
	}
}

func TestResolveParticipantPlacementPrefersLocalRelayEdgeForViewer(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()
	repo := &fakeMediaRepo{
		placement: &entity.MediaRoomPlacement{
			CallID:          callID,
			WorkspaceID:     workspaceID,
			NodeID:          "origin-a",
			Region:          "us-east",
			ControlURL:      "https://origin-a.example.com",
			MediaURL:        "wss://origin-a.example.com/media",
			FanoutStrategy:  entity.MediaFanoutWebinarEdges,
			MaxParticipants: 10000,
			MaxPresenters:   50,
			MaxViewers:      10000,
		},
		relayEdges: []entity.MediaRelayEdge{{
			CallID:         callID,
			WorkspaceID:    workspaceID,
			SourceNodeID:   "origin-a",
			TargetNodeID:   "edge-eu",
			TargetRegion:   "eu-west",
			ControlURL:     "https://edge-eu.example.com",
			MediaURL:       "wss://edge-eu.example.com/media",
			FanoutStrategy: entity.MediaFanoutWebinarEdges,
			RoleScope:      entity.MediaRelayRoleScopeViewers,
			Status:         entity.MediaRelayStatusActive,
			Priority:       0,
		}},
	}
	svc := NewService(repo, nil, nil, Config{NodeID: "edge-eu", Region: "eu-west"})

	call := &entity.Call{ID: callID, WorkspaceID: workspaceID, Type: entity.CallTypeWebinar}
	participant := &entity.CallParticipant{UserID: userID, Role: entity.CallRoleViewer}
	placement, err := svc.ResolveParticipantPlacement(ctx, call, participant, "edge-eu")
	if err != nil {
		t.Fatalf("ResolveParticipantPlacement returned error: %v", err)
	}
	if placement.NodeID != "edge-eu" || placement.ControlURL != "https://edge-eu.example.com" {
		t.Fatalf("resolved placement = %+v, want relay edge", placement)
	}
}

func TestCanServeNodeAllowsActiveRelayEdge(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	callID := uuid.New()
	repo := &fakeMediaRepo{
		placement: &entity.MediaRoomPlacement{
			CallID:      callID,
			WorkspaceID: workspaceID,
			NodeID:      "origin-a",
		},
		relayEdges: []entity.MediaRelayEdge{{
			CallID:       callID,
			WorkspaceID:  workspaceID,
			SourceNodeID: "origin-a",
			TargetNodeID: "edge-b",
			Status:       entity.MediaRelayStatusActive,
			RoleScope:    entity.MediaRelayRoleScopeViewers,
		}},
	}
	svc := NewService(repo, nil, nil, Config{NodeID: "edge-b"})

	allowed, err := svc.CanServeNode(ctx, &entity.Call{ID: callID, WorkspaceID: workspaceID}, "edge-b")
	if err != nil {
		t.Fatalf("CanServeNode returned error: %v", err)
	}
	if !allowed {
		t.Fatalf("expected relay edge node to be allowed to serve call")
	}
}

func TestRecordQualitySnapshotCreatesMismatchAlert(t *testing.T) {
	repo := &fakeMediaRepo{}
	svc := NewService(repo, nil, nil, Config{})
	workspaceID := uuid.New()
	callID := uuid.New()
	userID := uuid.New()

	repo.samples = []entity.MediaQoSSample{{
		ID:              uuid.New(),
		WorkspaceID:     workspaceID,
		CallID:          callID,
		UserID:          userID,
		Source:          entity.MediaTelemetrySourceServer,
		MediaKind:       "video",
		PacketLossPct:   1,
		JitterMs:        8,
		RoundTripTimeMs: 90,
		SampledAt:       time.Now().UTC(),
	}}

	if err := svc.RecordQualitySnapshot(context.Background(), entity.MediaQoSSample{
		ID:              uuid.New(),
		WorkspaceID:     workspaceID,
		CallID:          callID,
		UserID:          userID,
		Source:          entity.MediaTelemetrySourceClient,
		MediaKind:       "video",
		PacketLossPct:   20,
		JitterMs:        120,
		RoundTripTimeMs: 900,
		SampledAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordQualitySnapshot returned error: %v", err)
	}
	if len(repo.alerts) == 0 {
		t.Fatalf("expected mismatch or degraded alert to be created")
	}
}

func TestUpdateAndReportQualityPolicy(t *testing.T) {
	repo := &fakeMediaRepo{}
	svc := NewService(repo, nil, nil, Config{})
	workspaceID := uuid.New()
	callID := uuid.New()
	actorID := uuid.New()

	policy, err := svc.UpdateCallQualityPolicy(context.Background(), &entity.MediaQualityPolicy{
		WorkspaceID:          workspaceID,
		CallID:               callID,
		Mode:                 entity.MediaQualityPolicyForceLow,
		MeetingWideDowngrade: true,
		AlertingEnabled:      true,
		UpdatedBy:            actorID,
	})
	if err != nil {
		t.Fatalf("UpdateCallQualityPolicy returned error: %v", err)
	}
	if policy.Mode != entity.MediaQualityPolicyForceLow {
		t.Fatalf("Mode = %q, want force_low", policy.Mode)
	}
	report, err := svc.GetCallQualityReport(context.Background(), workspaceID, callID, 20)
	if err != nil {
		t.Fatalf("GetCallQualityReport returned error: %v", err)
	}
	if report.Policy.Mode != entity.MediaQualityPolicyForceLow {
		t.Fatalf("report policy mode = %q, want force_low", report.Policy.Mode)
	}
}

func TestServerTelemetryDeltaAndDecisionPublishing(t *testing.T) {
	svc := NewService(&fakeMediaRepo{}, nil, nil, Config{
		ServerDrivenEnabled:     true,
		ServerDrivenMinInterval: time.Second,
	})
	placement := &entity.MediaRoomPlacement{
		CallID:      uuid.New(),
		WorkspaceID: uuid.New(),
	}
	userID := uuid.New()
	publisher := &capturingMediaPublisher{}
	svc.SetEventPublisher(publisher)

	first := sfu.PeerMediaTelemetry{
		MediaKind:     "video",
		BytesReceived: 1000,
		Metadata: map[string]any{
			"frames_received": 100,
			"frames_dropped":  4,
			"freeze_count":    1,
			"nack_count":      2,
			"pli_count":       1,
		},
	}
	second := sfu.PeerMediaTelemetry{
		MediaKind:     "video",
		BytesReceived: 7000,
		Metadata: map[string]any{
			"frames_received": 160,
			"frames_dropped":  10,
			"freeze_count":    3,
			"nack_count":      6,
			"pli_count":       2,
		},
	}

	if metrics := svc.serverTelemetryDelta(placement.CallID, userID.String(), "video", first, time.Now().UTC()); metrics.ObservedBitrateKbps != 0 {
		t.Fatalf("first ObservedBitrateKbps = %d, want 0 without a previous sample", metrics.ObservedBitrateKbps)
	}
	metrics := svc.serverTelemetryDelta(placement.CallID, userID.String(), "video", second, time.Now().UTC().Add(2*time.Second))
	if metrics.ObservedBitrateKbps <= 0 {
		t.Fatalf("ObservedBitrateKbps = %d, want > 0", metrics.ObservedBitrateKbps)
	}
	if metrics.FreezeCountDelta != 2 || metrics.NACKCountDelta != 4 || metrics.PLICountDelta != 1 {
		t.Fatalf("delta counters = %+v, want freeze=2 nack=4 pli=1", metrics)
	}

	decision := sfu.AdaptiveDecision{
		StreamID:            "camera",
		PreviousQuality:     sfu.QualityHigh,
		TargetQuality:       sfu.QualityLow,
		NetworkGrade:        sfu.NetworkGradePoor,
		Changed:             true,
		AudioPriority:       true,
		MaxVideoBitrateKbps: 250,
		MaxVideoFPS:         12,
		DecidedAt:           time.Now().UTC(),
	}
	policy := &entity.MediaQualityPolicy{ServerDrivenEnabled: true, ServerDrivenMinInterval: 500}
	if !svc.shouldPublishQualityDecision(placement.CallID, userID, decision, policy) {
		t.Fatalf("expected first server-driven decision to publish")
	}
	svc.publishQualityDecision(context.Background(), placement, userID, decision)
	if publisher.subject != "aloqa.signal."+userID.String() {
		t.Fatalf("subject = %q, want user signal subject", publisher.subject)
	}
	var evt event.Event
	if err := json.Unmarshal(publisher.data, &evt); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	payload, ok := evt.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", evt.Payload)
	}
	if payload["source"] != "server_telemetry" {
		t.Fatalf("payload source = %v, want server_telemetry", payload["source"])
	}
	if svc.shouldPublishQualityDecision(placement.CallID, userID, decision, policy) {
		t.Fatalf("expected duplicate server-driven decision to be suppressed")
	}
}

type fakeMediaRepo struct {
	placement  *entity.MediaRoomPlacement
	relayEdges []entity.MediaRelayEdge
	samples    []entity.MediaQoSSample
	policy     *entity.MediaQualityPolicy
	alerts     []entity.MediaQualityAlert
}

type capturingMediaPublisher struct {
	subject string
	data    []byte
}

func (p *capturingMediaPublisher) Publish(_ context.Context, subject string, data []byte) error {
	p.subject = subject
	p.data = append([]byte(nil), data...)
	return nil
}

func (r *fakeMediaRepo) UpsertPlacement(_ context.Context, placement *entity.MediaRoomPlacement) error {
	if placement == nil {
		return nil
	}
	cp := *placement
	r.placement = &cp
	return nil
}

func (r *fakeMediaRepo) GetPlacement(context.Context, uuid.UUID) (*entity.MediaRoomPlacement, error) {
	if r.placement == nil {
		return nil, cerrors.NotFound("media placement not found")
	}
	return r.placement, nil
}

func (r *fakeMediaRepo) ListPlacementsByWorkspace(_ context.Context, workspaceID uuid.UUID) ([]entity.MediaRoomPlacement, error) {
	if r.placement == nil || r.placement.WorkspaceID != workspaceID {
		return nil, nil
	}
	return []entity.MediaRoomPlacement{*r.placement}, nil
}

func (r *fakeMediaRepo) ReplaceRelayEdges(_ context.Context, callID uuid.UUID, edges []entity.MediaRelayEdge) error {
	r.relayEdges = r.relayEdges[:0]
	for _, edge := range edges {
		if edge.CallID != callID {
			continue
		}
		r.relayEdges = append(r.relayEdges, edge)
	}
	return nil
}

func (r *fakeMediaRepo) ListRelayEdgesByCall(_ context.Context, callID uuid.UUID) ([]entity.MediaRelayEdge, error) {
	var out []entity.MediaRelayEdge
	for _, edge := range r.relayEdges {
		if edge.CallID == callID {
			out = append(out, edge)
		}
	}
	return out, nil
}

func (r *fakeMediaRepo) ListRelayEdgesByWorkspace(_ context.Context, workspaceID uuid.UUID) ([]entity.MediaRelayEdge, error) {
	var out []entity.MediaRelayEdge
	for _, edge := range r.relayEdges {
		if edge.WorkspaceID == workspaceID {
			out = append(out, edge)
		}
	}
	return out, nil
}

func (r *fakeMediaRepo) AppendQoSSamples(_ context.Context, samples []entity.MediaQoSSample) error {
	r.samples = append(r.samples, samples...)
	return nil
}

func (r *fakeMediaRepo) ListQoSSamples(context.Context, uuid.UUID, uuid.UUID, int) ([]entity.MediaQoSSample, error) {
	return append([]entity.MediaQoSSample(nil), r.samples...), nil
}

func (r *fakeMediaRepo) SummarizeQoS(_ context.Context, workspaceID, callID uuid.UUID) (*entity.MediaQoSSummary, error) {
	return &entity.MediaQoSSummary{WorkspaceID: workspaceID, CallID: callID, SampleCount: len(r.samples)}, nil
}

func (r *fakeMediaRepo) UpsertQualityPolicy(_ context.Context, policy *entity.MediaQualityPolicy) error {
	if policy == nil {
		return nil
	}
	cp := *policy
	r.policy = &cp
	return nil
}

func (r *fakeMediaRepo) GetQualityPolicy(_ context.Context, workspaceID, callID uuid.UUID) (*entity.MediaQualityPolicy, error) {
	if r.policy == nil || r.policy.WorkspaceID != workspaceID || r.policy.CallID != callID {
		return nil, cerrors.NotFound("media quality policy not found")
	}
	return r.policy, nil
}

func (r *fakeMediaRepo) UpsertQualityAlert(_ context.Context, alert *entity.MediaQualityAlert) error {
	if alert == nil {
		return nil
	}
	for i := range r.alerts {
		if r.alerts[i].WorkspaceID == alert.WorkspaceID && r.alerts[i].CallID == alert.CallID && r.alerts[i].Kind == alert.Kind && r.alerts[i].Status == entity.MediaQualityAlertStatusActive {
			r.alerts[i] = *alert
			return nil
		}
	}
	r.alerts = append(r.alerts, *alert)
	return nil
}

func (r *fakeMediaRepo) ResolveQualityAlert(_ context.Context, workspaceID, callID uuid.UUID, kind string) error {
	for i := range r.alerts {
		if r.alerts[i].WorkspaceID == workspaceID && r.alerts[i].CallID == callID && r.alerts[i].Kind == kind {
			r.alerts[i].Status = entity.MediaQualityAlertStatusResolved
		}
	}
	return nil
}

func (r *fakeMediaRepo) ListQualityAlerts(_ context.Context, workspaceID, callID uuid.UUID, _ int) ([]entity.MediaQualityAlert, error) {
	var out []entity.MediaQualityAlert
	for _, alert := range r.alerts {
		if alert.WorkspaceID == workspaceID && alert.CallID == callID {
			out = append(out, alert)
		}
	}
	return out, nil
}
