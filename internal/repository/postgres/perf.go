package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/pkg/id"
	perfsvc "aloqa/internal/service/perf"
)

// PerfRepo persists performance telemetry (epic ALK-849): lab runs + metrics and
// sampled field RUM events.
type PerfRepo struct {
	pool *pgxpool.Pool
}

// NewPerfRepo builds a PerfRepo over pool.
func NewPerfRepo(pool *pgxpool.Pool) *PerfRepo {
	return &PerfRepo{pool: pool}
}

// Compile-time guarantee that PerfRepo satisfies the service port.
var _ perfsvc.Repository = (*PerfRepo)(nil)

// UpsertLabRun inserts or updates a run by its identity tuple
// (commit, scenario, profile, environment) and returns the run id — the existing
// id on conflict so a CI retry rewrites the same run.
func (r *PerfRepo) UpsertLabRun(ctx context.Context, run perfsvc.LabRun) (uuid.UUID, error) {
	const query = `
		INSERT INTO perf_lab_runs (id, commit, branch, profile, environment, scenario, ci_run_url, ts)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (commit, scenario, profile, environment) DO UPDATE
		SET branch = EXCLUDED.branch,
			ci_run_url = EXCLUDED.ci_run_url,
			ts = EXCLUDED.ts
		RETURNING id`

	ts := run.TS
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	var runID uuid.UUID
	if err := r.pool.QueryRow(ctx, query,
		id.New(),
		run.Commit,
		run.Branch,
		run.Profile,
		run.Environment,
		run.Scenario,
		nullableString(run.CIRunURL),
		ts.UTC(),
	).Scan(&runID); err != nil {
		return uuid.Nil, fmt.Errorf("postgres: upsert perf lab run: %w", err)
	}
	return runID, nil
}

// UpsertLabMetrics inserts or updates each metric row for a run, deduping on the
// (run_id, metric_key, route_template, case) identity.
func (r *PerfRepo) UpsertLabMetrics(ctx context.Context, runID uuid.UUID, metrics []perfsvc.LabMetric) error {
	if len(metrics) == 0 {
		return nil
	}
	const query = `
		INSERT INTO perf_lab_metrics (run_id, metric_key, route_template, "case", value, unit)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (run_id, metric_key, route_template, "case") DO UPDATE
		SET value = EXCLUDED.value,
			unit = EXCLUDED.unit`

	batch := &pgx.Batch{}
	for _, m := range metrics {
		metricCase := m.Case
		if metricCase == "" {
			metricCase = "default"
		}
		batch.Queue(query, runID, m.Key, m.RouteTemplate, metricCase, m.Value, m.Unit)
	}

	br := r.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for range metrics {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("postgres: upsert perf lab metric: %w", err)
		}
	}
	return nil
}

// InsertRumEvents inserts sanitized field events, ignoring rows that collide on
// the (session, route_template, metric_key, ts_bucket) 10s dedupe key.
func (r *PerfRepo) InsertRumEvents(ctx context.Context, rows []perfsvc.RumEventRecord) error {
	if len(rows) == 0 {
		return nil
	}
	const query = `
		INSERT INTO perf_rum_events
			(session, route_template, metric_key, value, release, device_class, connection, ts, ts_bucket)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (session, route_template, metric_key, ts_bucket) DO NOTHING`

	batch := &pgx.Batch{}
	for _, row := range rows {
		batch.Queue(query,
			row.Session,
			row.RouteTemplate,
			row.MetricKey,
			row.Value,
			nullableString(row.Release),
			nullableString(row.DeviceClass),
			nullableString(row.Connection),
			row.TS.UTC(),
			row.TSBucket,
		)
	}

	br := r.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for range rows {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("postgres: insert perf rum event: %w", err)
		}
	}
	return nil
}
