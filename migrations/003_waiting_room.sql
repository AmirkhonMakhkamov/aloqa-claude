-- Migration: 003_waiting_room
-- Adds 'waiting' participant status for waiting room support.

BEGIN;

-- Expand the status CHECK constraint to include 'waiting'.
ALTER TABLE call_participants DROP CONSTRAINT IF EXISTS call_participants_status_check;
ALTER TABLE call_participants ADD CONSTRAINT call_participants_status_check
    CHECK (status IN ('invited', 'waiting', 'joining', 'connected', 'disconnected'));

-- Index for waiting participants (host needs to see the waiting list).
CREATE INDEX idx_call_participants_waiting ON call_participants (call_id)
    WHERE status = 'waiting';

COMMIT;
