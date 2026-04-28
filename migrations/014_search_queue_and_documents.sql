ALTER TABLE search_index
    ADD COLUMN IF NOT EXISTS title TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

DROP INDEX IF EXISTS idx_search_resource;
CREATE UNIQUE INDEX IF NOT EXISTS idx_search_resource_workspace
    ON search_index (workspace_id, resource_type, resource_id);
CREATE INDEX IF NOT EXISTS idx_search_type_created
    ON search_index (workspace_id, resource_type, created_at DESC);

CREATE TABLE IF NOT EXISTS search_index_jobs (
    id            UUID PRIMARY KEY,
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    resource_type VARCHAR(20) NOT NULL CHECK (resource_type IN ('message', 'file', 'channel', 'user')),
    resource_id   UUID NOT NULL,
    operation     VARCHAR(10) NOT NULL CHECK (operation IN ('upsert', 'delete')),
    channel_id    UUID REFERENCES channels(id) ON DELETE SET NULL,
    title         TEXT NOT NULL DEFAULT '',
    content       TEXT NOT NULL DEFAULT '',
    metadata      JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    available_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    locked_at     TIMESTAMPTZ,
    processed_at  TIMESTAMPTZ,
    attempts      INT NOT NULL DEFAULT 0,
    max_attempts  INT NOT NULL DEFAULT 8,
    status        VARCHAR(12) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'failed', 'processed', 'dead')),
    last_error    TEXT,
    CONSTRAINT search_index_jobs_resource_unique UNIQUE (workspace_id, resource_type, resource_id)
);

CREATE INDEX IF NOT EXISTS idx_search_index_jobs_available
    ON search_index_jobs (status, available_at ASC);
