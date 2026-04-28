CREATE TABLE IF NOT EXISTS realtime_events (
    id                 UUID PRIMARY KEY,
    sequence           BIGSERIAL UNIQUE,
    version            INT NOT NULL DEFAULT 1,
    type               TEXT NOT NULL,
    subject            TEXT NOT NULL,
    workspace_id       UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    channel_id         UUID REFERENCES channels(id) ON DELETE SET NULL,
    user_id            UUID NOT NULL,
    delivery_semantic  TEXT NOT NULL CHECK (delivery_semantic IN ('ephemeral', 'best_effort', 'at_least_once')),
    replayable         BOOLEAN NOT NULL DEFAULT false,
    body               JSONB NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    published_at       TIMESTAMPTZ,
    available_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    locked_at          TIMESTAMPTZ,
    attempts           INT NOT NULL DEFAULT 0,
    max_attempts       INT NOT NULL DEFAULT 8,
    status             TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'published', 'failed', 'dead')),
    last_error         TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_realtime_events_outbox
    ON realtime_events (status, available_at ASC, sequence ASC);

CREATE INDEX IF NOT EXISTS idx_realtime_events_workspace_sequence
    ON realtime_events (workspace_id, sequence ASC)
    WHERE replayable = true AND status = 'published';

CREATE INDEX IF NOT EXISTS idx_realtime_events_subject_sequence
    ON realtime_events (subject, sequence ASC)
    WHERE replayable = true AND status = 'published';

CREATE TABLE IF NOT EXISTS event_consumer_cursors (
    consumer_name        TEXT PRIMARY KEY,
    stream_name          TEXT NOT NULL DEFAULT '',
    last_event_id        UUID,
    last_event_sequence  BIGINT NOT NULL DEFAULT 0,
    deliveries           BIGINT NOT NULL DEFAULT 0,
    failures             BIGINT NOT NULL DEFAULT 0,
    lag                  BIGINT NOT NULL DEFAULT 0,
    status               TEXT NOT NULL DEFAULT 'idle' CHECK (status IN ('idle', 'active', 'degraded', 'failed')),
    last_error           TEXT NOT NULL DEFAULT '',
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
