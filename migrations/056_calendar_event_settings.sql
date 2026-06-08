-- 056: optional per-event call-settings preconfig (ALK-819 / S11).
--
-- A calendar event whose location is `aloqa_meet` may carry a subset of the
-- call settings (entry_mode, mute_on_join, breakout_rooms, breakout_creation,
-- max_breakout_rooms). When a call is started from the event the service
-- overlays these onto the canonical meeting defaults and normalises the result,
-- instead of always using hardcoded defaults.
--
-- The column is a NULLable JSONB so an omitted preconfig is SQL NULL (and reads
-- back as a nil pointer on the entity). Existing rows have no settings and keep
-- their previous behaviour (canonical defaults at start-call). Only the subset
-- pointer fields that were explicitly set are stored; absent keys remain
-- omitted so the start-call overlay leaves the corresponding default untouched.
ALTER TABLE calendar_events
    ADD COLUMN IF NOT EXISTS settings jsonb NULL;
