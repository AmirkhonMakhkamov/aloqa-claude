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
	"aloqa/internal/platform/txscope"
)

type EventPublisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

type CallService interface {
	StartCall(ctx context.Context, workspaceID, userID uuid.UUID, callType entity.CallType, title string, channelID *uuid.UUID, settings entity.CallSettings) (*entity.Call, error)
	GetCall(ctx context.Context, workspaceID, callID, userID uuid.UUID) (*entity.Call, error)
}

type Service struct {
	calendars repository.CalendarRepository
	members   repository.WorkspaceRepository
	calls     CallService
	pubsub    EventPublisher
	tx        txscope.Manager
}

const (
	reminderDispatchLimit     = 100
	reminderDiscoveryHorizon  = 24 * time.Hour
	reminderOutboxBatchSize   = 100
	reminderOutboxMaxAttempts = 10
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
			return callEntity, nil
		}
	}

	callEntity, err := s.calls.StartCall(ctx, workspaceID, initiatorID, entity.CallTypeMeeting, eventEntity.Title, eventEntity.ChannelID, entity.CallSettings{
		WaitingRoom:     false,
		MuteOnJoin:      false,
		Recording:       false,
		ScreenSharing:   true,
		Chat:            true,
		BreakoutRooms:   false,
		MaxParticipants: 500,
	})
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

func (s *Service) startCallFromEventTx(ctx context.Context, workspaceID, eventID, initiatorID uuid.UUID) (*entity.Call, error) {
	var callEntity *entity.Call
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
				callEntity = existing
				return nil
			}
		}

		now := time.Now().UTC()
		scheduledCallID := eventEntity.ID
		callEntity = &entity.Call{
			ID:              id.New(),
			WorkspaceID:     workspaceID,
			ChannelID:       eventEntity.ChannelID,
			Type:            entity.CallTypeMeeting,
			Status:          entity.CallStatusRinging,
			Title:           eventEntity.Title,
			CreatedBy:       initiatorID,
			ScheduledCallID: &scheduledCallID,
			Settings: entity.CallSettings{
				WaitingRoom:     false,
				MuteOnJoin:      false,
				Recording:       false,
				ScreenSharing:   true,
				Chat:            true,
				BreakoutRooms:   false,
				MaxParticipants: 500,
			},
			StartedAt: &now,
			CreatedAt: now,
		}
		participant := &entity.CallParticipant{
			ID:       id.New(),
			CallID:   callEntity.ID,
			UserID:   initiatorID,
			Role:     entity.CallRoleHost,
			Status:   entity.ParticipantStatusConnected,
			JoinedAt: &now,
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
	targets, err := calendars.ListDueReminderTargets(ctx, now, reminderDiscoveryHorizon, reminderDispatchLimit)
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
			if reminder.OffsetMinutes < 0 || reminder.OffsetMinutes > 10080 {
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

func (s *Service) publishReminder(ctx context.Context, target entity.ReminderTarget) error {
	payload, err := prepareReminderPayload(target, time.Now().UTC())
	if err != nil {
		return err
	}
	if s.pubsub == nil {
		return nil
	}
	return s.pubsub.Publish(ctx, userEventsSubject(target.UserID), payload)
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
