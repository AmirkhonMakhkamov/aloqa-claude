BEGIN;
DROP INDEX IF EXISTS idx_search_index_jobs_available;
DROP TABLE IF EXISTS search_index_jobs;
DROP INDEX IF EXISTS idx_search_type_created;
DROP INDEX IF EXISTS idx_search_resource_workspace;
ALTER TABLE search_index DROP COLUMN IF EXISTS updated_at;
ALTER TABLE search_index DROP COLUMN IF EXISTS metadata;
ALTER TABLE search_index DROP COLUMN IF EXISTS title;
COMMIT;
