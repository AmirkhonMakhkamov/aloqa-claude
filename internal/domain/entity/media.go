package entity

import (
	"time"

	"github.com/google/uuid"
)

type MediaNodeStatus string

const (
	MediaNodeStatusActive     MediaNodeStatus = "active"
	MediaNodeStatusDraining   MediaNodeStatus = "draining"
	MediaNodeStatusOverloaded MediaNodeStatus = "overloaded"
	MediaNodeStatusUnknown    MediaNodeStatus = "unknown"
)

type MediaRoutingMode string

const (
	MediaRoutingStickyEdge   MediaRoutingMode = "sticky_edge"
	MediaRoutingRegionalEdge MediaRoutingMode = "regional_edge"
)

type MediaOverflowPolicy string

const (
	MediaOverflowReject       MediaOverflowPolicy = "reject"
	MediaOverflowRegionalMove MediaOverflowPolicy = "regional_spillover"
	MediaOverflowWebinarEdge  MediaOverflowPolicy = "webinar_fanout"
)

type MediaFanoutStrategy string

const (
	MediaFanoutSingleNode      MediaFanoutStrategy = "single_node"
	MediaFanoutRegionalCascade MediaFanoutStrategy = "regional_cascade"
	MediaFanoutWebinarEdges    MediaFanoutStrategy = "webinar_edges"
)

type MediaRelayStatus string

const (
	MediaRelayStatusActive   MediaRelayStatus = "active"
	MediaRelayStatusDraining MediaRelayStatus = "draining"
	MediaRelayStatusFailed   MediaRelayStatus = "failed"
)

type MediaRelayRoleScope string

const (
	MediaRelayRoleScopeViewers MediaRelayRoleScope = "viewers"
	MediaRelayRoleScopeAll     MediaRelayRoleScope = "all"
)

type MediaScreenSharePriority string

const (
	MediaScreenShareBalanced  MediaScreenSharePriority = "balanced"
	MediaScreenShareProtected MediaScreenSharePriority = "protected"
)

type MediaTelemetrySource string

const (
	MediaTelemetrySourceServer MediaTelemetrySource = "server"
	MediaTelemetrySourceClient MediaTelemetrySource = "client"
)

type MediaQualityPolicyMode string

const (
	MediaQualityPolicyAuto              MediaQualityPolicyMode = "auto"
	MediaQualityPolicyConserveBandwidth MediaQualityPolicyMode = "conserve_bandwidth"
	MediaQualityPolicyForceLow          MediaQualityPolicyMode = "force_low"
	MediaQualityPolicyAudioOnly         MediaQualityPolicyMode = "audio_only"
)

type MediaQualityAlertSeverity string

const (
	MediaQualityAlertSeverityWarning  MediaQualityAlertSeverity = "warning"
	MediaQualityAlertSeverityCritical MediaQualityAlertSeverity = "critical"
)

type MediaQualityAlertStatus string

const (
	MediaQualityAlertStatusActive   MediaQualityAlertStatus = "active"
	MediaQualityAlertStatusResolved MediaQualityAlertStatus = "resolved"
)

type MediaCallPolicy struct {
	MaxParticipants     int                      `json:"max_participants"`
	MaxPresenters       int                      `json:"max_presenters"`
	MaxViewers          int                      `json:"max_viewers"`
	RoutingMode         MediaRoutingMode         `json:"routing_mode"`
	FanoutStrategy      MediaFanoutStrategy      `json:"fanout_strategy"`
	OverflowPolicy      MediaOverflowPolicy      `json:"overflow_policy"`
	ScreenSharePriority MediaScreenSharePriority `json:"screen_share_priority"`
	TURNStrategy        string                   `json:"turn_strategy"`
	Sticky              bool                     `json:"sticky"`
}

type MediaRoomPlacement struct {
	CallID              uuid.UUID                `json:"call_id"`
	WorkspaceID         uuid.UUID                `json:"workspace_id"`
	NodeID              string                   `json:"node_id"`
	Region              string                   `json:"region"`
	ControlURL          string                   `json:"control_url"`
	MediaURL            string                   `json:"media_url"`
	RoutingMode         MediaRoutingMode         `json:"routing_mode"`
	FanoutStrategy      MediaFanoutStrategy      `json:"fanout_strategy"`
	OverflowPolicy      MediaOverflowPolicy      `json:"overflow_policy"`
	ScreenSharePriority MediaScreenSharePriority `json:"screen_share_priority"`
	TURNStrategy        string                   `json:"turn_strategy"`
	Sticky              bool                     `json:"sticky"`
	MaxParticipants     int                      `json:"max_participants"`
	MaxPresenters       int                      `json:"max_presenters"`
	MaxViewers          int                      `json:"max_viewers"`
	Metadata            map[string]any           `json:"metadata,omitempty"`
	AssignedAt          time.Time                `json:"assigned_at"`
	UpdatedAt           time.Time                `json:"updated_at"`
}

type MediaRelayEdge struct {
	CallID          uuid.UUID           `json:"call_id"`
	WorkspaceID     uuid.UUID           `json:"workspace_id"`
	SourceNodeID    string              `json:"source_node_id"`
	TargetNodeID    string              `json:"target_node_id"`
	TargetRegion    string              `json:"target_region"`
	ControlURL      string              `json:"control_url"`
	MediaURL        string              `json:"media_url"`
	FanoutStrategy  MediaFanoutStrategy `json:"fanout_strategy"`
	RoleScope       MediaRelayRoleScope `json:"role_scope"`
	Status          MediaRelayStatus    `json:"status"`
	Sticky          bool                `json:"sticky"`
	MaxParticipants int                 `json:"max_participants"`
	Priority        int                 `json:"priority"`
	Metadata        map[string]any      `json:"metadata,omitempty"`
	AssignedAt      time.Time           `json:"assigned_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
}

type MediaNodeSnapshot struct {
	NodeID          string          `json:"node_id"`
	Region          string          `json:"region"`
	ControlURL      string          `json:"control_url"`
	MediaURL        string          `json:"media_url"`
	Status          MediaNodeStatus `json:"status"`
	MaxRooms        int             `json:"max_rooms"`
	Rooms           int             `json:"rooms"`
	Presenters      int             `json:"presenters"`
	Viewers         int             `json:"viewers"`
	Tracks          int             `json:"tracks"`
	SimulcastTracks int             `json:"simulcast_tracks"`
	LoadScore       float64         `json:"load_score"`
	LastHeartbeatAt time.Time       `json:"last_heartbeat_at"`
	Capabilities    []string        `json:"capabilities,omitempty"`
}

type MediaQoSSample struct {
	ID                           uuid.UUID            `json:"id"`
	WorkspaceID                  uuid.UUID            `json:"workspace_id"`
	CallID                       uuid.UUID            `json:"call_id"`
	UserID                       uuid.UUID            `json:"user_id"`
	NodeID                       string               `json:"node_id"`
	Region                       string               `json:"region"`
	StreamID                     string               `json:"stream_id,omitempty"`
	Source                       MediaTelemetrySource `json:"source"`
	ParticipantRole              string               `json:"participant_role,omitempty"`
	MediaKind                    string               `json:"media_kind,omitempty"`
	PacketLossPct                float64              `json:"packet_loss_pct"`
	JitterMs                     float64              `json:"jitter_ms"`
	RoundTripTimeMs              float64              `json:"round_trip_time_ms"`
	AvailableOutgoingBitrateKbps int                  `json:"available_outgoing_bitrate_kbps"`
	AvailableIncomingBitrateKbps int                  `json:"available_incoming_bitrate_kbps"`
	BytesSent                    int64                `json:"bytes_sent"`
	BytesReceived                int64                `json:"bytes_received"`
	Metadata                     map[string]any       `json:"metadata,omitempty"`
	SampledAt                    time.Time            `json:"sampled_at"`
}

type MediaQoSSummary struct {
	WorkspaceID        uuid.UUID `json:"workspace_id"`
	CallID             uuid.UUID `json:"call_id"`
	SampleCount        int       `json:"sample_count"`
	LastSampledAt      time.Time `json:"last_sampled_at"`
	AvgPacketLossPct   float64   `json:"avg_packet_loss_pct"`
	MaxPacketLossPct   float64   `json:"max_packet_loss_pct"`
	AvgJitterMs        float64   `json:"avg_jitter_ms"`
	MaxJitterMs        float64   `json:"max_jitter_ms"`
	AvgRoundTripTimeMs float64   `json:"avg_round_trip_time_ms"`
	MaxRoundTripTimeMs float64   `json:"max_round_trip_time_ms"`
}

type MediaQualityPolicy struct {
	WorkspaceID             uuid.UUID              `json:"workspace_id"`
	CallID                  uuid.UUID              `json:"call_id"`
	Mode                    MediaQualityPolicyMode `json:"mode"`
	AlertPacketLossPct      float64                `json:"alert_packet_loss_pct"`
	AlertJitterMs           float64                `json:"alert_jitter_ms"`
	AlertRoundTripTimeMs    float64                `json:"alert_round_trip_time_ms"`
	CorrelationTolerancePct float64                `json:"correlation_tolerance_pct"`
	CorrelationToleranceMs  float64                `json:"correlation_tolerance_ms"`
	ServerDrivenEnabled     bool                   `json:"server_driven_enabled"`
	ServerDrivenMinInterval int                    `json:"server_driven_min_interval_ms"`
	MeetingWideDowngrade    bool                   `json:"meeting_wide_downgrade"`
	AlertingEnabled         bool                   `json:"alerting_enabled"`
	UpdatedBy               uuid.UUID              `json:"updated_by,omitempty"`
	UpdatedAt               time.Time              `json:"updated_at"`
}

type MediaQualityAlert struct {
	ID          uuid.UUID                 `json:"id"`
	WorkspaceID uuid.UUID                 `json:"workspace_id"`
	CallID      uuid.UUID                 `json:"call_id"`
	Kind        string                    `json:"kind"`
	Severity    MediaQualityAlertSeverity `json:"severity"`
	Status      MediaQualityAlertStatus   `json:"status"`
	Message     string                    `json:"message"`
	Metadata    map[string]any            `json:"metadata,omitempty"`
	CreatedAt   time.Time                 `json:"created_at"`
	UpdatedAt   time.Time                 `json:"updated_at"`
	ResolvedAt  *time.Time                `json:"resolved_at,omitempty"`
}

type MediaQualityCorrelation struct {
	UserID               uuid.UUID       `json:"user_id"`
	MediaKind            string          `json:"media_kind"`
	ServerSample         *MediaQoSSample `json:"server_sample,omitempty"`
	ClientSample         *MediaQoSSample `json:"client_sample,omitempty"`
	PacketLossDeltaPct   float64         `json:"packet_loss_delta_pct"`
	JitterDeltaMs        float64         `json:"jitter_delta_ms"`
	RoundTripTimeDeltaMs float64         `json:"round_trip_time_delta_ms"`
	SampleSkewMs         float64         `json:"sample_skew_ms"`
	Healthy              bool            `json:"healthy"`
}

type CallQoSHistory struct {
	WorkspaceID uuid.UUID        `json:"workspace_id"`
	CallID      uuid.UUID        `json:"call_id"`
	Summary     MediaQoSSummary  `json:"summary"`
	Samples     []MediaQoSSample `json:"samples"`
}

type CallQualityReport struct {
	WorkspaceID  uuid.UUID                 `json:"workspace_id"`
	CallID       uuid.UUID                 `json:"call_id"`
	Policy       MediaQualityPolicy        `json:"policy"`
	Server       MediaQoSSummary           `json:"server"`
	Client       MediaQoSSummary           `json:"client"`
	Correlations []MediaQualityCorrelation `json:"correlations,omitempty"`
	Alerts       []MediaQualityAlert       `json:"alerts,omitempty"`
	Snapshots    []MediaQoSSample          `json:"snapshots,omitempty"`
}

type WorkspaceMediaTopology struct {
	WorkspaceID uuid.UUID            `json:"workspace_id"`
	Nodes       []MediaNodeSnapshot  `json:"nodes"`
	Placements  []MediaRoomPlacement `json:"placements"`
	RelayEdges  []MediaRelayEdge     `json:"relay_edges,omitempty"`
}
