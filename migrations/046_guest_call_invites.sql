-- ALK-700: call-scoped guest one-time links + terminal "declined" waiting-room state.

-- Tie a guest invite to a specific call so the redeem flow can drop the guest
-- into that call (and reject the link once the call has ended).
ALTER TABLE guest_invites
    ADD COLUMN IF NOT EXISTS call_id uuid NULL REFERENCES calls (id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_guest_invites_call_id ON guest_invites (call_id);

-- Allow the terminal 'declined' participant status (guest reject is terminal so
-- the FE deep-link poll stops instead of re-knocking).
ALTER TABLE call_participants DROP CONSTRAINT IF EXISTS call_participants_status_check;
ALTER TABLE call_participants ADD CONSTRAINT call_participants_status_check
    CHECK (status IN ('invited', 'waiting', 'joining', 'connected', 'disconnected', 'declined'));
