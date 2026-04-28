package observability

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
)

type StorageProvider interface {
	RuntimeReport(ctx context.Context) (*entity.StorageRuntimeReport, error)
}

type QueueStatsProvider interface {
	QueueStats(ctx context.Context) (*entity.QueueRuntimeStats, error)
}

type ConsumerLagProvider interface {
	ListConsumerLag(ctx context.Context, limit int) ([]entity.EventConsumerLag, error)
}

type Config struct {
	Namespace                   string
	EventLagWarn                time.Duration
	EventLagCritical            time.Duration
	DeadLetterWarn              int64
	DeadLetterCritical          int64
	DBUtilizationWarnPct        float64
	DBUtilizationCriticalPct    float64
	RedisUtilizationWarnPct     float64
	RedisUtilizationCriticalPct float64
	WorkerStallAfter            time.Duration
	CallDegradedWarnRatio       float64
	CallDegradedCriticalRatio   float64
	ReplaySuccessTargetPct      float64
	RecordingSuccessTargetPct   float64
	ConsumerLagWarn             int64
	ConsumerLagCritical         int64
}

type Service struct {
	storage   StorageProvider
	realtime  QueueStatsProvider
	search    QueueStatsProvider
	consumers ConsumerLagProvider
	config    Config

	mu sync.RWMutex

	realtimeTotals queueTotals
	searchTotals   queueTotals
	ws             entity.WebSocketRuntimeStats
	recording      entity.RecordingPipelineHealth
	workers        map[string]*entity.WorkerRuntimeStats
	callQuality    map[uuid.UUID]callQualityState
}

type queueTotals struct {
	ProcessedTotal    int64
	FailedTotal       int64
	DeadTotal         int64
	LastBatchDuration int64
	UpdatedAt         time.Time
	LastError         string
}

type callQualityState struct {
	WorkspaceID uuid.UUID
	Server      entity.MediaQoSSummary
	Client      entity.MediaQoSSummary
	Degraded    bool
	Mismatch    bool
	UpdatedAt   time.Time
}

func NewService(cfg Config) *Service {
	if cfg.Namespace == "" {
		cfg.Namespace = "aloqa"
	}
	if cfg.EventLagWarn <= 0 {
		cfg.EventLagWarn = 15 * time.Second
	}
	if cfg.EventLagCritical <= 0 {
		cfg.EventLagCritical = time.Minute
	}
	if cfg.DeadLetterWarn <= 0 {
		cfg.DeadLetterWarn = 10
	}
	if cfg.DeadLetterCritical <= 0 {
		cfg.DeadLetterCritical = 50
	}
	if cfg.DBUtilizationWarnPct <= 0 {
		cfg.DBUtilizationWarnPct = 75
	}
	if cfg.DBUtilizationCriticalPct <= 0 {
		cfg.DBUtilizationCriticalPct = 90
	}
	if cfg.RedisUtilizationWarnPct <= 0 {
		cfg.RedisUtilizationWarnPct = 75
	}
	if cfg.RedisUtilizationCriticalPct <= 0 {
		cfg.RedisUtilizationCriticalPct = 90
	}
	if cfg.WorkerStallAfter <= 0 {
		cfg.WorkerStallAfter = 2 * time.Minute
	}
	if cfg.CallDegradedWarnRatio <= 0 {
		cfg.CallDegradedWarnRatio = 5
	}
	if cfg.CallDegradedCriticalRatio <= 0 {
		cfg.CallDegradedCriticalRatio = 15
	}
	if cfg.ReplaySuccessTargetPct <= 0 {
		cfg.ReplaySuccessTargetPct = 99
	}
	if cfg.RecordingSuccessTargetPct <= 0 {
		cfg.RecordingSuccessTargetPct = 99
	}
	if cfg.ConsumerLagWarn <= 0 {
		cfg.ConsumerLagWarn = 250
	}
	if cfg.ConsumerLagCritical <= 0 {
		cfg.ConsumerLagCritical = 1000
	}
	return &Service{
		config:      cfg,
		workers:     make(map[string]*entity.WorkerRuntimeStats),
		callQuality: make(map[uuid.UUID]callQualityState),
	}
}

func (s *Service) SetStorageProvider(provider StorageProvider) {
	s.storage = provider
}

func (s *Service) SetRealtimeProvider(provider QueueStatsProvider, consumers ConsumerLagProvider) {
	s.realtime = provider
	s.consumers = consumers
}

func (s *Service) SetSearchProvider(provider QueueStatsProvider) {
	s.search = provider
}

func (s *Service) RecordRealtimeBatch(processed, failed, dead int, duration time.Duration, err error) {
	s.recordQueueBatch("realtime_outbox", &s.realtimeTotals, processed, failed, dead, duration, err)
}

func (s *Service) RecordSearchBatch(processed, failed, dead int, duration time.Duration, err error) {
	s.recordQueueBatch("search_indexer", &s.searchTotals, processed, failed, dead, duration, err)
}

func (s *Service) RecordWSRestore(restoredRooms, replayedEvents, unauthorized int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.ws.Reconnects++
	s.ws.RestoredSessions++
	s.ws.RestoredRooms += int64(restoredRooms)
	s.ws.ReplayEvents += int64(replayedEvents)
	s.ws.UnauthorizedRestores += int64(unauthorized)
	s.ws.UpdatedAt = now
}

func (s *Service) RecordWSReplayFailure() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ws.ReplayFailures++
	s.ws.UpdatedAt = time.Now().UTC()
}

func (s *Service) RecordWSDropped() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ws.DroppedMessages++
	s.ws.UpdatedAt = time.Now().UTC()
}

func (s *Service) RecordCallQualityAggregate(workspaceID, callID uuid.UUID, server, client entity.MediaQoSSummary, degraded, mismatch bool) {
	if callID == uuid.Nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callQuality[callID] = callQualityState{
		WorkspaceID: workspaceID,
		Server:      server,
		Client:      client,
		Degraded:    degraded,
		Mismatch:    mismatch,
		UpdatedAt:   time.Now().UTC(),
	}
}

func (s *Service) RecordRecordingRun(workerName string, processed, failed, deleted, tiered int, duration time.Duration, err error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	worker := s.ensureWorkerLocked(workerName)
	worker.LastHeartbeatAt = now
	worker.LastRunDurationMs = duration.Milliseconds()
	worker.ProcessedTotal += int64(processed + deleted + tiered)
	worker.FailedTotal += int64(failed)
	if err != nil {
		worker.LastError = err.Error()
	} else {
		worker.LastError = ""
	}

	s.recording.ProcessedTotal += int64(processed)
	s.recording.FailedTotal += int64(failed)
	s.recording.DeletedTotal += int64(deleted)
	s.recording.TieredTotal += int64(tiered)
	s.recording.UpdatedAt = now
	if workerName == "recording_processing" {
		s.recording.LastProcessingAt = now
	}
	if workerName == "recording_cleanup" {
		s.recording.LastCleanupAt = now
	}
	if workerName == "recording_lifecycle" {
		s.recording.LastLifecycleAt = now
	}
	if err != nil {
		s.recording.LastError = err.Error()
	}
}

func (s *Service) RecordRecordingHookFailure(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recording.HookFailures++
	s.recording.UpdatedAt = time.Now().UTC()
	if err != nil {
		s.recording.LastError = err.Error()
	}
}

func (s *Service) RecordWorkerHeartbeat(name string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	worker := s.ensureWorkerLocked(name)
	worker.LastHeartbeatAt = time.Now().UTC()
}

func (s *Service) Dashboard(ctx context.Context) (*entity.ObservabilityDashboard, error) {
	now := time.Now().UTC()
	storageReport := entity.StorageRuntimeReport{}
	if s.storage != nil {
		report, err := s.storage.RuntimeReport(ctx)
		if err != nil {
			return nil, err
		}
		if report != nil {
			storageReport = *report
		}
	}

	realtimeStats := entity.QueueRuntimeStats{Name: "realtime_outbox"}
	if s.realtime != nil {
		stats, err := s.realtime.QueueStats(ctx)
		if err != nil {
			return nil, err
		}
		if stats != nil {
			realtimeStats = *stats
		}
	}
	searchStats := entity.QueueRuntimeStats{Name: "search_indexer"}
	if s.search != nil {
		stats, err := s.search.QueueStats(ctx)
		if err != nil {
			return nil, err
		}
		if stats != nil {
			searchStats = *stats
		}
	}
	consumers := []entity.EventConsumerLag{}
	if s.consumers != nil {
		items, err := s.consumers.ListConsumerLag(ctx, 50)
		if err != nil {
			return nil, err
		}
		consumers = items
	}

	s.mu.RLock()
	realtimeTotals := s.realtimeTotals
	searchTotals := s.searchTotals
	wsStats := s.ws
	recording := s.recording
	workers := make([]entity.WorkerRuntimeStats, 0, len(s.workers))
	for _, worker := range s.workers {
		copied := *worker
		if !copied.LastHeartbeatAt.IsZero() && now.Sub(copied.LastHeartbeatAt) > s.config.WorkerStallAfter {
			copied.Stalled = true
		}
		workers = append(workers, copied)
	}
	callStates := make([]callQualityState, 0, len(s.callQuality))
	for _, state := range s.callQuality {
		callStates = append(callStates, state)
	}
	s.mu.RUnlock()

	realtimeStats.ProcessedTotal += realtimeTotals.ProcessedTotal
	realtimeStats.FailedTotal += realtimeTotals.FailedTotal
	realtimeStats.DeadTotal += realtimeTotals.DeadTotal
	if realtimeTotals.LastBatchDuration > 0 {
		realtimeStats.LastBatchDuration = realtimeTotals.LastBatchDuration
	}
	if realtimeTotals.UpdatedAt.After(realtimeStats.UpdatedAt) {
		realtimeStats.UpdatedAt = realtimeTotals.UpdatedAt
	}

	searchStats.ProcessedTotal += searchTotals.ProcessedTotal
	searchStats.FailedTotal += searchTotals.FailedTotal
	searchStats.DeadTotal += searchTotals.DeadTotal
	if searchTotals.LastBatchDuration > 0 {
		searchStats.LastBatchDuration = searchTotals.LastBatchDuration
	}
	if searchTotals.UpdatedAt.After(searchStats.UpdatedAt) {
		searchStats.UpdatedAt = searchTotals.UpdatedAt
	}

	callQuality := summarizeCallQuality(callStates)
	slos := s.computeSLOs(callQuality, realtimeStats, wsStats, recording, storageReport, consumers)
	alerts := s.computeAlerts(now, callQuality, realtimeStats, searchStats, wsStats, recording, storageReport, workers, consumers)

	sort.Slice(workers, func(i, j int) bool {
		return workers[i].Name < workers[j].Name
	})
	sort.Slice(consumers, func(i, j int) bool {
		if consumers[i].Lag == consumers[j].Lag {
			return consumers[i].ConsumerName < consumers[j].ConsumerName
		}
		return consumers[i].Lag > consumers[j].Lag
	})

	return &entity.ObservabilityDashboard{
		GeneratedAt:   now,
		CallQuality:   callQuality,
		RealtimeQueue: realtimeStats,
		SearchQueue:   searchStats,
		Consumers:     consumers,
		WebSocket:     wsStats,
		Recording:     recording,
		Workers:       workers,
		Storage:       storageReport,
		SLOs:          slos,
		Alerts:        alerts,
	}, nil
}

func (s *Service) Alerts(ctx context.Context) ([]entity.OperationalAlert, error) {
	dashboard, err := s.Dashboard(ctx)
	if err != nil {
		return nil, err
	}
	return dashboard.Alerts, nil
}

func (s *Service) SLOs(ctx context.Context) ([]entity.ObservabilitySLO, error) {
	dashboard, err := s.Dashboard(ctx)
	if err != nil {
		return nil, err
	}
	return dashboard.SLOs, nil
}

func (s *Service) Metrics(ctx context.Context) (string, error) {
	dashboard, err := s.Dashboard(ctx)
	if err != nil {
		return "", err
	}

	ns := sanitizeMetricName(s.config.Namespace)
	var b strings.Builder
	writeGauge := func(name string, value any) {
		fmt.Fprintf(&b, "%s_%s %v\n", ns, name, value)
	}

	writeGauge("call_quality_tracked_calls", dashboard.CallQuality.TrackedCalls)
	writeGauge("call_quality_degraded_calls", dashboard.CallQuality.DegradedCalls)
	writeGauge("call_quality_mismatch_calls", dashboard.CallQuality.MismatchCalls)
	writeGauge("call_quality_avg_packet_loss_pct", fmt.Sprintf("%.2f", dashboard.CallQuality.AvgPacketLossPct))
	writeGauge("call_quality_avg_jitter_ms", fmt.Sprintf("%.2f", dashboard.CallQuality.AvgJitterMs))
	writeGauge("call_quality_avg_rtt_ms", fmt.Sprintf("%.2f", dashboard.CallQuality.AvgRoundTripTimeMs))

	writeGauge("realtime_queue_pending", dashboard.RealtimeQueue.Pending)
	writeGauge("realtime_queue_processing", dashboard.RealtimeQueue.Processing)
	writeGauge("realtime_queue_failed", dashboard.RealtimeQueue.Failed)
	writeGauge("realtime_queue_dead", dashboard.RealtimeQueue.Dead)
	writeGauge("realtime_queue_oldest_pending_age_ms", dashboard.RealtimeQueue.OldestPendingAgeMs)
	writeGauge("search_queue_pending", dashboard.SearchQueue.Pending)
	writeGauge("search_queue_processing", dashboard.SearchQueue.Processing)
	writeGauge("search_queue_failed", dashboard.SearchQueue.Failed)
	writeGauge("search_queue_dead", dashboard.SearchQueue.Dead)
	writeGauge("search_queue_oldest_pending_age_ms", dashboard.SearchQueue.OldestPendingAgeMs)

	writeGauge("db_pool_utilization_pct", fmt.Sprintf("%.2f", dashboard.Storage.Postgres.AcquiredConnsPct))
	writeGauge("db_pool_saturated", boolToInt(dashboard.Storage.Postgres.Saturated))
	writeGauge("redis_pool_utilization_pct", fmt.Sprintf("%.2f", dashboard.Storage.Redis.UtilizationPct))
	writeGauge("redis_pool_saturated", boolToInt(dashboard.Storage.Redis.Saturated))

	writeGauge("ws_reconnect_total", dashboard.WebSocket.Reconnects)
	writeGauge("ws_restored_sessions_total", dashboard.WebSocket.RestoredSessions)
	writeGauge("ws_restored_rooms_total", dashboard.WebSocket.RestoredRooms)
	writeGauge("ws_replay_events_total", dashboard.WebSocket.ReplayEvents)
	writeGauge("ws_replay_failures_total", dashboard.WebSocket.ReplayFailures)
	writeGauge("ws_dropped_messages_total", dashboard.WebSocket.DroppedMessages)

	writeGauge("recording_processed_total", dashboard.Recording.ProcessedTotal)
	writeGauge("recording_failed_total", dashboard.Recording.FailedTotal)
	writeGauge("recording_deleted_total", dashboard.Recording.DeletedTotal)
	writeGauge("recording_tiered_total", dashboard.Recording.TieredTotal)
	writeGauge("recording_hook_failures_total", dashboard.Recording.HookFailures)

	for _, worker := range dashboard.Workers {
		label := sanitizeMetricName(worker.Name)
		fmt.Fprintf(&b, "%s_worker_stalled{name=\"%s\"} %d\n", ns, label, boolToInt(worker.Stalled))
		fmt.Fprintf(&b, "%s_worker_last_run_duration_ms{name=\"%s\"} %d\n", ns, label, worker.LastRunDurationMs)
	}
	for _, consumer := range dashboard.Consumers {
		label := sanitizeMetricName(consumer.ConsumerName)
		fmt.Fprintf(&b, "%s_consumer_lag{name=\"%s\"} %d\n", ns, label, consumer.Lag)
	}
	for _, slo := range dashboard.SLOs {
		label := sanitizeMetricName(slo.Name)
		fmt.Fprintf(&b, "%s_slo_status{name=\"%s\"} %d\n", ns, label, sloStatusValue(slo.Status))
		fmt.Fprintf(&b, "%s_slo_current{name=\"%s\"} %.2f\n", ns, label, slo.Current)
	}
	return b.String(), nil
}

func (s *Service) recordQueueBatch(workerName string, totals *queueTotals, processed, failed, dead int, duration time.Duration, err error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	totals.ProcessedTotal += int64(processed)
	totals.FailedTotal += int64(failed)
	totals.DeadTotal += int64(dead)
	totals.LastBatchDuration = duration.Milliseconds()
	totals.UpdatedAt = now
	if err != nil {
		totals.LastError = err.Error()
	} else {
		totals.LastError = ""
	}
	worker := s.ensureWorkerLocked(workerName)
	worker.LastHeartbeatAt = now
	worker.LastRunDurationMs = duration.Milliseconds()
	worker.ProcessedTotal += int64(processed)
	worker.FailedTotal += int64(failed)
	worker.DeadLettersTotal += int64(dead)
	if err != nil {
		worker.LastError = err.Error()
	} else {
		worker.LastError = ""
	}
}

func (s *Service) ensureWorkerLocked(name string) *entity.WorkerRuntimeStats {
	worker, ok := s.workers[name]
	if !ok {
		worker = &entity.WorkerRuntimeStats{Name: name}
		s.workers[name] = worker
	}
	return worker
}

func summarizeCallQuality(states []callQualityState) entity.CallQualityRuntimeStats {
	var result entity.CallQualityRuntimeStats
	var totalPacketLoss, totalJitter, totalRTT float64
	for callID, state := range mapFromStates(states) {
		summary := state.Server
		if summary.SampleCount == 0 {
			summary = state.Client
		}
		if summary.SampleCount == 0 {
			continue
		}
		result.TrackedCalls++
		totalPacketLoss += summary.AvgPacketLossPct
		totalJitter += summary.AvgJitterMs
		totalRTT += summary.AvgRoundTripTimeMs
		if state.Degraded {
			result.DegradedCalls++
		}
		if state.Mismatch {
			result.MismatchCalls++
		}
		if summary.MaxPacketLossPct >= result.WorstPacketLossPct {
			result.WorstPacketLossPct = summary.MaxPacketLossPct
			result.WorstCallID = callID
		}
		if state.UpdatedAt.After(result.UpdatedAt) {
			result.UpdatedAt = state.UpdatedAt
		}
	}
	if result.TrackedCalls > 0 {
		div := float64(result.TrackedCalls)
		result.AvgPacketLossPct = totalPacketLoss / div
		result.AvgJitterMs = totalJitter / div
		result.AvgRoundTripTimeMs = totalRTT / div
	}
	return result
}

func mapFromStates(states []callQualityState) map[uuid.UUID]callQualityState {
	result := make(map[uuid.UUID]callQualityState, len(states))
	for _, state := range states {
		callID := state.Server.CallID
		if callID == uuid.Nil {
			callID = state.Client.CallID
		}
		if callID == uuid.Nil {
			continue
		}
		result[callID] = state
	}
	return result
}

func (s *Service) computeSLOs(callQuality entity.CallQualityRuntimeStats, realtimeStats entity.QueueRuntimeStats, ws entity.WebSocketRuntimeStats, recording entity.RecordingPipelineHealth, storage entity.StorageRuntimeReport, consumers []entity.EventConsumerLag) []entity.ObservabilitySLO {
	callRatio := 0.0
	if callQuality.TrackedCalls > 0 {
		callRatio = (float64(callQuality.DegradedCalls) / float64(callQuality.TrackedCalls)) * 100
	}
	replaySuccess := 100.0
	if ws.Reconnects > 0 {
		failures := ws.ReplayFailures + ws.UnauthorizedRestores
		replaySuccess = 100 - (float64(failures)/float64(ws.Reconnects))*100
	}
	recordingSuccess := 100.0
	if denom := recording.ProcessedTotal + recording.FailedTotal; denom > 0 {
		recordingSuccess = (float64(recording.ProcessedTotal) / float64(denom)) * 100
	}
	maxLag := float64(realtimeStats.OldestPendingAgeMs)
	maxConsumerLag := int64(0)
	for _, consumer := range consumers {
		if consumer.Lag > maxConsumerLag {
			maxConsumerLag = consumer.Lag
		}
	}

	return []entity.ObservabilitySLO{
		{
			Name:        "call_quality_degraded_ratio",
			Description: "Percent of tracked calls currently above degraded quality thresholds",
			Target:      s.config.CallDegradedWarnRatio,
			Current:     callRatio,
			Unit:        "percent",
			Status:      thresholdStatus(callRatio, s.config.CallDegradedWarnRatio, s.config.CallDegradedCriticalRatio),
		},
		{
			Name:        "event_lag_ms",
			Description: "Oldest pending durable event lag in milliseconds",
			Target:      float64(s.config.EventLagWarn.Milliseconds()),
			Current:     maxLag,
			Unit:        "ms",
			Status:      thresholdStatus(maxLag, float64(s.config.EventLagWarn.Milliseconds()), float64(s.config.EventLagCritical.Milliseconds())),
		},
		{
			Name:        "consumer_lag_events",
			Description: "Largest observed consumer lag in durable event stream",
			Target:      float64(s.config.ConsumerLagWarn),
			Current:     float64(maxConsumerLag),
			Unit:        "events",
			Status:      thresholdStatus(float64(maxConsumerLag), float64(s.config.ConsumerLagWarn), float64(s.config.ConsumerLagCritical)),
		},
		{
			Name:        "ws_replay_success_rate",
			Description: "Successful reconnect/replay recovery rate",
			Target:      s.config.ReplaySuccessTargetPct,
			Current:     replaySuccess,
			Unit:        "percent",
			Status:      inverseThresholdStatus(replaySuccess, s.config.ReplaySuccessTargetPct, s.config.ReplaySuccessTargetPct-5),
		},
		{
			Name:        "recording_pipeline_success_rate",
			Description: "Recording processing success rate",
			Target:      s.config.RecordingSuccessTargetPct,
			Current:     recordingSuccess,
			Unit:        "percent",
			Status:      inverseThresholdStatus(recordingSuccess, s.config.RecordingSuccessTargetPct, s.config.RecordingSuccessTargetPct-10),
		},
		{
			Name:        "db_pool_utilization",
			Description: "Postgres pool utilization percentage",
			Target:      s.config.DBUtilizationWarnPct,
			Current:     storage.Postgres.AcquiredConnsPct,
			Unit:        "percent",
			Status:      thresholdStatus(storage.Postgres.AcquiredConnsPct, s.config.DBUtilizationWarnPct, s.config.DBUtilizationCriticalPct),
		},
		{
			Name:        "redis_pool_utilization",
			Description: "Redis pool utilization percentage",
			Target:      s.config.RedisUtilizationWarnPct,
			Current:     storage.Redis.UtilizationPct,
			Unit:        "percent",
			Status:      thresholdStatus(storage.Redis.UtilizationPct, s.config.RedisUtilizationWarnPct, s.config.RedisUtilizationCriticalPct),
		},
	}
}

func (s *Service) computeAlerts(now time.Time, callQuality entity.CallQualityRuntimeStats, realtimeStats, searchStats entity.QueueRuntimeStats, ws entity.WebSocketRuntimeStats, recording entity.RecordingPipelineHealth, storage entity.StorageRuntimeReport, workers []entity.WorkerRuntimeStats, consumers []entity.EventConsumerLag) []entity.OperationalAlert {
	var alerts []entity.OperationalAlert

	if callQuality.DegradedCalls > 0 {
		severity := entity.ObservabilityAlertWarning
		if callQuality.TrackedCalls > 0 && (float64(callQuality.DegradedCalls)/float64(callQuality.TrackedCalls))*100 >= s.config.CallDegradedCriticalRatio {
			severity = entity.ObservabilityAlertCritical
		}
		alerts = append(alerts, entity.OperationalAlert{
			Key:       "degraded_calls",
			Component: "media",
			Severity:  severity,
			Message:   fmt.Sprintf("%d calls are currently degraded", callQuality.DegradedCalls),
			UpdatedAt: now,
		})
	}
	if realtimeStats.Dead >= s.config.DeadLetterWarn || searchStats.Dead >= s.config.DeadLetterWarn {
		severity := entity.ObservabilityAlertWarning
		totalDead := realtimeStats.Dead + searchStats.Dead
		if totalDead >= s.config.DeadLetterCritical {
			severity = entity.ObservabilityAlertCritical
		}
		alerts = append(alerts, entity.OperationalAlert{
			Key:       "dead_letters",
			Component: "eventing",
			Severity:  severity,
			Message:   fmt.Sprintf("dead-letter backlog detected (realtime=%d search=%d)", realtimeStats.Dead, searchStats.Dead),
			UpdatedAt: now,
		})
	}
	if maxInt64(realtimeStats.OldestPendingAgeMs, searchStats.OldestPendingAgeMs) >= s.config.EventLagWarn.Milliseconds() {
		severity := entity.ObservabilityAlertWarning
		if maxInt64(realtimeStats.OldestPendingAgeMs, searchStats.OldestPendingAgeMs) >= s.config.EventLagCritical.Milliseconds() {
			severity = entity.ObservabilityAlertCritical
		}
		alerts = append(alerts, entity.OperationalAlert{
			Key:       "event_lag",
			Component: "eventing",
			Severity:  severity,
			Message:   fmt.Sprintf("durable event lag is elevated (realtime=%dms search=%dms)", realtimeStats.OldestPendingAgeMs, searchStats.OldestPendingAgeMs),
			UpdatedAt: now,
		})
	}
	for _, consumer := range consumers {
		if consumer.Lag < s.config.ConsumerLagWarn {
			continue
		}
		severity := entity.ObservabilityAlertWarning
		if consumer.Lag >= s.config.ConsumerLagCritical {
			severity = entity.ObservabilityAlertCritical
		}
		alerts = append(alerts, entity.OperationalAlert{
			Key:       "consumer_lag:" + consumer.ConsumerName,
			Component: "eventing",
			Severity:  severity,
			Message:   fmt.Sprintf("consumer %s lag is %d events", consumer.ConsumerName, consumer.Lag),
			UpdatedAt: now,
		})
	}
	if storage.Postgres.Saturated || storage.Postgres.AcquiredConnsPct >= s.config.DBUtilizationWarnPct {
		severity := entity.ObservabilityAlertWarning
		if storage.Postgres.Saturated || storage.Postgres.AcquiredConnsPct >= s.config.DBUtilizationCriticalPct {
			severity = entity.ObservabilityAlertCritical
		}
		alerts = append(alerts, entity.OperationalAlert{
			Key:       "db_pressure",
			Component: "storage",
			Severity:  severity,
			Message:   fmt.Sprintf("Postgres pool pressure is high (%.1f%% acquired)", storage.Postgres.AcquiredConnsPct),
			UpdatedAt: now,
		})
	}
	if storage.Redis.Saturated || storage.Redis.UtilizationPct >= s.config.RedisUtilizationWarnPct || storage.Redis.Timeouts > 0 {
		severity := entity.ObservabilityAlertWarning
		if storage.Redis.Saturated || storage.Redis.UtilizationPct >= s.config.RedisUtilizationCriticalPct {
			severity = entity.ObservabilityAlertCritical
		}
		alerts = append(alerts, entity.OperationalAlert{
			Key:       "redis_pressure",
			Component: "storage",
			Severity:  severity,
			Message:   fmt.Sprintf("Redis pool pressure/timeouts detected (%.1f%% utilized, timeouts=%d)", storage.Redis.UtilizationPct, storage.Redis.Timeouts),
			UpdatedAt: now,
		})
	}
	if ws.ReplayFailures > 0 || ws.DroppedMessages > 0 {
		severity := entity.ObservabilityAlertWarning
		if ws.ReplayFailures > 5 || ws.DroppedMessages > 50 {
			severity = entity.ObservabilityAlertCritical
		}
		alerts = append(alerts, entity.OperationalAlert{
			Key:       "ws_recovery",
			Component: "websocket",
			Severity:  severity,
			Message:   fmt.Sprintf("websocket replay/delivery issues detected (replay_failures=%d dropped=%d)", ws.ReplayFailures, ws.DroppedMessages),
			UpdatedAt: now,
		})
	}
	if recording.FailedTotal > 0 || recording.HookFailures > 0 {
		severity := entity.ObservabilityAlertWarning
		if recording.FailedTotal > recording.ProcessedTotal && recording.FailedTotal > 0 {
			severity = entity.ObservabilityAlertCritical
		}
		alerts = append(alerts, entity.OperationalAlert{
			Key:       "recording_pipeline",
			Component: "recording",
			Severity:  severity,
			Message:   fmt.Sprintf("recording pipeline failures detected (failed=%d hook_failures=%d)", recording.FailedTotal, recording.HookFailures),
			UpdatedAt: now,
		})
	}
	for _, worker := range workers {
		if !worker.Stalled {
			continue
		}
		alerts = append(alerts, entity.OperationalAlert{
			Key:       "worker_stall:" + worker.Name,
			Component: "workers",
			Severity:  entity.ObservabilityAlertCritical,
			Message:   fmt.Sprintf("worker %s has not heartbeated within %s", worker.Name, s.config.WorkerStallAfter),
			UpdatedAt: now,
		})
	}
	sort.Slice(alerts, func(i, j int) bool {
		if alerts[i].Severity == alerts[j].Severity {
			return alerts[i].Key < alerts[j].Key
		}
		return alerts[i].Severity > alerts[j].Severity
	})
	return alerts
}

func thresholdStatus(current, warn, critical float64) entity.SLOStatus {
	if current >= critical {
		return entity.SLOStatusBreached
	}
	if current >= warn {
		return entity.SLOStatusWarning
	}
	return entity.SLOStatusHealthy
}

func inverseThresholdStatus(current, target, warnFloor float64) entity.SLOStatus {
	if current < warnFloor {
		return entity.SLOStatusBreached
	}
	if current < target {
		return entity.SLOStatusWarning
	}
	return entity.SLOStatusHealthy
}

func sanitizeMetricName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer(".", "_", "-", "_", " ", "_", ":", "_", "/", "_")
	return replacer.Replace(value)
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func sloStatusValue(status entity.SLOStatus) int {
	switch status {
	case entity.SLOStatusHealthy:
		return 0
	case entity.SLOStatusWarning:
		return 1
	default:
		return 2
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
