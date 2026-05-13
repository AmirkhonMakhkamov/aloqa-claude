package entity

import (
	"time"

	"github.com/google/uuid"
)

type CalendarColor string

const (
	CalendarColorBrand   CalendarColor = "brand"
	CalendarColorViolet  CalendarColor = "violet"
	CalendarColorTeal    CalendarColor = "teal"
	CalendarColorAmber   CalendarColor = "amber"
	CalendarColorWarning CalendarColor = "warning"
	CalendarColorMuted   CalendarColor = "muted"
)

type UserCalendar struct {
	ID          uuid.UUID     `json:"id"`
	OwnerID     uuid.UUID     `json:"owner_id"`
	WorkspaceID uuid.UUID     `json:"workspace_id"`
	Name        string        `json:"name"`
	Color       CalendarColor `json:"color"`
	IsDefault   bool          `json:"is_default"`
	IsSystem    bool          `json:"is_system"`
	IsVisible   bool          `json:"is_visible"`
	CreatedAt   time.Time     `json:"created_at"`
}

type EventLocationType string

const (
	EventLocationAloqaMeet    EventLocationType = "aloqa_meet"
	EventLocationExternalLink EventLocationType = "external_link"
	EventLocationInPerson     EventLocationType = "in_person"
	EventLocationRoom         EventLocationType = "room"
)

type EventLocation struct {
	Type  EventLocationType `json:"type"`
	Value *string           `json:"value"`
}

type RecurrenceRule struct {
	RRule   string      `json:"rrule"`
	Exdates []time.Time `json:"exdates"`
}

type RsvpStatus string

const (
	RsvpStatusNoResponse RsvpStatus = "no_response"
	RsvpStatusGoing      RsvpStatus = "going"
	RsvpStatusMaybe      RsvpStatus = "maybe"
	RsvpStatusDeclined   RsvpStatus = "declined"
)

type EventAttendee struct {
	ID          uuid.UUID  `json:"id"`
	EventID     uuid.UUID  `json:"event_id"`
	UserID      *uuid.UUID `json:"user_id"`
	Email       *string    `json:"email"`
	IsRequired  bool       `json:"is_required"`
	RsvpStatus  RsvpStatus `json:"rsvp_status"`
	RespondedAt *time.Time `json:"responded_at"`
}

type ReminderChannel string

const (
	ReminderChannelInApp ReminderChannel = "in_app"
	ReminderChannelOS    ReminderChannel = "os"
)

type EventReminder struct {
	ID                    uuid.UUID       `json:"-"`
	EventID               uuid.UUID       `json:"-"`
	UserID                uuid.UUID       `json:"-"`
	OffsetMinutes         int             `json:"offset_minutes"`
	Channel               ReminderChannel `json:"channel"`
	RemindersDispatchedAt *time.Time      `json:"-"`
}

type CalendarEvent struct {
	ID              uuid.UUID       `json:"id"`
	CalendarID      uuid.UUID       `json:"calendar_id"`
	WorkspaceID     uuid.UUID       `json:"workspace_id"`
	ChannelID       *uuid.UUID      `json:"channel_id,omitempty"`
	OrganizerID     uuid.UUID       `json:"organizer_id"`
	Title           string          `json:"title"`
	Description     *string         `json:"description"`
	Location        EventLocation   `json:"location"`
	ScheduledAt     time.Time       `json:"scheduled_at"`
	DurationMinutes int             `json:"duration_minutes"`
	AllDay          bool            `json:"all_day"`
	Recurrence      *RecurrenceRule `json:"recurrence"`
	CallID          *uuid.UUID      `json:"call_id"`
	Attendees       []EventAttendee `json:"attendees"`
	Reminders       []EventReminder `json:"reminders"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type EventOccurrence struct {
	CalendarEvent
	InstanceAt          time.Time `json:"instance_at"`
	IsRecurringInstance bool      `json:"is_recurring_instance"`
}

type EventOverride struct {
	ID                      uuid.UUID  `json:"id"`
	EventID                 uuid.UUID  `json:"event_id"`
	OriginalStart           time.Time  `json:"original_start"`
	OverrideStart           *time.Time `json:"override_start"`
	OverrideDurationMinutes *int       `json:"override_duration_minutes"`
	Cancelled               bool       `json:"cancelled"`
}

type ReminderTarget struct {
	Occurrence    EventOccurrence `json:"event"`
	UserID        uuid.UUID       `json:"user_id"`
	OffsetMinutes int             `json:"offset_minutes"`
}
