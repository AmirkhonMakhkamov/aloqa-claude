BEGIN;
DROP INDEX IF EXISTS idx_call_participants_waiting;
ALTER TABLE call_participants DROP CONSTRAINT IF EXISTS call_participants_status_check;
ALTER TABLE call_participants ADD CONSTRAINT call_participants_status_check
    CHECK (status IN ('invited', 'joining', 'connected', 'disconnected'));
COMMIT;
