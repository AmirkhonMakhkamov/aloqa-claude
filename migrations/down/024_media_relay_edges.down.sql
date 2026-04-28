BEGIN;
DROP INDEX IF EXISTS idx_media_relay_edges_source;
DROP INDEX IF EXISTS idx_media_relay_edges_target;
DROP INDEX IF EXISTS idx_media_relay_edges_workspace;
DROP TABLE IF EXISTS media_relay_edges;
COMMIT;
