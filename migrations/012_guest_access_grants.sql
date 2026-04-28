-- 012_guest_access_grants.sql
-- Non-member guest access grants derived from redeemed invite links.

BEGIN;

CREATE TABLE IF NOT EXISTS guest_access_grants (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    invite_id    uuid        NOT NULL REFERENCES guest_invites (id) ON DELETE CASCADE,
    workspace_id uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    channel_ids  uuid[]      NOT NULL DEFAULT '{}',
    expires_at   timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_guest_access_invite_user
    ON guest_access_grants (invite_id, user_id);

CREATE INDEX IF NOT EXISTS idx_guest_access_user_workspace
    ON guest_access_grants (user_id, workspace_id, expires_at DESC);

COMMIT;
