-- Migration: 042_call_lifecycle_reasons
-- Adds explicit lifecycle reasons for LiveKit-backed call cleanup.

BEGIN;

ALTER TABLE calls
    ADD COLUMN end_reason text
        CHECK (end_reason IN ('host_ended', 'all_left', 'failed', 'missed', 'cancelled'));

ALTER TABLE call_participants
    ADD COLUMN left_reason text
        CHECK (left_reason IN ('left', 'timeout', 'declined', 'missed'));

COMMIT;
