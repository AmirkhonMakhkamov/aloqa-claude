CREATE TABLE IF NOT EXISTS media_quality_policies (
    call_id UUID PRIMARY KEY REFERENCES calls(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    mode TEXT NOT NULL DEFAULT 'auto',
    alert_packet_loss_pct DOUBLE PRECISION NOT NULL DEFAULT 5,
    alert_jitter_ms DOUBLE PRECISION NOT NULL DEFAULT 70,
    alert_round_trip_time_ms DOUBLE PRECISION NOT NULL DEFAULT 400,
    correlation_tolerance_pct DOUBLE PRECISION NOT NULL DEFAULT 8,
    correlation_tolerance_ms DOUBLE PRECISION NOT NULL DEFAULT 80,
    meeting_wide_downgrade BOOLEAN NOT NULL DEFAULT FALSE,
    alerting_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    updated_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_media_quality_policies_workspace
    ON media_quality_policies (workspace_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS media_quality_alerts (
    id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    call_id UUID NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    severity TEXT NOT NULL,
    status TEXT NOT NULL,
    message TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_media_quality_alerts_active_unique
    ON media_quality_alerts (workspace_id, call_id, kind)
    WHERE status = 'active';

CREATE INDEX IF NOT EXISTS idx_media_quality_alerts_call
    ON media_quality_alerts (call_id, updated_at DESC);
