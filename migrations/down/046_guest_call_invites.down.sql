-- Revert ALK-700 call-scoped guest links + 'declined' status.
ALTER TABLE call_participants DROP CONSTRAINT IF EXISTS call_participants_status_check;
ALTER TABLE call_participants ADD CONSTRAINT call_participants_status_check
    CHECK (status IN ('invited', 'waiting', 'joining', 'connected', 'disconnected'));

DROP INDEX IF EXISTS idx_guest_invites_call_id;
ALTER TABLE guest_invites DROP COLUMN IF EXISTS call_id;
