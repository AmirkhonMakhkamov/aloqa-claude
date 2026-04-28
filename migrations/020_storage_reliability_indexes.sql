BEGIN;

CREATE INDEX IF NOT EXISTS idx_recordings_workspace_created_desc
    ON recordings (workspace_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_recordings_processable_order
    ON recordings ((COALESCE(next_retry_at, stopped_at, created_at)), created_at)
    WHERE status IN ('processing', 'failed');

CREATE INDEX IF NOT EXISTS idx_media_qos_samples_workspace_call_time
    ON media_qos_samples (workspace_id, call_id, sampled_at DESC);

CREATE INDEX IF NOT EXISTS idx_realtime_events_cleanup_status_time
    ON realtime_events (status, published_at DESC)
    WHERE status IN ('published', 'dead');

CREATE INDEX IF NOT EXISTS idx_search_index_jobs_cleanup
    ON search_index_jobs (status, processed_at DESC)
    WHERE status IN ('processed', 'dead');

COMMIT;
