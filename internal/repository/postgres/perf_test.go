package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	perfsvc "aloqa/internal/service/perf"
)

func perfTestPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	dsn := os.Getenv("ALOQA_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set ALOQA_POSTGRES_TEST_DSN to a disposable migrated Postgres database to run this integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

func TestPerfRepoUpsertLabRunIsIdempotent(t *testing.T) {
	pool, ctx := perfTestPool(t)
	repo := NewPerfRepo(pool)

	commit := "test-" + uuid.New().String()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM perf_lab_runs WHERE commit = $1`, commit)
	})

	run := perfsvc.LabRun{
		Commit: commit, Branch: "develop", Profile: "desktop", Environment: "mocked",
		Scenario: "common.load", TS: time.Now().UTC(),
		Metrics: []perfsvc.LabMetric{
			{Key: "web-vital.lcp", RouteTemplate: "/login", Case: "default", Value: 900, Unit: "ms"},
		},
	}

	firstID, err := repo.UpsertLabRun(ctx, run)
	if err != nil {
		t.Fatalf("first upsert run: %v", err)
	}
	if err := repo.UpsertLabMetrics(ctx, firstID, run.Metrics); err != nil {
		t.Fatalf("upsert metrics: %v", err)
	}

	// Same identity, changed branch + metric value → same run id, updated rows.
	run.Branch = "feature/x"
	run.Metrics[0].Value = 750
	secondID, err := repo.UpsertLabRun(ctx, run)
	if err != nil {
		t.Fatalf("second upsert run: %v", err)
	}
	if firstID != secondID {
		t.Fatalf("expected same run id on conflict, got %s then %s", firstID, secondID)
	}
	if err := repo.UpsertLabMetrics(ctx, secondID, run.Metrics); err != nil {
		t.Fatalf("re-upsert metrics: %v", err)
	}

	var runCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM perf_lab_runs WHERE commit = $1`, commit).Scan(&runCount); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("expected 1 run row, got %d", runCount)
	}

	var branch string
	var value float64
	var metricCount int
	if err := pool.QueryRow(ctx, `SELECT branch FROM perf_lab_runs WHERE id = $1`, firstID).Scan(&branch); err != nil {
		t.Fatalf("read branch: %v", err)
	}
	if branch != "feature/x" {
		t.Fatalf("expected branch updated to feature/x, got %s", branch)
	}
	if err := pool.QueryRow(ctx, `SELECT value FROM perf_lab_metrics WHERE run_id = $1`, firstID).Scan(&value); err != nil {
		t.Fatalf("read metric value: %v", err)
	}
	if value != 750 {
		t.Fatalf("expected metric value updated to 750, got %v", value)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM perf_lab_metrics WHERE run_id = $1`, firstID).Scan(&metricCount); err != nil {
		t.Fatalf("count metrics: %v", err)
	}
	if metricCount != 1 {
		t.Fatalf("expected 1 metric row after re-upsert, got %d", metricCount)
	}
}

func TestPerfRepoInsertRumEventsDedupesByBucket(t *testing.T) {
	pool, ctx := perfTestPool(t)
	repo := NewPerfRepo(pool)

	session := "test-" + uuid.New().String()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM perf_rum_events WHERE session = $1`, session)
	})

	now := time.Now().UTC()
	bucket := now.Unix() / 10
	row := perfsvc.RumEventRecord{
		Session: session, RouteTemplate: "/w/:ws/c/:channel", MetricKey: "web-vital.lcp",
		Value: 1200, Release: "v0.25.0", DeviceClass: "desktop", Connection: "4g",
		TS: now, TSBucket: bucket,
	}

	if err := repo.InsertRumEvents(ctx, []perfsvc.RumEventRecord{row}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// Same dedupe key (session, route, metric, bucket) → ignored.
	dupe := row
	dupe.Value = 9999
	if err := repo.InsertRumEvents(ctx, []perfsvc.RumEventRecord{dupe}); err != nil {
		t.Fatalf("dupe insert: %v", err)
	}
	// Different bucket → new row.
	next := row
	next.TSBucket = bucket + 1
	if err := repo.InsertRumEvents(ctx, []perfsvc.RumEventRecord{next}); err != nil {
		t.Fatalf("next-bucket insert: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM perf_rum_events WHERE session = $1`, session).Scan(&count); err != nil {
		t.Fatalf("count rum events: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 rows (one deduped, one new bucket), got %d", count)
	}

	var value float64
	if err := pool.QueryRow(ctx,
		`SELECT value FROM perf_rum_events WHERE session = $1 AND ts_bucket = $2`, session, bucket,
	).Scan(&value); err != nil {
		t.Fatalf("read original value: %v", err)
	}
	if value != 1200 {
		t.Fatalf("expected original value 1200 preserved (dupe ignored), got %v", value)
	}
}
