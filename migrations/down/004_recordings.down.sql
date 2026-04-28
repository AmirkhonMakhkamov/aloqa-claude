BEGIN;
DROP INDEX IF EXISTS idx_recordings_legal_hold;
DROP INDEX IF EXISTS idx_recordings_status;
DROP INDEX IF EXISTS idx_recordings_workspace_id;
DROP INDEX IF EXISTS idx_recordings_call_id;
DROP TABLE IF EXISTS recordings;
COMMIT;
