package entity

import (
	"time"

	"github.com/google/uuid"
)

type QueueRuntimeStats struct {
	Name               string    `json:"name"`
	Pending            int64     `json:"pending"`
	Processing         int64     `json:"processing"`
	Failed             int64     `json:"failed"`
	Dead               int64     `json:"dead"`
	Published          int64     `json:"published,omitempty"`
	OldestPendingAgeMs int64     `json:"oldest_pending_age_ms"`
	ProcessedTotal     int64     `json:"processed_total"`
	FailedTotal        int64     `json:"failed_total"`
	DeadTotal          int64     `json:"dead_total"`
	LastBatchDuration  int64     `json:"last_batch_duration_ms"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type EventConsumerLag struct {
	ConsumerName      string    `json:"consumer_name"`
	StreamName        string    `json:"stream_name"`
	LastEventID       string    `json:"last_event_id,omitempty"`
	LastEventSequence int64     `json:"last_event_sequence"`
	Deliveries        int64     `json:"deliveries"`
	Failures          int64     `json:"failures"`
	Lag               int64     `json:"lag"`
	Status            string    `json:"status"`
	LastError         string    `json:"last_error,omitempty"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type WebSocketRuntimeStats struct {
	Reconnects           int64     `json:"reconnects"`
	RestoredSessions     int64     `json:"restored_sessions"`
	RestoredRooms        int64     `json:"restored_rooms"`
	ReplayEvents         int64     `json:"replay_events"`
	ReplayFailures       int64     `json:"replay_failures"`
	UnauthorizedRestores int64     `json:"unauthorized_restores"`
	DroppedMessages      int64     `json:"dropped_messages"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type RecordingPipelineHealth struct {
	ProcessedTotal   int64     `json:"processed_total"`
	FailedTotal      int64     `json:"failed_total"`
	DeletedTotal     int64     `json:"deleted_total"`
	TieredTotal      int64     `json:"tiered_total"`
	HookFailures     int64     `json:"hook_failures"`
	LastProcessingAt time.Time `json:"last_processing_at,omitempty"`
	LastCleanupAt    time.Time `json:"last_cleanup_at,omitempty"`
	LastLifecycleAt  time.Time `json:"last_lifecycle_at,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type CallQualityRuntimeStats struct {
	TrackedCalls       int       `json:"tracked_calls"`
	DegradedCalls      int       `json:"degraded_calls"`
	MismatchCalls      int       `json:"mismatch_calls"`
	AvgPacketLossPct   float64   `json:"avg_packet_loss_pct"`
	AvgJitterMs        float64   `json:"avg_jitter_ms"`
	AvgRoundTripTimeMs float64   `json:"avg_round_trip_time_ms"`
	WorstCallID        uuid.UUID `json:"worst_call_id,omitempty"`
	WorstPacketLossPct float64   `json:"worst_packet_loss_pct"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type WorkerRuntimeStats struct {
	Name              string    `json:"name"`
	LastHeartbeatAt   time.Time `json:"last_heartbeat_at,omitempty"`
	LastRunDurationMs int64     `json:"last_run_duration_ms"`
	ProcessedTotal    int64     `json:"processed_total"`
	FailedTotal       int64     `json:"failed_total"`
	DeadLettersTotal  int64     `json:"dead_letters_total"`
	LastError         string    `json:"last_error,omitempty"`
	Stalled           bool      `json:"stalled"`
}

type SLOStatus string

const (
	SLOStatusHealthy  SLOStatus = "healthy"
	SLOStatusWarning  SLOStatus = "warning"
	SLOStatusBreached SLOStatus = "breached"
)

type ObservabilitySLO struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Target      float64   `json:"target"`
	Current     float64   `json:"current"`
	Unit        string    `json:"unit"`
	Status      SLOStatus `json:"status"`
}

type ObservabilityAlertSeverity string

const (
	ObservabilityAlertWarning  ObservabilityAlertSeverity = "warning"
	ObservabilityAlertCritical ObservabilityAlertSeverity = "critical"
)

type OperationalAlert struct {
	Key       string                     `json:"key"`
	Component string                     `json:"component"`
	Severity  ObservabilityAlertSeverity `json:"severity"`
	Message   string                     `json:"message"`
	UpdatedAt time.Time                  `json:"updated_at"`
}

type ObservabilityDashboard struct {
	GeneratedAt   time.Time               `json:"generated_at"`
	CallQuality   CallQualityRuntimeStats `json:"call_quality"`
	RealtimeQueue QueueRuntimeStats       `json:"realtime_queue"`
	SearchQueue   QueueRuntimeStats       `json:"search_queue"`
	Consumers     []EventConsumerLag      `json:"consumers"`
	WebSocket     WebSocketRuntimeStats   `json:"websocket"`
	Recording     RecordingPipelineHealth `json:"recording"`
	Workers       []WorkerRuntimeStats    `json:"workers"`
	Storage       StorageRuntimeReport    `json:"storage"`
	SLOs          []ObservabilitySLO      `json:"slos"`
	Alerts        []OperationalAlert      `json:"alerts"`
}
