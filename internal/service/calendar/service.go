package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	calrrule "aloqa/internal/pkg/rrule"
	"aloqa/internal/platform/txscope"
)

type EventPublisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

type CallService interface {
	StartCall(ctx context.Context, workspaceID, userID uuid.UUID, callType entity.CallType, title string, channelID *uuid.UUID, settings entity.CallSettings, joinPassword string) (*entity.Call, error)
	// FinalizeNewCallSettings resolves a caller-supplied settings tuple to the
	// server-authoritative values a new call must carry (breakout-force, recording
	// capability, chat-on, member-permission defaults, entry-mode/breakout
	// resolution). The tx start-call path applies it directly so it persists the
	// SAME settings as StartCall does on the non-tx path (ALK-819 review).
	FinalizeNewCallSettings(callType entity.CallType, settings entity.CallSettings) entity.CallSettings
	// ValidateNewCallSettings runs the call service's per-field settings validation
	// (the same StartCall runs on the non-tx path). The tx start-call path builds
	// the call inline and must run it explicitly so a corrupted/legacy event
	// settings row can't persist a call the non-tx path would reject (ALK-819 review).
	ValidateNewCallSettings(callType entity.CallType, settings entity.CallSettings) error
	GetCall(ctx context.Context, workspaceID, callID, userID uuid.UUID) (*entity.Call, error)
	EnsureLiveKitRoomRequired(ctx context.Context, call *entity.Call) error
	DeleteLiveKitRoom(ctx context.Context, callID uuid.UUID) error
}

type Service struct {
	calendars repository.CalendarRepository
	members   repository.WorkspaceRepository
	calls     CallService
	pubsub    EventPublisher
	tx        txscope.Manager
}

const (
	reminderDispatchLimit         = 100
	reminderDiscoveryHorizon      = 24 * time.Hour
	maxReminderOffsetMinutes      = 10080
	reminderEventLookaheadHorizon = time.Duration(maxReminderOffsetMinutes)*time.Minute + reminderDiscoveryHorizon
	reminderOutboxBatchSize       = 100
	reminderOutboxMaxAttempts     = 10
)

type CreateEventInput struct {
	CalendarID      uuid.UUID
	ChannelID       *uuid.UUID
	Title           string
	Description     *string
	Location        entity.EventLocation
	ScheduledAt     time.Time
	OriginatorTZ    string
	DurationMinutes int
	AllDay          bool
	Recurrence      *entity.RecurrenceRule
	AttendeeUserIDs []uuid.UUID
	AttendeeEmails  []string
	RequiredUserIDs []uuid.UUID
	Reminders       []entity.EventReminder
	// Settings is the optional per-event call-settings preconfig (ALK-819 / S11);
	// the HTTP layer already drops it (nil) for non-aloqa_meet events.
	Settings *entity.EventCallSettings
}

type UpdateEventInput struct {
	CalendarID      *uuid.UUID
	ChannelID       *uuid.UUID
	ChannelIDSet    bool
	Title           *string
	Description     *string
	DescriptionSet  bool
	Location        *entity.EventLocation
	ScheduledAt     *time.Time
	OriginatorTZ    *string
	OriginatorTZSet bool
	DurationMinutes *int
	AllDay          *bool
	Recurrence      *entity.RecurrenceRule
	RecurrenceSet   bool
	AttendeeUserIDs []uuid.UUID
	AttendeeEmails  []string
	RequiredUserIDs []uuid.UUID
	AttendeesSet    bool
	Reminders       []entity.EventReminder
	RemindersSet    bool
	// Settings is the optional per-event call-settings preconfig (ALK-819 / S11)
	// with key-presence tri-state semantics:
	//   - SettingsSet == false           -> key absent, leave existing unchanged
	//   - SettingsSet == true, Settings == nil   -> explicit JSON null, clear it
	//   - SettingsSet == true, Settings != nil    -> JSON object, replace it
	// The service additionally forces Settings to nil when the effective
	// location type is not aloqa_meet (a non-meet event cannot host a call).
	Settings    *entity.EventCallSettings
	SettingsSet bool
}

type MoveOccurrenceScope string

const (
	MoveScopeThis             MoveOccurrenceScope = "this"
	MoveScopeThisAndFollowing MoveOccurrenceScope = "this_and_following"
	MoveScopeAll              MoveOccurrenceScope = "all"
)

type MoveOccurrenceInput struct {
	InstanceAt         time.Time
	Scope              MoveOccurrenceScope
	NewScheduledAt     time.Time
	NewDurationMinutes *int
	ExpectedUpdatedAt  *time.Time
}

type MoveOccurrenceResult struct {
	Updated *entity.CalendarEvent
	Created *entity.CalendarEvent
}

func NewService(
	calendars repository.CalendarRepository,
	members repository.WorkspaceRepository,
	calls CallService,
	pubsub EventPublisher,
) *Service {
	return &Service{
		calendars: calendars,
		members:   members,
		calls:     calls,
		pubsub:    pubsub,
	}
}

func (s *Service) SetTransactionManager(manager txscope.Manager) {
	s.tx = manager
}

func (s *Service) ListUserCalendars(ctx context.Context, workspaceID, ownerID uuid.UUID) ([]entity.UserCalendar, error) {
	if err := s.requireWorkspaceMember(ctx, workspaceID, ownerID); err != nil {
		return nil, err
	}
	if _, err := s.calendars.EnsureDefaultCalendar(ctx, workspaceID, ownerID); err != nil {
		slog.WarnContext(ctx, "failed to ensure default calendar", "workspace_id", workspaceID, "user_id", ownerID, "error", err)
	}
	return s.calendars.ListUserCalendars(ctx, workspaceID, ownerID)
}

func (s *Service) CreateUserCalendar(ctx context.Context, workspaceID, ownerID uuid.UUID, name string, color entity.CalendarColor) (*entity.UserCalendar, error) {
	if err := s.requireWorkspaceMember(ctx, workspaceID, ownerID); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, cerrors.InvalidInput("calendar name is required")
	}
	if err := validateColor(color); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	calendar := &entity.UserCalendar{
		ID:          id.New(),
		OwnerID:     ownerID,
		WorkspaceID: workspaceID,
		Name:        name,
		Color:       color,
		IsVisible:   true,
		CreatedAt:   now,
	}
	if err := s.calendars.CreateUserCalendar(ctx, calendar); err != nil {
		return nil, err
	}
	s.publishCalendar(ctx, event.TypeUserCalendarCreated, *calendar, ownerID)
	return calendar, nil
}

func (s *Service) UpdateUserCalendar(ctx context.Context, workspaceID, ownerID, calendarID uuid.UUID, name *string, color *entity.CalendarColor) (*entity.UserCalendar, error) {
	if err := s.requireWorkspaceMember(ctx, workspaceID, ownerID); err != nil {
		return nil, err
	}
	calendar, err := s.calendars.GetUserCalendar(ctx, workspaceID, calendarID, ownerID)
	if err != nil {
		return nil, err
	}
	if name != nil {
		calendar.Name = strings.TrimSpace(*name)
		if calendar.Name == "" {
			return nil, cerrors.InvalidInput("calendar name is required")
		}
	}
	if color != nil {
		if err := validateColor(*color); err != nil {
			return nil, err
		}
		calendar.Color = *color
	}
	if err := s.calendars.UpdateUserCalendar(ctx, calendar); err != nil {
		return nil, err
	}
	s.publishCalendar(ctx, event.TypeUserCalendarUpdated, *calendar, ownerID)
	return calendar, nil
}

func (s *Service) DeleteUserCalendar(ctx context.Context, workspaceID, ownerID, calendarID uuid.UUID) error {
	if err := s.requireWorkspaceMember(ctx, workspaceID, ownerID); err != nil {
		return err
	}
	calendar, err := s.calendars.GetUserCalendar(ctx, workspaceID, calendarID, ownerID)
	if err != nil {
		return err
	}
	if err := s.calendars.DeleteUserCalendar(ctx, workspaceID, calendarID, ownerID); err != nil {
		return err
	}
	s.publishCalendar(ctx, event.TypeUserCalendarDeleted, *calendar, ownerID)
	return nil
}

func (s *Service) SetCalendarVisibility(ctx context.Context, workspaceID, ownerID, calendarID uuid.UUID, visible bool) (*entity.UserCalendar, error) {
	if err := s.requireWorkspaceMember(ctx, workspaceID, ownerID); err != nil {
		return nil, err
	}
	calendar, err := s.calendars.SetCalendarVisibility(ctx, workspaceID, calendarID, ownerID, visible)
	if err != nil {
		return nil, err
	}
	s.publishCalendar(ctx, event.TypeUserCalendarUpdated, *calendar, ownerID)
	return calendar, nil
}

func (s *Service) ListEvents(ctx context.Context, workspaceID uuid.UUID, fromTS, toTS time.Time, viewerID uuid.UUID) ([]entity.EventOccurrence, error) {
	if err := s.requireWorkspaceMember(ctx, workspaceID, viewerID); err != nil {
		return nil, err
	}
	if toTS.Before(fromTS) {
		return nil, cerrors.InvalidInput("to must be after from")
	}
	return s.calendars.ListEvents(ctx, workspaceID, fromTS, toTS, viewerID)
}

func (s *Service) ListUpcoming(ctx context.Context, workspaceID, viewerID uuid.UUID, withinMinutes int) ([]entity.EventOccurrence, error) {
	if err := s.requireWorkspaceMember(ctx, workspaceID, viewerID); err != nil {
		return nil, err
	}
	return s.calendars.ListUpcoming(ctx, workspaceID, viewerID, withinMinutes)
}

func (s *Service) GetEvent(ctx context.Context, workspaceID, eventID, viewerID uuid.UUID) (*entity.CalendarEvent, error) {
	if err := s.requireWorkspaceMember(ctx, workspaceID, viewerID); err != nil {
		return nil, err
	}
	eventEntity, err := s.calendars.GetEvent(ctx, eventID)
	if err != nil {
		return nil, err
	}
	if eventEntity.WorkspaceID != workspaceID {
		return nil, cerrors.NotFound("event not found")
	}
	if !canAccessEvent(eventEntity, viewerID) {
		return nil, cerrors.Forbidden("you do not have access to this event")
	}
	return eventEntity, nil
}

func (s *Service) CreateEvent(ctx context.Context, workspaceID, organizerID uuid.UUID, input CreateEventInput) (*entity.CalendarEvent, error) {
	if err := s.requireWorkspaceMember(ctx, workspaceID, organizerID); err != nil {
		return nil, err
	}
	if err := s.validateEventInput(input); err != nil {
		return nil, err
	}
	if _, err := s.calendars.GetUserCalendar(ctx, workspaceID, input.CalendarID, organizerID); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	eventEntity := &entity.CalendarEvent{
		ID:              id.New(),
		CalendarID:      input.CalendarID,
		WorkspaceID:     workspaceID,
		ChannelID:       input.ChannelID,
		OrganizerID:     organizerID,
		Title:           strings.TrimSpace(input.Title),
		Description:     input.Description,
		Location:        input.Location,
		ScheduledAt:     input.ScheduledAt.UTC(),
		OriginatorTZ:    normalizeOriginatorTZ(input.OriginatorTZ),
		DurationMinutes: input.DurationMinutes,
		AllDay:          input.AllDay,
		Recurrence:      normalizeRecurrence(input.Recurrence),
		Settings:        normalizeEventSettings(input.Settings, input.Location.Type),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	eventEntity.Attendees = buildAttendees(eventEntity.ID, input.AttendeeUserIDs, input.AttendeeEmails, input.RequiredUserIDs)
	eventEntity.Reminders = buildReminderRows(eventEntity.ID, organizerID, eventEntity.Attendees, input.Reminders)

	created, err := s.calendars.CreateEvent(ctx, eventEntity)
	if err != nil {
		return nil, err
	}
	s.publishEvent(ctx, event.TypeCalendarEventCreated, *created, organizerID)
	return created, nil
}

func (s *Service) UpdateEvent(ctx context.Context, workspaceID, eventID, actorID uuid.UUID, input UpdateEventInput) (*entity.CalendarEvent, error) {
	if err := s.requireWorkspaceMember(ctx, workspaceID, actorID); err != nil {
		return nil, err
	}
	existing, err := s.calendars.GetEvent(ctx, eventID)
	if err != nil {
		return nil, err
	}
	if existing.WorkspaceID != workspaceID {
		return nil, cerrors.NotFound("event not found")
	}
	if existing.OrganizerID != actorID {
		return nil, cerrors.Forbidden("only the organizer can update this event")
	}

	if input.CalendarID != nil {
		if _, err := s.calendars.GetUserCalendar(ctx, workspaceID, *input.CalendarID, actorID); err != nil {
			return nil, err
		}
		existing.CalendarID = *input.CalendarID
	}
	if input.ChannelIDSet {
		existing.ChannelID = input.ChannelID
	}
	if input.Title != nil {
		existing.Title = strings.TrimSpace(*input.Title)
	}
	if input.DescriptionSet {
		existing.Description = input.Description
	}
	if input.Location != nil {
		existing.Location = *input.Location
	}
	if input.ScheduledAt != nil {
		existing.ScheduledAt = input.ScheduledAt.UTC()
	}
	if input.OriginatorTZSet {
		existing.OriginatorTZ = normalizeOriginatorTZ("")
		if input.OriginatorTZ != nil {
			existing.OriginatorTZ = normalizeOriginatorTZ(*input.OriginatorTZ)
		}
	}
	if input.DurationMinutes != nil {
		existing.DurationMinutes = *input.DurationMinutes
	}
	if input.AllDay != nil {
		existing.AllDay = *input.AllDay
	}
	if input.RecurrenceSet {
		existing.Recurrence = normalizeRecurrence(input.Recurrence)
	}
	if input.AttendeesSet {
		existing.Attendees = buildAttendees(existing.ID, input.AttendeeUserIDs, input.AttendeeEmails, input.RequiredUserIDs)
	}
	if input.RemindersSet {
		existing.Reminders = input.Reminders
	}
	// Apply the settings tri-state after the location merge so the effective
	// location type (already merged into existing.Location) governs the drop:
	//   - absent (SettingsSet false): leave existing.Settings unchanged
	//   - explicit null: clear
	//   - object: replace
	// then force nil when the (effective) location is not aloqa_meet. (ALK-819.)
	if input.SettingsSet {
		existing.Settings = input.Settings
	}
	existing.Settings = normalizeEventSettings(existing.Settings, existing.Location.Type)
	if err := s.validateEventEntity(existing); err != nil {
		return nil, err
	}
	existing.Reminders = buildReminderRows(existing.ID, existing.OrganizerID, existing.Attendees, existing.Reminders)

	updated, err := s.calendars.UpdateEvent(ctx, existing)
	if err != nil {
		return nil, err
	}
	s.publishEvent(ctx, event.TypeCalendarEventUpdated, *updated, actorID)
	return updated, nil
}

func (s *Service) DeleteEvent(ctx context.Context, workspaceID, eventID, actorID uuid.UUID) error {
	if err := s.requireWorkspaceMember(ctx, workspaceID, actorID); err != nil {
		return err
	}
	existing, err := s.calendars.GetEvent(ctx, eventID)
	if err != nil {
		return err
	}
	if existing.WorkspaceID != workspaceID {
		return cerrors.NotFound("event not found")
	}
	if existing.OrganizerID != actorID {
		return cerrors.Forbidden("only the organizer can delete this event")
	}
	if err := s.calendars.DeleteEvent(ctx, workspaceID, eventID); err != nil {
		return err
	}
	s.publishEvent(ctx, event.TypeCalendarEventDeleted, *existing, actorID)
	return nil
}

func (s *Service) MoveEventOccurrence(
	ctx context.Context,
	workspaceID, eventID, actorID uuid.UUID,
	input MoveOccurrenceInput,
) (*MoveOccurrenceResult, error) {
	if err := s.requireWorkspaceMember(ctx, workspaceID, actorID); err != nil {
		return nil, err
	}
	switch input.Scope {
	case MoveScopeThis, MoveScopeThisAndFollowing, MoveScopeAll:
	default:
		return nil, cerrors.InvalidInput("INVALID_SCOPE: must be this, this_and_following, or all")
	}
	if input.NewDurationMinutes != nil && (*input.NewDurationMinutes < 0 || *input.NewDurationMinutes > 24*60) {
		return nil, cerrors.InvalidInput("INVALID_DURATION: duration_minutes must be 0-1440")
	}
	existing, err := s.calendars.GetEvent(ctx, eventID)
	if err != nil {
		return nil, err
	}
	if existing.WorkspaceID != workspaceID {
		return nil, cerrors.NotFound("event not found")
	}
	if existing.OrganizerID != actorID {
		return nil, cerrors.Forbidden("FORBIDDEN_NOT_ORGANIZER: only the organizer can move occurrences")
	}
	if existing.Recurrence == nil {
		return s.moveOccurrenceAll(ctx, existing, input)
	}
	switch input.Scope {
	case MoveScopeAll:
		return s.moveOccurrenceAll(ctx, existing, input)
	case MoveScopeThis:
		return s.moveOccurrenceThis(ctx, existing, input)
	default:
		return s.moveOccurrenceThisAndFollowing(ctx, existing, input)
	}
}

func deriveDuration(existing *entity.CalendarEvent, input MoveOccurrenceInput) int {
	if input.NewDurationMinutes != nil {
		return *input.NewDurationMinutes
	}
	return existing.DurationMinutes
}

func (s *Service) moveOccurrenceAll(ctx context.Context, existing *entity.CalendarEvent, input MoveOccurrenceInput) (*MoveOccurrenceResult, error) {
	existing.ScheduledAt = input.NewScheduledAt.UTC()
	existing.DurationMinutes = deriveDuration(existing, input)
	existing.UpdatedAt = time.Now().UTC()

	var updated *entity.CalendarEvent
	if input.ExpectedUpdatedAt == nil {
		u, err := s.calendars.UpdateEvent(ctx, existing)
		if err != nil {
			return nil, err
		}
		updated = u
	} else {
		if s.tx == nil {
			return nil, cerrors.Unavailable("transaction manager not configured")
		}
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Calendars() == nil {
				return cerrors.Unavailable("calendars repository not bound to tx")
			}
			u, err := scope.Calendars().UpdateEventTx(ctx, existing, input.ExpectedUpdatedAt)
			if err != nil {
				return err
			}
			updated = u
			return nil
		}); err != nil {
			return nil, err
		}
	}
	s.publishEvent(ctx, event.TypeCalendarEventUpdated, *updated, existing.OrganizerID)
	return &MoveOccurrenceResult{Updated: updated}, nil
}

func cloneEvent(src *entity.CalendarEvent) *entity.CalendarEvent {
	c := *src
	c.Attendees = append([]entity.EventAttendee(nil), src.Attendees...)
	c.Reminders = append([]entity.EventReminder(nil), src.Reminders...)
	if src.Recurrence != nil {
		c.Recurrence = &entity.RecurrenceRule{
			RRule:   src.Recurrence.RRule,
			Exdates: append([]time.Time(nil), src.Recurrence.Exdates...),
		}
	}
	return &c
}

func resetAttendees(src []entity.EventAttendee, newEventID uuid.UUID) []entity.EventAttendee {
	out := make([]entity.EventAttendee, len(src))
	for i, a := range src {
		out[i] = entity.EventAttendee{
			ID:         id.New(),
			EventID:    newEventID,
			UserID:     a.UserID,
			Email:      a.Email,
			IsRequired: a.IsRequired,
			RsvpStatus: entity.RsvpStatusNoResponse,
		}
	}
	return out
}

func copyReminders(src []entity.EventReminder, newEventID uuid.UUID) []entity.EventReminder {
	out := make([]entity.EventReminder, len(src))
	for i, r := range src {
		out[i] = entity.EventReminder{
			ID:            id.New(),
			EventID:       newEventID,
			UserID:        r.UserID,
			OffsetMinutes: r.OffsetMinutes,
			Channel:       r.Channel,
		}
	}
	return out
}

func buildOccurrenceChild(parent *entity.CalendarEvent, scheduledAt time.Time, durationMinutes int) *entity.CalendarEvent {
	now := time.Now().UTC()
	child := &entity.CalendarEvent{
		ID:              id.New(),
		CalendarID:      parent.CalendarID,
		WorkspaceID:     parent.WorkspaceID,
		ChannelID:       parent.ChannelID,
		OrganizerID:     parent.OrganizerID,
		Title:           parent.Title,
		Description:     parent.Description,
		Location:        parent.Location,
		ScheduledAt:     scheduledAt.UTC(),
		OriginatorTZ:    parent.OriginatorTZ,
		DurationMinutes: durationMinutes,
		AllDay:          parent.AllDay,
		Recurrence:      nil,
		CallID:          nil,
		// Carry the per-event call-settings preconfig onto the split-off occurrence
		// so a moved instance of an aloqa_meet recurring meeting keeps its schedule
		// preset instead of falling back to defaults. Deep-copy the pointer struct
		// (the child is a distinct row) and re-run the location normalisation so a
		// relocated child (non-meet location) drops the settings consistently with
		// create/update. (ALK-819 review.)
		Settings:  normalizeEventSettings(cloneEventCallSettings(parent.Settings), parent.Location.Type),
		CreatedAt: now,
		UpdatedAt: now,
	}
	child.Attendees = resetAttendees(parent.Attendees, child.ID)
	child.Reminders = copyReminders(parent.Reminders, child.ID)
	return child
}

// cloneEventCallSettings deep-copies the per-event call-settings preconfig so a
// derived event row (e.g. a moved recurring occurrence) owns its own pointers and
// never aliases the parent's (ALK-819 review).
func cloneEventCallSettings(src *entity.EventCallSettings) *entity.EventCallSettings {
	if src == nil {
		return nil
	}
	clone := &entity.EventCallSettings{}
	if src.EntryMode != nil {
		entryMode := *src.EntryMode
		clone.EntryMode = &entryMode
	}
	if src.MuteOnJoin != nil {
		muteOnJoin := *src.MuteOnJoin
		clone.MuteOnJoin = &muteOnJoin
	}
	if src.BreakoutCreation != nil {
		breakoutCreation := *src.BreakoutCreation
		clone.BreakoutCreation = &breakoutCreation
	}
	if src.MaxBreakoutRooms != nil {
		maxBreakoutRooms := *src.MaxBreakoutRooms
		clone.MaxBreakoutRooms = &maxBreakoutRooms
	}
	return clone
}

func recurrenceLocation(originatorTZ string) *time.Location {
	loc, err := time.LoadLocation(originatorTZ)
	if err != nil || loc == nil {
		return time.UTC
	}
	return loc
}

func (s *Service) moveOccurrenceThis(ctx context.Context, existing *entity.CalendarEvent, input MoveOccurrenceInput) (*MoveOccurrenceResult, error) {
	if s.tx == nil {
		return nil, cerrors.Unavailable("transaction manager required for scope=this")
	}
	for _, ex := range existing.Recurrence.Exdates {
		if ex.UTC().Equal(input.InstanceAt.UTC()) {
			return nil, cerrors.InvalidInput("INVALID_INSTANCE_EXDATED: instance_at was already moved or cancelled in a prior scope=this move")
		}
	}
	loc := recurrenceLocation(existing.OriginatorTZ)
	ok, err := calrrule.IsMember(existing.Recurrence.RRule, existing.ScheduledAt.In(loc), input.InstanceAt.UTC(), existing.Recurrence.Exdates)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, cerrors.InvalidInput("INVALID_INSTANCE: instance_at is not a member of the series")
	}

	var parentAfter, created *entity.CalendarEvent
	if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
		if scope.Calendars() == nil {
			return cerrors.Unavailable("calendar transaction scope not configured")
		}
		parent := cloneEvent(existing)
		parent.Recurrence = &entity.RecurrenceRule{
			RRule:   existing.Recurrence.RRule,
			Exdates: append(append([]time.Time(nil), existing.Recurrence.Exdates...), input.InstanceAt.UTC()),
		}
		parent.UpdatedAt = time.Now().UTC()
		updated, err := scope.Calendars().UpdateEventTx(ctx, parent, input.ExpectedUpdatedAt)
		if err != nil {
			return err
		}
		parentAfter = updated

		child := buildOccurrenceChild(existing, input.NewScheduledAt, deriveDuration(existing, input))
		inserted, err := scope.Calendars().CreateEventTx(ctx, child)
		if err != nil {
			return err
		}
		created = inserted
		return nil
	}); err != nil {
		return nil, err
	}
	s.publishEvent(ctx, event.TypeCalendarEventUpdated, *parentAfter, existing.OrganizerID)
	s.publishEvent(ctx, event.TypeCalendarEventCreated, *created, existing.OrganizerID)
	return &MoveOccurrenceResult{Updated: parentAfter, Created: created}, nil
}

func (s *Service) moveOccurrenceThisAndFollowing(ctx context.Context, existing *entity.CalendarEvent, input MoveOccurrenceInput) (*MoveOccurrenceResult, error) {
	for _, ex := range existing.Recurrence.Exdates {
		if ex.UTC().Equal(input.InstanceAt.UTC()) {
			return nil, cerrors.InvalidInput("INVALID_INSTANCE_EXDATED: instance_at was already moved or cancelled in a prior scope=this move")
		}
	}
	loc := recurrenceLocation(existing.OriginatorTZ)
	ok, err := calrrule.IsMember(existing.Recurrence.RRule, existing.ScheduledAt.In(loc), input.InstanceAt.UTC(), existing.Recurrence.Exdates)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, cerrors.InvalidInput("INVALID_INSTANCE: instance_at is not a member of the series")
	}
	if input.InstanceAt.UTC().Equal(existing.ScheduledAt.UTC()) {
		return s.moveOccurrenceAll(ctx, existing, input)
	}
	if s.tx == nil {
		return nil, cerrors.Unavailable("transaction manager required for scope=this_and_following")
	}

	delta := input.NewScheduledAt.UTC().Sub(input.InstanceAt.UTC())
	parentUntil := input.InstanceAt.UTC().Add(-time.Millisecond)
	var parentExdates, childExdates []time.Time
	for _, ex := range existing.Recurrence.Exdates {
		switch {
		case ex.UTC().Before(input.InstanceAt.UTC()):
			parentExdates = append(parentExdates, ex)
		case ex.UTC().After(input.InstanceAt.UTC()):
			childExdates = append(childExdates, ex.UTC().Add(delta))
		}
	}

	childRule, _, err := calrrule.ShiftBounds(existing.Recurrence.RRule, existing.ScheduledAt.UTC(), input.InstanceAt.UTC(), input.NewScheduledAt.UTC())
	if err != nil {
		return nil, err
	}

	var parentAfter, created *entity.CalendarEvent
	if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
		if scope.Calendars() == nil {
			return cerrors.Unavailable("calendar transaction scope not configured")
		}
		parentRRule, err := calrrule.SetUntil(existing.Recurrence.RRule, parentUntil)
		if err != nil {
			return err
		}
		parent := cloneEvent(existing)
		parent.Recurrence = &entity.RecurrenceRule{RRule: parentRRule, Exdates: parentExdates}
		parent.UpdatedAt = time.Now().UTC()
		updated, err := scope.Calendars().UpdateEventTx(ctx, parent, input.ExpectedUpdatedAt)
		if err != nil {
			return err
		}
		parentAfter = updated

		child := buildOccurrenceChild(existing, input.NewScheduledAt, deriveDuration(existing, input))
		child.Recurrence = &entity.RecurrenceRule{RRule: childRule, Exdates: childExdates}
		inserted, err := scope.Calendars().CreateEventTx(ctx, child)
		if err != nil {
			return err
		}
		created = inserted
		return nil
	}); err != nil {
		return nil, err
	}
	s.publishEvent(ctx, event.TypeCalendarEventUpdated, *parentAfter, existing.OrganizerID)
	s.publishEvent(ctx, event.TypeCalendarEventCreated, *created, existing.OrganizerID)
	return &MoveOccurrenceResult{Updated: parentAfter, Created: created}, nil
}

func (s *Service) UpsertRsvp(ctx context.Context, workspaceID, eventID, userID uuid.UUID, status entity.RsvpStatus) (*entity.EventAttendee, error) {
	if err := s.requireWorkspaceMember(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	if status != entity.RsvpStatusGoing && status != entity.RsvpStatusMaybe && status != entity.RsvpStatusDeclined {
		return nil, cerrors.InvalidInput("invalid RSVP status")
	}
	eventEntity, err := s.calendars.GetEvent(ctx, eventID)
	if err != nil {
		return nil, err
	}
	if eventEntity.WorkspaceID != workspaceID {
		return nil, cerrors.NotFound("event not found")
	}
	if !canAccessEvent(eventEntity, userID) {
		return nil, cerrors.Forbidden("you do not have access to this event")
	}
	attendee, err := s.calendars.UpsertRsvp(ctx, eventID, userID, status)
	if err != nil {
		return nil, err
	}
	s.publishRsvp(ctx, eventEntity, *attendee)
	return attendee, nil
}

func (s *Service) StartCallFromEvent(ctx context.Context, workspaceID, eventID, initiatorID uuid.UUID) (*entity.Call, error) {
	if err := s.requireWorkspaceMember(ctx, workspaceID, initiatorID); err != nil {
		return nil, err
	}
	if s.tx != nil {
		return s.startCallFromEventTx(ctx, workspaceID, eventID, initiatorID)
	}
	eventEntity, err := s.calendars.GetEvent(ctx, eventID)
	if err != nil {
		return nil, err
	}
	if eventEntity.WorkspaceID != workspaceID {
		return nil, cerrors.NotFound("event not found")
	}
	if !canAccessEvent(eventEntity, initiatorID) {
		return nil, cerrors.Forbidden("you do not have access to this event")
	}
	if s.calls == nil {
		return nil, cerrors.Unavailable("call service is not configured")
	}
	if eventEntity.CallID != nil {
		callEntity, err := s.calls.GetCall(ctx, workspaceID, *eventEntity.CallID, initiatorID)
		if err != nil {
			return nil, err
		}
		if callEntity.Status != entity.CallStatusEnded {
			if err := s.calls.EnsureLiveKitRoomRequired(ctx, callEntity); err != nil {
				return nil, err
			}
			return callEntity, nil
		}
	}

	// Validate + reject password entry mode up front via the shared chokepoint so
	// this path returns the SAME error class as the tx path for a corrupted/legacy
	// event settings row (StartCall would also reject password mode for lack of a
	// join password, but going through the helper makes the rejection explicit and
	// consistent across both paths). Then hand the pre-finalisation base to
	// StartCall, which finalises it identically (ALK-819 / S11 + review).
	if _, err := s.resolveScheduledCallSettings(eventEntity.Settings); err != nil {
		return nil, err
	}
	callSettings := baseWithEventOverlay(eventEntity.Settings)
	callEntity, err := s.calls.StartCall(ctx, workspaceID, initiatorID, entity.CallTypeMeeting, eventEntity.Title, eventEntity.ChannelID, callSettings, "")
	if err != nil {
		return nil, err
	}
	if err := s.calendars.LinkEventAndCall(ctx, eventID, callEntity.ID); err != nil {
		return nil, err
	}
	scheduledCallID := eventID
	callEntity.ScheduledCallID = &scheduledCallID
	updated, err := s.calendars.GetEvent(ctx, eventID)
	if err != nil {
		return nil, err
	}
	s.publishEvent(ctx, event.TypeCalendarEventUpdated, *updated, initiatorID)
	return callEntity, nil
}

// startCallFromEventTx builds and persists the scheduled call inline within a
// single transaction (rather than delegating to call.Service.StartCall). It uses
// EVENT-SCOPED idempotency: the call is reused when the locked event already
// points at a non-ended call (the eventEntity.CallID != nil branch below), so
// concurrent "start" presses on the same scheduled event converge on one call.
// This intentionally does NOT run StartCall's per-channel and per-user
// single-active-call guards (9b/9c): a scheduled meeting is keyed by its event,
// not by its channel or initiator, so those ad-hoc guards do not apply here. The
// asymmetry with StartCall is deliberate, not an oversight (ALK-819 review).
func (s *Service) startCallFromEventTx(ctx context.Context, workspaceID, eventID, initiatorID uuid.UUID) (*entity.Call, error) {
	var callEntity *entity.Call
	var preparedLiveKitRoomID *uuid.UUID
	err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
		if scope.Calendars() == nil || scope.Calls() == nil {
			return cerrors.Unavailable("calendar call transaction scope is not configured")
		}
		eventEntity, err := scope.Calendars().LockEventForStartCall(ctx, eventID)
		if err != nil {
			return err
		}
		if eventEntity.WorkspaceID != workspaceID {
			return cerrors.NotFound("event not found")
		}
		if !canAccessEvent(eventEntity, initiatorID) {
			return cerrors.Forbidden("you do not have access to this event")
		}
		if eventEntity.CallID != nil {
			existing, err := scope.Calls().GetByID(ctx, *eventEntity.CallID)
			if err != nil {
				if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
					return err
				}
			} else if existing.Status != entity.CallStatusEnded {
				if s.calls != nil {
					if err := s.calls.EnsureLiveKitRoomRequired(ctx, existing); err != nil {
						return err
					}
				}
				callEntity = existing
				return nil
			}
		}

		now := time.Now().UTC()
		scheduledCallID := eventEntity.ID
		// This path writes callEntity.Settings directly via scope.Calls().Create
		// without going through call.Service.StartCall, so the call-service settings
		// validation + finaliser MUST be applied here on the same base StartCall
		// receives on the non-tx path: resolveScheduledCallSettings overlays the
		// event preconfig onto the meeting defaults, validates the fields, resolves
		// entry_mode/waiting_room/breakout + the server invariants, and rejects
		// password entry mode. Routing both paths through it guarantees identical
		// persisted settings and the same rejection class (ALK-819 / S11 + review).
		// s.calls is required for this path (the helper enforces it).
		callSettings, err := s.resolveScheduledCallSettings(eventEntity.Settings)
		if err != nil {
			return err
		}
		callEntity = &entity.Call{
			ID:              id.New(),
			WorkspaceID:     workspaceID,
			ChannelID:       eventEntity.ChannelID,
			Type:            entity.CallTypeMeeting,
			Status:          entity.CallStatusRinging,
			Title:           eventEntity.Title,
			CreatedBy:       initiatorID,
			ScheduledCallID: &scheduledCallID,
			Settings:        callSettings,
			StartedAt:       &now,
			CreatedAt:       now,
		}
		participant := &entity.CallParticipant{
			ID:       id.New(),
			CallID:   callEntity.ID,
			UserID:   initiatorID,
			Role:     entity.CallRoleHost,
			Status:   entity.ParticipantStatusConnected,
			JoinedAt: &now,
		}
		if s.calls != nil {
			if err := s.calls.EnsureLiveKitRoomRequired(ctx, callEntity); err != nil {
				return err
			}
			preparedLiveKitRoomID = &callEntity.ID
		}
		if err := scope.Calls().Create(ctx, callEntity); err != nil {
			return err
		}
		if err := scope.Calls().AddParticipant(ctx, participant); err != nil {
			return err
		}
		if err := scope.Calendars().LinkEventAndCall(ctx, eventEntity.ID, callEntity.ID); err != nil {
			return err
		}
		eventEntity.CallID = &callEntity.ID
		if err := s.enqueueTx(ctx, scope, event.TypeCallStarted, fmt.Sprintf("aloqa.ws.%s", workspaceID), workspaceID, channelIDOrNil(eventEntity.ChannelID), initiatorID, event.CallPayload{Call: callEntity}); err != nil {
			return err
		}
		return s.enqueueTx(ctx, scope, event.TypeCalendarEventUpdated, calendarSubject(workspaceID), workspaceID, channelIDOrNil(eventEntity.ChannelID), initiatorID, event.CalendarEventPayload{Event: *eventEntity})
	})
	if err != nil {
		if preparedLiveKitRoomID != nil && s.calls != nil {
			if cleanupErr := s.calls.DeleteLiveKitRoom(ctx, *preparedLiveKitRoomID); cleanupErr != nil {
				slog.WarnContext(ctx, "failed to clean up livekit room after scheduled call rollback", "call_id", *preparedLiveKitRoomID, "error", cleanupErr)
			}
		}
		return nil, err
	}
	return callEntity, nil
}

func (s *Service) ListAndDispatchReminders(ctx context.Context, now time.Time) error {
	now = now.UTC()
	if s.tx == nil {
		return cerrors.Unavailable("calendar transaction manager is not configured")
	}
	return s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
		if scope.Calendars() == nil {
			return cerrors.Unavailable("calendar transaction scope is not configured")
		}
		return s.dispatchDueReminders(ctx, scope.Calendars(), now)
	})
}

func (s *Service) dispatchDueReminders(ctx context.Context, calendars repository.CalendarRepository, now time.Time) error {
	targets, err := calendars.ListDueReminderTargets(ctx, now, reminderEventLookaheadHorizon, reminderDispatchLimit)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if target.ReminderID == uuid.Nil {
			return cerrors.Internal("reminder target is missing reminder id", nil)
		}
		payload, err := prepareReminderPayload(target, now)
		if err != nil {
			return err
		}
		if err := calendars.EnqueueReminderOutbox(ctx, target, payload, now); err != nil {
			return err
		}
		if err := calendars.MarkReminderDispatched(ctx, target.ReminderID, target.Occurrence.InstanceAt, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) PublishPendingReminderOutbox(ctx context.Context) (processed, failed, dead int, err error) {
	if s.calendars == nil {
		return 0, 0, 0, cerrors.Unavailable("calendar repository is not configured")
	}
	return s.calendars.PublishReminderOutbox(ctx, reminderOutboxBatchSize, reminderOutboxMaxAttempts, func(ctx context.Context, msg entity.ReminderOutboxMessage) error {
		if s.pubsub == nil {
			return nil
		}
		return s.pubsub.Publish(ctx, userEventsSubject(msg.UserID), msg.PayloadJSON)
	})
}

func (s *Service) requireWorkspaceMember(ctx context.Context, workspaceID, userID uuid.UUID) error {
	if _, err := s.members.GetMember(ctx, workspaceID, userID); err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.Forbidden("user is not a member of this workspace")
		}
		return cerrors.Internal("failed to verify workspace membership", err)
	}
	return nil
}

func (s *Service) validateEventInput(input CreateEventInput) error {
	eventEntity := &entity.CalendarEvent{
		Title:           input.Title,
		Location:        input.Location,
		ScheduledAt:     input.ScheduledAt,
		DurationMinutes: input.DurationMinutes,
		Recurrence:      input.Recurrence,
	}
	return s.validateEventEntity(eventEntity)
}

func (s *Service) validateEventEntity(eventEntity *entity.CalendarEvent) error {
	if strings.TrimSpace(eventEntity.Title) == "" {
		return cerrors.InvalidInput("event title is required")
	}
	if len(eventEntity.Title) > 200 {
		return cerrors.InvalidInput("event title is too long")
	}
	if eventEntity.Location.Type == "" {
		return cerrors.InvalidInput("event location type is required")
	}
	switch eventEntity.Location.Type {
	case entity.EventLocationAloqaMeet, entity.EventLocationExternalLink, entity.EventLocationInPerson:
	case entity.EventLocationRoom:
		return cerrors.Unavailable("room locations are not supported yet")
	default:
		return cerrors.InvalidInput("invalid location type")
	}
	if eventEntity.DurationMinutes < 0 || eventEntity.DurationMinutes > 24*60 {
		return cerrors.InvalidInput("duration_minutes must be between 0 and 1440")
	}
	if eventEntity.ScheduledAt.IsZero() {
		return cerrors.InvalidInput("scheduled_at is required")
	}
	if eventEntity.Recurrence != nil && strings.TrimSpace(eventEntity.Recurrence.RRule) == "" {
		return cerrors.InvalidInput("recurrence rr_rule is required")
	}
	return nil
}

func validateColor(color entity.CalendarColor) error {
	switch color {
	case entity.CalendarColorBrand, entity.CalendarColorViolet, entity.CalendarColorTeal, entity.CalendarColorAmber, entity.CalendarColorWarning, entity.CalendarColorMuted:
		return nil
	default:
		return cerrors.InvalidInput("invalid calendar color")
	}
}

func normalizeRecurrence(recurrence *entity.RecurrenceRule) *entity.RecurrenceRule {
	if recurrence == nil {
		return nil
	}
	rrule := strings.TrimSpace(recurrence.RRule)
	if rrule == "" {
		return nil
	}
	return &entity.RecurrenceRule{RRule: rrule, Exdates: recurrence.Exdates}
}

// normalizeEventSettings drops the per-event call-settings preconfig for any
// location type other than aloqa_meet (a non-meet event cannot host a call), and
// otherwise returns the settings unchanged (ALK-819 / S11).
func normalizeEventSettings(settings *entity.EventCallSettings, locationType entity.EventLocationType) *entity.EventCallSettings {
	if locationType != entity.EventLocationAloqaMeet {
		return nil
	}
	return settings
}

// meetingDefaultCallSettings is the canonical base call-settings tuple used when
// starting a call from a scheduled event. An event preconfig is overlaid on top
// of this and the result normalised before the call is created (ALK-819 / S11).
func meetingDefaultCallSettings() entity.CallSettings {
	return entity.CallSettings{
		WaitingRoom:      false,
		MuteOnJoin:       false,
		Recording:        false,
		ScreenSharing:    true,
		Chat:             true,
		BreakoutRooms:    true,
		BreakoutCreation: entity.BreakoutCreationHost,
		MaxBreakoutRooms: 8,
		MaxParticipants:  500,
		E2EE:             false,
		Watermark:        false,
		EntryMode:        entity.EntryModeOpen,
	}
}

// overlayEventSettings applies the present (non-nil) pointer fields of the event
// preconfig onto the base call settings; absent fields keep the base value. It
// mutates the base in place (ALK-819 / S11).
func overlayEventSettings(base *entity.CallSettings, settings *entity.EventCallSettings) {
	if base == nil || settings == nil {
		return
	}
	if settings.EntryMode != nil {
		base.EntryMode = *settings.EntryMode
	}
	if settings.MuteOnJoin != nil {
		base.MuteOnJoin = *settings.MuteOnJoin
	}
	if settings.BreakoutCreation != nil {
		base.BreakoutCreation = *settings.BreakoutCreation
	}
	if settings.MaxBreakoutRooms != nil {
		base.MaxBreakoutRooms = *settings.MaxBreakoutRooms
	}
}

// baseWithEventOverlay builds the pre-finalisation call-settings tuple to use
// when starting a call from an event: the canonical meeting defaults overlaid
// with the event preconfig (if any). The result is handed to
// CallService.FinalizeNewCallSettings, which resolves entry_mode / waiting_room
// and the breakout fields and applies the server-authoritative invariants — so
// BOTH start-call paths (non-tx via StartCall, tx via the direct write) run the
// same finaliser and persist byte-identical settings (ALK-819 / S11 + review).
func baseWithEventOverlay(eventSettings *entity.EventCallSettings) entity.CallSettings {
	base := meetingDefaultCallSettings()
	overlayEventSettings(&base, eventSettings)
	return base
}

// resolveScheduledCallSettings is the single chokepoint both StartCallFromEvent
// paths run to turn a stored event preconfig into the finalised, validated call
// settings tuple. It (1) validates the pre-finalisation overlay with the call
// service's field validation (the same StartCall runs), (2) finalises it, and
// (3) rejects password entry mode — a scheduled meeting can never carry a join
// password (none is available at scheduled start), so a corrupted/legacy event
// settings row with entry_mode=password is refused on BOTH paths with the same
// InvalidInput error class rather than persisting an unusable password-mode call.
// Returns the finalised settings the call row must carry (ALK-819 review).
func (s *Service) resolveScheduledCallSettings(eventSettings *entity.EventCallSettings) (entity.CallSettings, error) {
	if s.calls == nil {
		return entity.CallSettings{}, cerrors.Unavailable("call service is not configured")
	}
	base := baseWithEventOverlay(eventSettings)
	if err := s.calls.ValidateNewCallSettings(entity.CallTypeMeeting, base); err != nil {
		return entity.CallSettings{}, err
	}
	finalized := s.calls.FinalizeNewCallSettings(entity.CallTypeMeeting, base)
	if finalized.EntryMode == entity.EntryModePassword {
		return entity.CallSettings{}, cerrors.InvalidInput("scheduled meetings cannot use password entry mode")
	}
	return finalized, nil
}

func normalizeOriginatorTZ(originatorTZ string) string {
	originatorTZ = strings.TrimSpace(originatorTZ)
	if originatorTZ == "" {
		return "UTC"
	}
	return originatorTZ
}

func buildAttendees(eventID uuid.UUID, userIDs []uuid.UUID, emails []string, requiredUserIDs []uuid.UUID) []entity.EventAttendee {
	required := make(map[uuid.UUID]struct{}, len(requiredUserIDs))
	for _, userID := range requiredUserIDs {
		required[userID] = struct{}{}
	}
	seenUsers := map[uuid.UUID]struct{}{}
	attendees := make([]entity.EventAttendee, 0, len(userIDs)+len(emails))
	for _, userID := range userIDs {
		if userID == uuid.Nil {
			continue
		}
		if _, ok := seenUsers[userID]; ok {
			continue
		}
		seenUsers[userID] = struct{}{}
		uid := userID
		_, isRequired := required[userID]
		attendees = append(attendees, entity.EventAttendee{
			ID:         id.New(),
			EventID:    eventID,
			UserID:     &uid,
			IsRequired: isRequired,
			RsvpStatus: entity.RsvpStatusNoResponse,
		})
	}
	seenEmails := map[string]struct{}{}
	for _, rawEmail := range emails {
		email := strings.TrimSpace(strings.ToLower(rawEmail))
		if email == "" {
			continue
		}
		if _, ok := seenEmails[email]; ok {
			continue
		}
		seenEmails[email] = struct{}{}
		value := email
		attendees = append(attendees, entity.EventAttendee{
			ID:         id.New(),
			EventID:    eventID,
			Email:      &value,
			IsRequired: true,
			RsvpStatus: entity.RsvpStatusNoResponse,
		})
	}
	return attendees
}

func buildReminderRows(eventID, organizerID uuid.UUID, attendees []entity.EventAttendee, reminders []entity.EventReminder) []entity.EventReminder {
	if len(reminders) == 0 {
		return nil
	}
	userIDs := []uuid.UUID{organizerID}
	for _, attendee := range attendees {
		if attendee.UserID != nil {
			userIDs = append(userIDs, *attendee.UserID)
		}
	}
	var rows []entity.EventReminder
	seen := map[string]struct{}{}
	for _, userID := range userIDs {
		for _, reminder := range reminders {
			if reminder.OffsetMinutes < 0 || reminder.OffsetMinutes > maxReminderOffsetMinutes {
				continue
			}
			if reminder.Channel != entity.ReminderChannelInApp && reminder.Channel != entity.ReminderChannelOS {
				continue
			}
			key := fmt.Sprintf("%s:%d:%s", userID, reminder.OffsetMinutes, reminder.Channel)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			rows = append(rows, entity.EventReminder{
				ID:            id.New(),
				EventID:       eventID,
				UserID:        userID,
				OffsetMinutes: reminder.OffsetMinutes,
				Channel:       reminder.Channel,
			})
		}
	}
	return rows
}

func canAccessEvent(eventEntity *entity.CalendarEvent, userID uuid.UUID) bool {
	if eventEntity.OrganizerID == userID {
		return true
	}
	for _, attendee := range eventEntity.Attendees {
		if attendee.UserID != nil && *attendee.UserID == userID {
			return true
		}
	}
	return false
}

func affectedCalendarUserIDs(eventEntity *entity.CalendarEvent, attendee entity.EventAttendee) []uuid.UUID {
	seen := map[uuid.UUID]struct{}{}
	add := func(userID uuid.UUID, out *[]uuid.UUID) {
		if userID == uuid.Nil {
			return
		}
		if _, ok := seen[userID]; ok {
			return
		}
		seen[userID] = struct{}{}
		*out = append(*out, userID)
	}

	userIDs := make([]uuid.UUID, 0, len(eventEntity.Attendees)+2)
	add(eventEntity.OrganizerID, &userIDs)
	if attendee.UserID != nil {
		add(*attendee.UserID, &userIDs)
	}
	for _, eventAttendee := range eventEntity.Attendees {
		if eventAttendee.UserID != nil {
			add(*eventAttendee.UserID, &userIDs)
		}
	}
	return userIDs
}

func channelIDOrNil(channelID *uuid.UUID) uuid.UUID {
	if channelID == nil {
		return uuid.Nil
	}
	return *channelID
}

func (s *Service) publishCalendar(ctx context.Context, eventType event.Type, calendarEntity entity.UserCalendar, userID uuid.UUID) {
	_ = s.publish(ctx, eventType, calendarSubject(calendarEntity.WorkspaceID), calendarEntity.WorkspaceID, uuid.Nil, userID, event.UserCalendarPayload{Calendar: calendarEntity})
}

func (s *Service) publishEvent(ctx context.Context, eventType event.Type, calendarEvent entity.CalendarEvent, userID uuid.UUID) {
	_ = s.publish(ctx, eventType, calendarSubject(calendarEvent.WorkspaceID), calendarEvent.WorkspaceID, channelIDOrNil(calendarEvent.ChannelID), userID, event.CalendarEventPayload{Event: calendarEvent})
}

func (s *Service) publishRsvp(ctx context.Context, eventEntity *entity.CalendarEvent, attendee entity.EventAttendee) {
	payload := event.CalendarRsvpPayload{EventID: eventEntity.ID, Attendee: attendee}
	for _, userID := range affectedCalendarUserIDs(eventEntity, attendee) {
		_ = s.publish(ctx, event.TypeCalendarAttendeeRsvpUpdated, userEventsSubject(userID), eventEntity.WorkspaceID, uuid.Nil, userID, payload)
	}
}

func prepareReminderPayload(target entity.ReminderTarget, timestamp time.Time) ([]byte, error) {
	_, body, _, err := event.Prepare(userEventsSubject(target.UserID), event.Event{
		Type:        event.TypeCalendarEventReminderFired,
		WorkspaceID: target.Occurrence.WorkspaceID,
		ChannelID:   uuid.Nil,
		UserID:      target.UserID,
		Timestamp:   timestamp.UTC(),
		Payload:     event.CalendarEventPayload{Event: target.Occurrence.CalendarEvent},
	})
	if err != nil {
		return nil, fmt.Errorf("prepare reminder event: %w", err)
	}
	return body, nil
}

func (s *Service) enqueueTx(ctx context.Context, scope txscope.Scope, eventType event.Type, subject string, workspaceID, channelID, userID uuid.UUID, payload any) error {
	evt, body, _, err := event.Prepare(subject, event.Event{
		Type:        eventType,
		WorkspaceID: workspaceID,
		ChannelID:   channelID,
		UserID:      userID,
		Timestamp:   time.Now().UTC(),
		Payload:     payload,
	})
	if err != nil {
		return fmt.Errorf("prepare calendar realtime event: %w", err)
	}
	return scope.EnqueueRealtime(ctx, evt, body)
}

func (s *Service) publish(ctx context.Context, eventType event.Type, subject string, workspaceID, channelID, userID uuid.UUID, payload any) error {
	if s.pubsub == nil {
		return nil
	}
	evt := event.Event{
		ID:          id.New(),
		Type:        eventType,
		WorkspaceID: workspaceID,
		ChannelID:   channelID,
		UserID:      userID,
		Timestamp:   time.Now().UTC(),
		Payload:     payload,
	}
	data, err := json.Marshal(evt)
	if err != nil {
		slog.ErrorContext(ctx, "failed to marshal calendar event", "type", eventType, "error", err)
		return fmt.Errorf("marshal calendar event: %w", err)
	}
	if err := s.pubsub.Publish(ctx, subject, data); err != nil {
		slog.ErrorContext(ctx, "failed to publish calendar event", "type", eventType, "subject", subject, "error", err)
		return fmt.Errorf("publish calendar event: %w", err)
	}
	return nil
}

func calendarSubject(workspaceID uuid.UUID) string {
	return fmt.Sprintf("aloqa.ws.%s.calendar", workspaceID)
}

func userEventsSubject(userID uuid.UUID) string {
	return fmt.Sprintf("aloqa.ws.%s.events", userID)
}
