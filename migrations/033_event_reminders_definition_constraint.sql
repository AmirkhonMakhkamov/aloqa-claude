-- Migration: 033_event_reminders_definition_constraint
-- Promote reminder definition uniqueness to a table constraint.

BEGIN;

ALTER TABLE event_reminders
    ADD CONSTRAINT uq_event_reminders_definition
    UNIQUE USING INDEX idx_event_reminders_unique;

COMMIT;
