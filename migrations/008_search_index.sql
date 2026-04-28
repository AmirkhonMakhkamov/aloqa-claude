-- 008_search_index.sql
-- PostgreSQL full-text search index for messages and files.

CREATE TABLE IF NOT EXISTS search_index (
    id             UUID PRIMARY KEY,
    workspace_id   UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    resource_type  VARCHAR(20) NOT NULL,  -- 'message', 'file', 'channel'
    resource_id    UUID NOT NULL,
    channel_id     UUID REFERENCES channels(id) ON DELETE SET NULL,
    content        TEXT NOT NULL DEFAULT '',
    tsv            TSVECTOR NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_search_resource ON search_index (resource_type, resource_id);
CREATE INDEX idx_search_tsv ON search_index USING GIN (tsv);
CREATE INDEX idx_search_workspace ON search_index (workspace_id, created_at DESC);
CREATE INDEX idx_search_channel ON search_index (channel_id, created_at DESC) WHERE channel_id IS NOT NULL;
