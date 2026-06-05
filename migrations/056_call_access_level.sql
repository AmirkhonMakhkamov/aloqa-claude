-- 056: meeting access level for channel-less calls (ALK-814 / S6).
--
-- access_level ('public' | 'private') lives in a dedicated calls column (not the
-- settings JSONB) so it can be filtered query-side and is explicit. Existing
-- rows default to 'public' so every current call stays openly joinable; the
-- value is inert for channel-attached and one_to_one calls (access is derived
-- from channel membership / DM membership there).
ALTER TABLE calls
    ADD COLUMN IF NOT EXISTS access_level text NOT NULL DEFAULT 'public'
        CHECK (access_level IN ('public', 'private'));
