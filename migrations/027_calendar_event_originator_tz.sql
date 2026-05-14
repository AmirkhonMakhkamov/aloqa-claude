-- Migration: 027_calendar_event_originator_tz
-- Preserve the event originator's IANA timezone for recurrence expansion.

BEGIN;

ALTER TABLE calendar_events
    ADD COLUMN originator_tz text NOT NULL DEFAULT 'UTC';

COMMIT;
