BEGIN;
DROP INDEX IF EXISTS idx_search_channel;
DROP INDEX IF EXISTS idx_search_workspace;
DROP INDEX IF EXISTS idx_search_tsv;
DROP INDEX IF EXISTS idx_search_resource;
DROP TABLE IF EXISTS search_index;
COMMIT;
