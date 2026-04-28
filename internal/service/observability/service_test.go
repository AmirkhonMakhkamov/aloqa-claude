package observability

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
)

type fakeStorageProvider struct {
	report *entity.StorageRuntimeReport
}

func (f fakeStorageProvider) RuntimeReport(context.Context) (*entity.StorageRuntimeReport, error) {
	if f.report == nil {
		return &entity.StorageRuntimeReport{}, nil
	}
	return f.report, nil
}

type fakeQueueProvider struct {
	stats *entity.QueueRuntimeStats
}

func (f fakeQueueProvider) QueueStats(context.Context) (*entity.QueueRuntimeStats, error) {
	if f.stats == nil {
		return &entity.QueueRuntimeStats{}, nil
	}
	return f.stats, nil
}

type fakeConsumerProvider struct {
	items []entity.EventConsumerLag
}

func (f fakeConsumerProvider) ListConsumerLag(context.Context, int) ([]entity.EventConsumerLag, error) {
	return f.items, nil
}

func TestDashboardIncludesAlertsAndSLOs(t *testing.T) {
	svc := NewService(Config{
		EventLagWarn:              5 * time.Second,
		EventLagCritical:          15 * time.Second,
		DeadLetterWarn:            2,
		DeadLetterCritical:        5,
		WorkerStallAfter:          time.Second,
		CallDegradedWarnRatio:     10,
		CallDegradedCriticalRatio: 20,
		ReplaySuccessTargetPct:    99,
		RecordingSuccessTargetPct: 99,
	})
	svc.SetStorageProvider(fakeStorageProvider{report: &entity.StorageRuntimeReport{
		Postgres: entity.PostgresRuntimeStats{AcquiredConnsPct: 92, Saturated: true},
		Redis:    entity.RedisRuntimeStats{UtilizationPct: 85, Saturated: true, Timeouts: 3},
	}})
	svc.SetRealtimeProvider(fakeQueueProvider{stats: &entity.QueueRuntimeStats{
		Name:               "realtime_outbox",
		Dead:               3,
		OldestPendingAgeMs: 20000,
	}}, fakeConsumerProvider{items: []entity.EventConsumerLag{{ConsumerName: "ws-fanout", Lag: 600}}})
	svc.SetSearchProvider(fakeQueueProvider{stats: &entity.QueueRuntimeStats{
		Name: "search_indexer",
		Dead: 1,
	}})

	callID := uuid.New()
	workspaceID := uuid.New()
	svc.RecordCallQualityAggregate(workspaceID, callID, entity.MediaQoSSummary{
		WorkspaceID:      workspaceID,
		CallID:           callID,
		SampleCount:      3,
		AvgPacketLossPct: 11,
		AvgJitterMs:      120,
		MaxPacketLossPct: 25,
	}, entity.MediaQoSSummary{}, true, true)
	svc.RecordRealtimeBatch(10, 2, 1, 250*time.Millisecond, nil)
	svc.RecordSearchBatch(5, 1, 1, 300*time.Millisecond, nil)
	svc.RecordWSRestore(2, 10, 1)
	svc.RecordWSReplayFailure()
	svc.RecordWSDropped()
	svc.RecordRecordingRun("recording_processing", 10, 2, 0, 0, time.Second, nil)
	svc.RecordWorkerHeartbeat("stalled_worker")

	// Force one worker into stalled state.
	svc.mu.Lock()
	if worker := svc.workers["stalled_worker"]; worker != nil {
		worker.LastHeartbeatAt = time.Now().UTC().Add(-2 * time.Second)
	}
	svc.mu.Unlock()

	dashboard, err := svc.Dashboard(context.Background())
	if err != nil {
		t.Fatalf("Dashboard returned error: %v", err)
	}
	if len(dashboard.Alerts) == 0 {
		t.Fatalf("expected alerts to be generated")
	}
	if len(dashboard.SLOs) == 0 {
		t.Fatalf("expected slos to be generated")
	}
	if dashboard.CallQuality.DegradedCalls != 1 {
		t.Fatalf("DegradedCalls = %d, want 1", dashboard.CallQuality.DegradedCalls)
	}
}

func TestMetricsOutputContainsCoreSeries(t *testing.T) {
	svc := NewService(Config{})
	svc.SetStorageProvider(fakeStorageProvider{report: &entity.StorageRuntimeReport{
		Postgres: entity.PostgresRuntimeStats{AcquiredConnsPct: 50},
		Redis:    entity.RedisRuntimeStats{UtilizationPct: 20},
	}})
	svc.SetRealtimeProvider(fakeQueueProvider{stats: &entity.QueueRuntimeStats{Name: "realtime_outbox", Pending: 4}}, fakeConsumerProvider{})
	svc.SetSearchProvider(fakeQueueProvider{stats: &entity.QueueRuntimeStats{Name: "search_indexer", Pending: 2}})
	svc.RecordWSRestore(1, 3, 0)
	svc.RecordRecordingRun("recording_processing", 1, 0, 0, 0, time.Second, nil)

	body, err := svc.Metrics(context.Background())
	if err != nil {
		t.Fatalf("Metrics returned error: %v", err)
	}
	for _, needle := range []string{
		"aloqa_realtime_queue_pending",
		"aloqa_search_queue_pending",
		"aloqa_ws_reconnect_total",
		"aloqa_recording_processed_total",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("metrics body missing %q", needle)
		}
	}
}
