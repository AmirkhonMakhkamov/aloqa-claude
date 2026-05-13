BEGIN;

DROP TRIGGER IF EXISTS trg_workspace_member_default_calendar ON workspace_members;
DROP FUNCTION IF EXISTS ensure_default_user_calendar();

DROP TABLE IF EXISTS event_overrides;
DROP TABLE IF EXISTS event_reminders;
DROP TABLE IF EXISTS event_attendees;
DROP TABLE IF EXISTS calendar_events;
DROP TABLE IF EXISTS user_calendars;

COMMIT;
