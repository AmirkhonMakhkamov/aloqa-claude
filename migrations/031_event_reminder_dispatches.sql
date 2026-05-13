-- Migration: 031_event_reminder_dispatches
-- Per-occurrence dispatch state for calendar reminders.

BEGIN;

CREATE TABLE event_reminder_dispatches (
    reminder_id   uuid        NOT NULL REFERENCES event_reminders (id) ON DELETE CASCADE,
    occurrence_at timestamptz NOT NULL,
    dispatched_at timestamptz,
    PRIMARY KEY (reminder_id, occurrence_at)
);

CREATE INDEX ix_reminder_dispatches_pending
    ON event_reminder_dispatches (occurrence_at)
    WHERE dispatched_at IS NULL;

COMMIT;
