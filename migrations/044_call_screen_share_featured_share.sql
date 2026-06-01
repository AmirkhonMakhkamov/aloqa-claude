-- Migration: 045_call_screen_share_featured_share
-- ALK-697: per-participant screen-share grant + host-featured share selection.

BEGIN;

ALTER TABLE call_participants
    ADD COLUMN can_screen_share boolean NOT NULL DEFAULT false;

ALTER TABLE calls
    ADD COLUMN featured_share_user_id uuid REFERENCES users (id) ON DELETE SET NULL;

CREATE INDEX idx_calls_featured_share_user ON calls (featured_share_user_id)
    WHERE featured_share_user_id IS NOT NULL;

COMMIT;
