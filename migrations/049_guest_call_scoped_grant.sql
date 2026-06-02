-- 049: scope a guest access grant to a single call (any call type), so a guest
-- invited to a channel-less call gets access to that call ONLY — never to
-- channels or workspace content. Complements 046 (guest_invites.call_id).
ALTER TABLE guest_access_grants
    ADD COLUMN IF NOT EXISTS call_id uuid NULL REFERENCES calls (id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_guest_access_grants_call_id ON guest_access_grants (call_id);
