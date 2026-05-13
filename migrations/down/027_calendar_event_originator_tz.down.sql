-- Down migration: 027_calendar_event_originator_tz

BEGIN;

ALTER TABLE calendar_events
    DROP COLUMN originator_tz;

COMMIT;
