-- 057: invited-members list for private calls (ALK-814 / S6).
--
-- A private channel-less call is host-curated: only the creator, host/co-host,
-- currently-connected participants, guests holding a grant for this call, and
-- the workspace members listed here may join. Populated at call creation and
-- snapshotted from connected non-guest participants on a public->private flip.
-- Only workspace members are ever inserted (guests are modelled by their
-- call-scoped grant, never this table).
CREATE TABLE IF NOT EXISTS call_invited_members (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    call_id     uuid NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invited_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    invited_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (call_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_call_invited_members_call_id
    ON call_invited_members (call_id);
