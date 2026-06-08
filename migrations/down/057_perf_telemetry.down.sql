-- Revert 057: perf telemetry tables.
BEGIN;
DROP TABLE IF EXISTS perf_rum_events;
DROP TABLE IF EXISTS perf_lab_metrics;
DROP TABLE IF EXISTS perf_lab_runs;
COMMIT;
