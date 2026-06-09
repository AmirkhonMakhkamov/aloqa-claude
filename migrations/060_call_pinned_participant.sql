-- Migration: 060_call_pinned_participant
-- ALK-813: host-pinned participant selection for everyone.

BEGIN;

ALTER TABLE calls
    ADD COLUMN IF NOT EXISTS pinned_participant_user_id uuid REFERENCES users (id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_calls_pinned_participant_user ON calls (pinned_participant_user_id)
    WHERE pinned_participant_user_id IS NOT NULL;

COMMIT;
