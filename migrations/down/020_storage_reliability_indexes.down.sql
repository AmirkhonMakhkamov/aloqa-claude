BEGIN;
DROP INDEX IF EXISTS idx_search_index_jobs_cleanup;
DROP INDEX IF EXISTS idx_realtime_events_cleanup_status_time;
DROP INDEX IF EXISTS idx_media_qos_samples_workspace_call_time;
DROP INDEX IF EXISTS idx_recordings_processable_order;
DROP INDEX IF EXISTS idx_recordings_workspace_created_desc;
COMMIT;
