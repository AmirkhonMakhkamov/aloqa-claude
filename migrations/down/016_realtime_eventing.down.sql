BEGIN;
DROP TABLE IF EXISTS event_consumer_cursors;
DROP INDEX IF EXISTS idx_realtime_events_subject_sequence;
DROP INDEX IF EXISTS idx_realtime_events_workspace_sequence;
DROP INDEX IF EXISTS idx_realtime_events_outbox;
DROP TABLE IF EXISTS realtime_events;
COMMIT;
