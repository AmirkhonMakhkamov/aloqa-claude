package calendar

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/pkg/pagination"
)

func TestStartCallFromEventIsIdempotent(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	eventID := uuid.New()
	repo := &fakeCalendarRepo{
		events: map[uuid.UUID]*entity.CalendarEvent{
			eventID: {
				ID:          eventID,
				WorkspaceID: workspaceID,
				OrganizerID: organizerID,
				Title:       "Planning",
			},
		},
	}
	calls := &fakeCallService{calls: map[uuid.UUID]*entity.Call{}}
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{workspaceID, organizerID}: true}}, calls, noopPublisher{})

	first, err := svc.StartCallFromEvent(ctx, workspaceID, eventID, organizerID)
	if err != nil {
		t.Fatalf("first StartCallFromEvent error = %v", err)
	}
	second, err := svc.StartCallFromEvent(ctx, workspaceID, eventID, organizerID)
	if err != nil {
		t.Fatalf("second StartCallFromEvent error = %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("call IDs differ: first=%s second=%s", first.ID, second.ID)
	}
	if calls.starts != 1 {
		t.Fatalf("starts = %d, want 1", calls.starts)
	}
}

func TestUpsertRsvpPublishesRsvpEvent(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	repo := &fakeCalendarRepo{
		events: map[uuid.UUID]*entity.CalendarEvent{
			eventID: {
				ID:          eventID,
				WorkspaceID: workspaceID,
				OrganizerID: organizerID,
				Attendees: []entity.EventAttendee{{
					ID:         uuid.New(),
					EventID:    eventID,
					UserID:     &userID,
					IsRequired: true,
					RsvpStatus: entity.RsvpStatusNoResponse,
				}},
			},
		},
	}
	pub := &capturingPublisher{}
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{workspaceID, userID}: true}}, nil, pub)

	if _, err := svc.UpsertRsvp(ctx, workspaceID, eventID, userID, entity.RsvpStatusGoing); err != nil {
		t.Fatalf("UpsertRsvp error = %v", err)
	}
	if len(pub.events) != 2 {
		t.Fatalf("published events = %d, want workspace + user event", len(pub.events))
	}
	if pub.events[0].Type != event.TypeCalendarAttendeeRsvpUpdated {
		t.Fatalf("event type = %s", pub.events[0].Type)
	}
	if pub.subjects[1] != userEventsSubject(workspaceID, userID) {
		t.Fatalf("user subject = %q, want %q", pub.subjects[1], userEventsSubject(workspaceID, userID))
	}
}

func TestListAndDispatchRemindersDeduplicates(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	repo := &fakeCalendarRepo{
		reminders: []entity.ReminderTarget{{
			UserID:        userID,
			OffsetMinutes: 10,
			Occurrence: entity.EventOccurrence{
				CalendarEvent: entity.CalendarEvent{
					ID:          eventID,
					WorkspaceID: workspaceID,
					OrganizerID: userID,
					Title:       "Standup",
				},
				InstanceAt: now.Add(10 * time.Minute),
			},
		}},
	}
	pub := &capturingPublisher{}
	svc := NewService(repo, fakeMembers{}, nil, pub)

	if err := svc.ListAndDispatchReminders(ctx, now); err != nil {
		t.Fatalf("first dispatch error = %v", err)
	}
	if err := svc.ListAndDispatchReminders(ctx, now.Add(time.Second)); err != nil {
		t.Fatalf("second dispatch error = %v", err)
	}
	if len(pub.events) != 1 {
		t.Fatalf("published reminder events = %d, want 1", len(pub.events))
	}
	if pub.events[0].Type != event.TypeCalendarEventReminderFired {
		t.Fatalf("event type = %s", pub.events[0].Type)
	}
}

type fakeCalendarRepo struct {
	calendars []entity.UserCalendar
	events    map[uuid.UUID]*entity.CalendarEvent
	reminders []entity.ReminderTarget
}

func (r *fakeCalendarRepo) ListUserCalendars(context.Context, uuid.UUID, uuid.UUID) ([]entity.UserCalendar, error) {
	return r.calendars, nil
}

func (r *fakeCalendarRepo) GetUserCalendar(_ context.Context, workspaceID, calendarID, ownerID uuid.UUID) (*entity.UserCalendar, error) {
	for _, calendar := range r.calendars {
		if calendar.ID == calendarID && calendar.WorkspaceID == workspaceID && calendar.OwnerID == ownerID {
			copy := calendar
			return &copy, nil
		}
	}
	return &entity.UserCalendar{ID: calendarID, WorkspaceID: workspaceID, OwnerID: ownerID}, nil
}

func (r *fakeCalendarRepo) CreateUserCalendar(_ context.Context, calendar *entity.UserCalendar) error {
	r.calendars = append(r.calendars, *calendar)
	return nil
}

func (r *fakeCalendarRepo) UpdateUserCalendar(context.Context, *entity.UserCalendar) error {
	return nil
}
func (r *fakeCalendarRepo) DeleteUserCalendar(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *fakeCalendarRepo) SetCalendarVisibility(_ context.Context, workspaceID, calendarID, ownerID uuid.UUID, visible bool) (*entity.UserCalendar, error) {
	return &entity.UserCalendar{ID: calendarID, WorkspaceID: workspaceID, OwnerID: ownerID, IsVisible: visible}, nil
}
func (r *fakeCalendarRepo) EnsureDefaultCalendar(_ context.Context, workspaceID, ownerID uuid.UUID) (*entity.UserCalendar, error) {
	return &entity.UserCalendar{ID: uuid.New(), WorkspaceID: workspaceID, OwnerID: ownerID, IsDefault: true}, nil
}

func (r *fakeCalendarRepo) ListEvents(context.Context, uuid.UUID, time.Time, time.Time, uuid.UUID) ([]entity.EventOccurrence, error) {
	return nil, nil
}
func (r *fakeCalendarRepo) ListUpcoming(context.Context, uuid.UUID, uuid.UUID, int) ([]entity.EventOccurrence, error) {
	return nil, nil
}
func (r *fakeCalendarRepo) ListEventsForReminderFanout(context.Context, time.Duration) ([]entity.ReminderTarget, error) {
	return r.reminders, nil
}
func (r *fakeCalendarRepo) GetEvent(_ context.Context, eventID uuid.UUID) (*entity.CalendarEvent, error) {
	eventEntity := r.events[eventID]
	if eventEntity == nil {
		return nil, cerrors.NotFound("event not found")
	}
	copy := *eventEntity
	copy.Attendees = append([]entity.EventAttendee(nil), eventEntity.Attendees...)
	copy.Reminders = append([]entity.EventReminder(nil), eventEntity.Reminders...)
	return &copy, nil
}
func (r *fakeCalendarRepo) CreateEvent(_ context.Context, eventEntity *entity.CalendarEvent) (*entity.CalendarEvent, error) {
	if r.events == nil {
		r.events = map[uuid.UUID]*entity.CalendarEvent{}
	}
	r.events[eventEntity.ID] = eventEntity
	return eventEntity, nil
}
func (r *fakeCalendarRepo) UpdateEvent(_ context.Context, eventEntity *entity.CalendarEvent) (*entity.CalendarEvent, error) {
	r.events[eventEntity.ID] = eventEntity
	return eventEntity, nil
}
func (r *fakeCalendarRepo) DeleteEvent(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (r *fakeCalendarRepo) SetEventCallIDIfUnset(_ context.Context, eventID, callID uuid.UUID) (*entity.CalendarEvent, error) {
	eventEntity := r.events[eventID]
	if eventEntity == nil {
		return nil, cerrors.NotFound("event not found")
	}
	if eventEntity.CallID == nil {
		eventEntity.CallID = &callID
	}
	return eventEntity, nil
}
func (r *fakeCalendarRepo) UpsertRsvp(_ context.Context, eventID, userID uuid.UUID, status entity.RsvpStatus) (*entity.EventAttendee, error) {
	now := time.Now().UTC()
	attendee := &entity.EventAttendee{
		ID:          uuid.New(),
		EventID:     eventID,
		UserID:      &userID,
		IsRequired:  true,
		RsvpStatus:  status,
		RespondedAt: &now,
	}
	return attendee, nil
}

type fakeMembers struct {
	members map[[2]uuid.UUID]bool
}

func (m fakeMembers) Create(context.Context, *entity.Workspace) error { return nil }
func (m fakeMembers) GetByID(context.Context, uuid.UUID) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}
func (m fakeMembers) GetBySlug(context.Context, string) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}
func (m fakeMembers) ListByUser(context.Context, uuid.UUID) ([]entity.Workspace, error) {
	return nil, nil
}
func (m fakeMembers) Update(context.Context, *entity.Workspace) error          { return nil }
func (m fakeMembers) AddMember(context.Context, *entity.WorkspaceMember) error { return nil }
func (m fakeMembers) GetMember(_ context.Context, workspaceID, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	if m.members == nil || m.members[[2]uuid.UUID{workspaceID, userID}] {
		return &entity.WorkspaceMember{ID: uuid.New(), WorkspaceID: workspaceID, UserID: userID}, nil
	}
	return nil, cerrors.NotFound("workspace member not found")
}
func (m fakeMembers) ListMembers(context.Context, uuid.UUID, pagination.Params) ([]entity.WorkspaceMember, error) {
	return nil, nil
}
func (m fakeMembers) UpdateMemberRole(context.Context, uuid.UUID, uuid.UUID, entity.WorkspaceRole) error {
	return nil
}
func (m fakeMembers) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type fakeCallService struct {
	starts int
	calls  map[uuid.UUID]*entity.Call
}

func (s *fakeCallService) StartCall(_ context.Context, workspaceID, userID uuid.UUID, callType entity.CallType, title string, channelID *uuid.UUID, settings entity.CallSettings) (*entity.Call, error) {
	s.starts++
	call := &entity.Call{
		ID:          id.New(),
		WorkspaceID: workspaceID,
		ChannelID:   channelID,
		Type:        callType,
		Title:       title,
		CreatedBy:   userID,
		Settings:    settings,
		CreatedAt:   time.Now().UTC(),
	}
	s.calls[call.ID] = call
	return call, nil
}

func (s *fakeCallService) GetCall(_ context.Context, _ uuid.UUID, callID, _ uuid.UUID) (*entity.Call, error) {
	call := s.calls[callID]
	if call == nil {
		return nil, cerrors.NotFound("call not found")
	}
	return call, nil
}

type noopPublisher struct{}

func (noopPublisher) Publish(context.Context, string, []byte) error { return nil }

type capturingPublisher struct {
	subjects []string
	events   []event.Event
}

func (p *capturingPublisher) Publish(_ context.Context, subject string, data []byte) error {
	p.subjects = append(p.subjects, subject)
	var evt event.Event
	if err := json.Unmarshal(data, &evt); err == nil {
		p.events = append(p.events, evt)
	}
	return nil
}
