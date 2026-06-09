-- Migration: 057_perf_telemetry
-- Performance telemetry storage (epic ALK-849, spec §11): lab CI runs and their
-- metric rows, plus sampled, privacy-scrubbed field RUM events. Lab rows are
-- written by CI via POST /api/v1/perf/lab (service token); RUM rows by the FE
-- BFF via POST /api/v1/perf/rum (forwarded, re-validated, privacy-filtered).
-- See docs/superpowers/specs/2026-06-08-perf-backend-infra-handoff.md.

BEGIN;

-- One row per (commit, scenario, profile, environment) lab run.
CREATE TABLE perf_lab_runs (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    commit      text        NOT NULL CHECK (commit <> ''),
    branch      text        NOT NULL CHECK (branch <> ''),
    profile     text        NOT NULL CHECK (profile IN ('desktop', 'throttled')),
    environment text        NOT NULL CHECK (environment IN ('mocked', 'live')),
    scenario    text        NOT NULL CHECK (scenario <> ''),
    ci_run_url  text,
    ts          timestamptz NOT NULL DEFAULT now()
);

-- Idempotent upsert key: re-running the same commit/scenario/profile/env (CI
-- retry) updates the existing run instead of inserting a duplicate.
ALTER TABLE perf_lab_runs
    ADD CONSTRAINT perf_lab_runs_identity_key UNIQUE (commit, scenario, profile, environment);

-- Metric rows for a run. The full gate identity is
-- (scenario, metric_key, route_template, profile, environment, case); scenario/
-- profile/environment live on the parent run, the rest here. The identity tuple
-- is the PRIMARY KEY — it is the natural key (no surrogate id) and is the
-- ON CONFLICT target that dedupes metric rows on CI retry within the same run.
CREATE TABLE perf_lab_metrics (
    run_id         uuid             NOT NULL REFERENCES perf_lab_runs(id) ON DELETE CASCADE,
    metric_key     text             NOT NULL CHECK (metric_key <> ''),
    route_template text             NOT NULL CHECK (route_template <> ''),
    "case"         text             NOT NULL DEFAULT 'default',
    value          double precision NOT NULL,
    unit           text             NOT NULL CHECK (unit <> ''),
    PRIMARY KEY (run_id, metric_key, route_template, "case")
);

-- Sampled field events. Privacy invariant (spec §11): ONLY route template,
-- release, metric key/value, coarse device_class, connection, and a pseudonymous
-- rotating session id — NEVER a workspace/channel/DM/call/user/message id or raw
-- URL. The BFF normalizes + drops dirty events; the BE re-validates and drops
-- again before this insert.
CREATE TABLE perf_rum_events (
    id             bigserial        PRIMARY KEY,
    session        text             NOT NULL CHECK (session <> ''),
    route_template text             NOT NULL CHECK (route_template <> ''),
    metric_key     text             NOT NULL CHECK (metric_key <> ''),
    value          double precision NOT NULL,
    release        text,
    device_class   text,
    connection     text,
    ts             timestamptz      NOT NULL DEFAULT now(),
    -- floor(epoch_seconds / 10): the 10s dedupe bucket (computed server-side).
    ts_bucket      bigint           NOT NULL
);

-- Dedupe: at most one row per (session, route, metric) per 10s bucket so a
-- sampled client retrying a beacon cannot double-count.
ALTER TABLE perf_rum_events
    ADD CONSTRAINT perf_rum_events_dedupe_key UNIQUE (session, route_template, metric_key, ts_bucket);

-- Trend lookup: latest-per-route / p75 queries filter by metric + route + time.
CREATE INDEX idx_perf_rum_events_lookup ON perf_rum_events (metric_key, route_template, ts DESC);

COMMIT;
