-- Migration: 026_calendars
-- Calendar containers, scheduled events, attendees, reminders, and overrides.

BEGIN;

CREATE TABLE user_calendars (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id     uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    workspace_id uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name         text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    color        text        NOT NULL DEFAULT 'brand'
                             CHECK (color IN ('brand', 'violet', 'teal', 'amber', 'warning', 'muted')),
    is_default   boolean     NOT NULL DEFAULT false,
    is_system    boolean     NOT NULL DEFAULT false,
    is_visible   boolean     NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_user_calendars_one_default
    ON user_calendars (owner_id, workspace_id)
    WHERE is_default = true;

CREATE INDEX idx_user_calendars_owner_workspace
    ON user_calendars (owner_id, workspace_id);

CREATE INDEX idx_user_calendars_workspace_visible
    ON user_calendars (workspace_id, is_visible);

CREATE TABLE calendar_events (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    calendar_id         uuid        NOT NULL REFERENCES user_calendars (id) ON DELETE CASCADE,
    workspace_id         uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    channel_id           uuid        REFERENCES channels (id) ON DELETE SET NULL,
    organizer_id         uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title                text        NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
    description          text,
    location_type        text        NOT NULL
                                      CHECK (location_type IN ('aloqa_meet', 'external_link', 'in_person', 'room')),
    location_value       text,
    scheduled_at         timestamptz NOT NULL,
    duration_minutes     integer     NOT NULL CHECK (duration_minutes BETWEEN 0 AND 1440),
    all_day              boolean     NOT NULL DEFAULT false,
    recurrence_rrule     text,
    recurrence_exdates   timestamptz[],
    call_id              uuid        REFERENCES calls (id) ON DELETE SET NULL,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_calendar_events_workspace_scheduled
    ON calendar_events (workspace_id, scheduled_at);

CREATE INDEX idx_calendar_events_calendar_scheduled
    ON calendar_events (calendar_id, scheduled_at);

CREATE INDEX idx_calendar_events_channel_id
    ON calendar_events (channel_id)
    WHERE channel_id IS NOT NULL;

CREATE INDEX idx_calendar_events_call_id
    ON calendar_events (call_id)
    WHERE call_id IS NOT NULL;

CREATE TRIGGER trg_calendar_events_updated_at
    BEFORE UPDATE ON calendar_events
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE event_attendees (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id      uuid        NOT NULL REFERENCES calendar_events (id) ON DELETE CASCADE,
    user_id       uuid        REFERENCES users (id) ON DELETE CASCADE,
    email         text,
    is_required   boolean     NOT NULL DEFAULT true,
    rsvp_status   text        NOT NULL DEFAULT 'no_response'
                              CHECK (rsvp_status IN ('no_response', 'going', 'maybe', 'declined')),
    responded_at  timestamptz,
    CONSTRAINT event_attendees_one_identifier
        CHECK ((user_id IS NOT NULL) <> (email IS NOT NULL))
);

CREATE UNIQUE INDEX idx_event_attendees_event_user_unique
    ON event_attendees (event_id, user_id)
    WHERE user_id IS NOT NULL;

CREATE UNIQUE INDEX idx_event_attendees_event_email_unique
    ON event_attendees (event_id, lower(email))
    WHERE email IS NOT NULL;

CREATE INDEX idx_event_attendees_event_id
    ON event_attendees (event_id);

CREATE INDEX idx_event_attendees_user_event
    ON event_attendees (user_id, event_id)
    WHERE user_id IS NOT NULL;

CREATE TABLE event_reminders (
    id                      uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id                uuid        NOT NULL REFERENCES calendar_events (id) ON DELETE CASCADE,
    user_id                 uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    offset_minutes          integer     NOT NULL CHECK (offset_minutes BETWEEN 0 AND 10080),
    channel                 text        NOT NULL CHECK (channel IN ('in_app', 'os')),
    reminders_dispatched_at timestamptz
);

CREATE UNIQUE INDEX idx_event_reminders_unique
    ON event_reminders (event_id, user_id, offset_minutes, channel);

CREATE INDEX idx_event_reminders_event_id
    ON event_reminders (event_id);

CREATE INDEX idx_event_reminders_user_event
    ON event_reminders (user_id, event_id);

CREATE INDEX idx_event_reminders_pending
    ON event_reminders (event_id, user_id, offset_minutes)
    WHERE reminders_dispatched_at IS NULL;

CREATE TABLE event_overrides (
    id                        uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id                  uuid        NOT NULL REFERENCES calendar_events (id) ON DELETE CASCADE,
    original_start            timestamptz NOT NULL,
    override_start            timestamptz,
    override_duration_minutes integer,
    cancelled                 boolean     NOT NULL DEFAULT false
);

CREATE UNIQUE INDEX idx_event_overrides_event_original
    ON event_overrides (event_id, original_start);

CREATE INDEX idx_event_overrides_event_id
    ON event_overrides (event_id);

CREATE OR REPLACE FUNCTION ensure_default_user_calendar()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO user_calendars (owner_id, workspace_id, name, color, is_default, is_system, is_visible)
    VALUES (NEW.user_id, NEW.workspace_id, 'Рабочий', 'brand', true, false, true)
    ON CONFLICT DO NOTHING;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

INSERT INTO user_calendars (owner_id, workspace_id, name, color, is_default, is_system, is_visible)
SELECT wm.user_id, wm.workspace_id, 'Рабочий', 'brand', true, false, true
FROM workspace_members wm
ON CONFLICT DO NOTHING;

CREATE TRIGGER trg_workspace_member_default_calendar
    AFTER INSERT ON workspace_members
    FOR EACH ROW EXECUTE FUNCTION ensure_default_user_calendar();

COMMIT;
