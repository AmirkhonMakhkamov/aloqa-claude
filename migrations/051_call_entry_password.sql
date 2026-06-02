-- 051: host-configurable call entry mode + optional join password.
--
-- entry_mode ('manual_admit' | 'password' | 'open') lives in the calls.settings
-- JSONB and is set at creation. Existing rows have no entry_mode key; the
-- service derives it from the legacy waiting_room flag on read, so their
-- behaviour is unchanged.
--
-- The join password's bcrypt hash is stored in a dedicated NULLable column so
-- it is never marshalled through the settings JSON (and thus never returned by
-- the API). Only the bcrypt verification path on JoinCall reads it.
ALTER TABLE calls
    ADD COLUMN IF NOT EXISTS join_password_hash text NULL;
