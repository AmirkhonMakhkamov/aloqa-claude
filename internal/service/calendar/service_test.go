package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/pkg/pagination"
	calrrule "aloqa/internal/pkg/rrule"
	"aloqa/internal/platform/txscope"
	searchsvc "aloqa/internal/service/search"
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

func TestStartCallFromEventRollsBackWhenLinkFails(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	eventID := uuid.New()
	txManager := newCalendarCallTxManager(&entity.CalendarEvent{
		ID:          eventID,
		WorkspaceID: workspaceID,
		OrganizerID: organizerID,
		Title:       "Planning",
	})
	txManager.linkErr = errors.New("link failed")
	svc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{members: map[[2]uuid.UUID]bool{{workspaceID, organizerID}: true}}, nil, noopPublisher{})
	svc.SetTransactionManager(txManager)

	if _, err := svc.StartCallFromEvent(ctx, workspaceID, eventID, organizerID); err == nil {
		t.Fatalf("StartCallFromEvent error = nil, want link failure")
	}
	if len(txManager.calls.calls) != 0 {
		t.Fatalf("calls persisted after rollback = %d, want 0", len(txManager.calls.calls))
	}
	if got := txManager.calendars.events[eventID].CallID; got != nil {
		t.Fatalf("event call_id after rollback = %s, want nil", *got)
	}
}

func TestStartCallFromEventConcurrentRequestsReturnOneActiveCall(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	eventID := uuid.New()
	txManager := newCalendarCallTxManager(&entity.CalendarEvent{
		ID:          eventID,
		WorkspaceID: workspaceID,
		OrganizerID: organizerID,
		Title:       "Planning",
	})
	svc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{members: map[[2]uuid.UUID]bool{{workspaceID, organizerID}: true}}, nil, noopPublisher{})
	svc.SetTransactionManager(txManager)

	const workers = 8
	var wg sync.WaitGroup
	results := make([]uuid.UUID, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			call, err := svc.StartCallFromEvent(ctx, workspaceID, eventID, organizerID)
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = call.ID
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d StartCallFromEvent error = %v", i, err)
		}
	}
	for i := 1; i < workers; i++ {
		if results[i] != results[0] {
			t.Fatalf("worker %d call ID = %s, want %s", i, results[i], results[0])
		}
	}
	if len(txManager.calls.calls) != 1 {
		t.Fatalf("calls persisted = %d, want 1", len(txManager.calls.calls))
	}
	call := txManager.calls.calls[results[0]]
	if call == nil || call.ScheduledCallID == nil || *call.ScheduledCallID != eventID {
		t.Fatalf("scheduled_call_id was not linked to event")
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
		t.Fatalf("published events = %d, want organizer + attendee user events", len(pub.events))
	}
	if pub.events[0].Type != event.TypeCalendarAttendeeRsvpUpdated {
		t.Fatalf("event type = %s", pub.events[0].Type)
	}
	if pub.subjects[1] != userEventsSubject(userID) {
		t.Fatalf("user subject = %q, want %q", pub.subjects[1], userEventsSubject(userID))
	}
}

func TestListAndDispatchRemindersEnqueuesOutboxAndMarksDispatchedAtomically(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	reminderID := uuid.New()
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	target := reminderTarget(workspaceID, userID, eventID, reminderID, now, now.Add(10*time.Minute))
	txManager := newCalendarReminderTxManager(&fakeCalendarRepo{reminders: []entity.ReminderTarget{target}})
	pub := &capturingPublisher{}
	svc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, nil, pub)
	svc.SetTransactionManager(txManager)

	if err := svc.ListAndDispatchReminders(ctx, now); err != nil {
		t.Fatalf("first dispatch error = %v", err)
	}
	if len(pub.events) != 0 {
		t.Fatalf("published during dispatch tx = %d, want 0", len(pub.events))
	}
	if len(txManager.calendars.outbox) != 1 {
		t.Fatalf("outbox rows = %d, want 1", len(txManager.calendars.outbox))
	}
	key := reminderDispatchKey{reminderID: reminderID, occurrenceAt: target.Occurrence.InstanceAt.UTC()}
	if _, ok := txManager.calendars.dispatched[key]; !ok {
		t.Fatalf("reminder occurrence was not marked dispatched")
	}

	restarted := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, nil, pub)
	restarted.SetTransactionManager(txManager)
	if err := restarted.ListAndDispatchReminders(ctx, now.Add(time.Second)); err != nil {
		t.Fatalf("dispatch after restart error = %v", err)
	}
	if len(txManager.calendars.outbox) != 1 {
		t.Fatalf("outbox rows after restart = %d, want 1", len(txManager.calendars.outbox))
	}

	publisher := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, nil, pub)
	processed, failed, dead, err := publisher.PublishPendingReminderOutbox(ctx)
	if err != nil {
		t.Fatalf("publish outbox error = %v", err)
	}
	if processed != 1 || failed != 0 || dead != 0 {
		t.Fatalf("outbox result = processed %d failed %d dead %d, want 1/0/0", processed, failed, dead)
	}
	if len(pub.events) != 1 {
		t.Fatalf("published reminder events = %d, want 1", len(pub.events))
	}
	if pub.events[0].Type != event.TypeCalendarEventReminderFired {
		t.Fatalf("event type = %s", pub.events[0].Type)
	}
	if len(txManager.calendars.publishedOutbox) != 1 {
		t.Fatalf("published outbox rows = %d, want 1", len(txManager.calendars.publishedOutbox))
	}
}

func TestListAndDispatchRemindersRollsBackWhenOutboxInsertFails(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	reminderID := uuid.New()
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	repo := &fakeCalendarRepo{
		reminders:  []entity.ReminderTarget{reminderTarget(workspaceID, userID, eventID, reminderID, now, now.Add(10*time.Minute))},
		enqueueErr: errors.New("outbox insert failed"),
	}
	txManager := newCalendarReminderTxManager(repo)
	pub := &capturingPublisher{}
	svc := NewService(repo, fakeMembers{}, nil, pub)
	svc.SetTransactionManager(txManager)

	if err := svc.ListAndDispatchReminders(ctx, now); err == nil {
		t.Fatalf("dispatch error = nil, want outbox insert failure")
	}
	if len(pub.events) != 0 {
		t.Fatalf("published events = %d, want 0", len(pub.events))
	}
	if len(txManager.calendars.outbox) != 0 {
		t.Fatalf("outbox rows after rollback = %d, want 0", len(txManager.calendars.outbox))
	}
	if len(txManager.calendars.dispatched) != 0 {
		t.Fatalf("dispatched rows after rollback = %d, want 0", len(txManager.calendars.dispatched))
	}
}

func TestListAndDispatchRemindersRecurringEventFiresEachOccurrence(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	reminderID := uuid.New()
	firstFire := time.Date(2026, 5, 13, 9, 50, 0, 0, time.UTC)
	eventEntity := entity.CalendarEvent{
		ID:              eventID,
		WorkspaceID:     workspaceID,
		OrganizerID:     userID,
		Title:           "Daily standup",
		ScheduledAt:     firstFire.Add(10 * time.Minute),
		DurationMinutes: 30,
		Recurrence:      &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=3"},
	}
	targets := make([]entity.ReminderTarget, 0, 3)
	for i := 0; i < 3; i++ {
		occurrenceAt := eventEntity.ScheduledAt.AddDate(0, 0, i)
		targets = append(targets, entity.ReminderTarget{
			ReminderID:    reminderID,
			UserID:        userID,
			OffsetMinutes: 10,
			Channel:       entity.ReminderChannelInApp,
			FireAt:        occurrenceAt.Add(-10 * time.Minute),
			Occurrence: entity.EventOccurrence{
				CalendarEvent:       eventEntity,
				InstanceAt:          occurrenceAt,
				IsRecurringInstance: true,
			},
		})
	}
	txManager := newCalendarReminderTxManager(&fakeCalendarRepo{reminders: targets})
	svc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, nil, noopPublisher{})
	svc.SetTransactionManager(txManager)

	for i := 0; i < 3; i++ {
		if err := svc.ListAndDispatchReminders(ctx, firstFire.AddDate(0, 0, i)); err != nil {
			t.Fatalf("dispatch day %d error = %v", i+1, err)
		}
		if got := len(txManager.calendars.outbox); got != i+1 {
			t.Fatalf("outbox rows after day %d = %d, want %d", i+1, got, i+1)
		}
	}
	if len(txManager.calendars.dispatched) != 3 {
		t.Fatalf("dispatched occurrences = %d, want 3", len(txManager.calendars.dispatched))
	}
}

func TestListAndDispatchRemindersSkipsPastOccurrencesOutsideHorizon(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	reminderID := uuid.New()
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	target := reminderTarget(workspaceID, userID, eventID, reminderID, now.Add(-48*time.Hour), now.Add(-48*time.Hour))
	txManager := newCalendarReminderTxManager(&fakeCalendarRepo{reminders: []entity.ReminderTarget{target}})
	svc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, nil, noopPublisher{})
	svc.SetTransactionManager(txManager)

	if err := svc.ListAndDispatchReminders(ctx, now); err != nil {
		t.Fatalf("dispatch error = %v", err)
	}
	if len(txManager.calendars.outbox) != 0 {
		t.Fatalf("outbox rows = %d, want 0", len(txManager.calendars.outbox))
	}
	if len(txManager.calendars.dispatched) != 0 {
		t.Fatalf("dispatched rows = %d, want 0", len(txManager.calendars.dispatched))
	}
}

func TestListAndDispatchRemindersConcurrentWorkersDoNotDoubleDiscover(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	reminderID := uuid.New()
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	txManager := newCalendarReminderTxManager(&fakeCalendarRepo{
		reminders: []entity.ReminderTarget{reminderTarget(workspaceID, userID, eventID, reminderID, now, now.Add(10*time.Minute))},
	})
	svcA := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, nil, noopPublisher{})
	svcA.SetTransactionManager(txManager)
	svcB := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, nil, noopPublisher{})
	svcB.SetTransactionManager(txManager)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, svc := range []*Service{svcA, svcB} {
		wg.Add(1)
		go func(i int, svc *Service) {
			defer wg.Done()
			errs[i] = svc.ListAndDispatchReminders(ctx, now)
		}(i, svc)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d dispatch error = %v", i, err)
		}
	}
	if len(txManager.calendars.outbox) != 1 {
		t.Fatalf("outbox rows = %d, want 1", len(txManager.calendars.outbox))
	}
	if len(txManager.calendars.dispatched) != 1 {
		t.Fatalf("dispatched rows = %d, want 1", len(txManager.calendars.dispatched))
	}
}

func TestListAndDispatchRemindersDiscoversLongOffsetEventWithinLookahead(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	reminderID := uuid.New()
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	target := reminderTarget(workspaceID, userID, eventID, reminderID, now.Add(-48*time.Hour), now.Add(5*24*time.Hour))
	txManager := newCalendarReminderTxManager(&fakeCalendarRepo{reminders: []entity.ReminderTarget{target}})
	svc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, nil, noopPublisher{})
	svc.SetTransactionManager(txManager)

	if err := svc.ListAndDispatchReminders(ctx, now); err != nil {
		t.Fatalf("dispatch error = %v", err)
	}
	if txManager.calendars.lastReminderHorizon != reminderEventLookaheadHorizon {
		t.Fatalf("horizon = %s, want %s", txManager.calendars.lastReminderHorizon, reminderEventLookaheadHorizon)
	}
	if len(txManager.calendars.outbox) != 1 {
		t.Fatalf("outbox rows = %d, want 1", len(txManager.calendars.outbox))
	}
	if err := svc.ListAndDispatchReminders(ctx, now.Add(time.Second)); err != nil {
		t.Fatalf("second dispatch error = %v", err)
	}
	if len(txManager.calendars.outbox) != 1 {
		t.Fatalf("outbox rows after second run = %d, want 1", len(txManager.calendars.outbox))
	}
}

func TestListAndDispatchRemindersWaitsForLongOffsetFireTime(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	reminderID := uuid.New()
	start := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	occurrenceAt := start.Add(14 * 24 * time.Hour)
	fireAt := occurrenceAt.Add(-time.Duration(maxReminderOffsetMinutes) * time.Minute)
	target := reminderTarget(workspaceID, userID, eventID, reminderID, fireAt, occurrenceAt)
	txManager := newCalendarReminderTxManager(&fakeCalendarRepo{reminders: []entity.ReminderTarget{target}})
	svc := NewService(txManager.calendars.fakeCalendarRepo, fakeMembers{}, nil, noopPublisher{})
	svc.SetTransactionManager(txManager)

	if err := svc.ListAndDispatchReminders(ctx, start); err != nil {
		t.Fatalf("early dispatch error = %v", err)
	}
	if len(txManager.calendars.outbox) != 0 {
		t.Fatalf("early outbox rows = %d, want 0", len(txManager.calendars.outbox))
	}
	if err := svc.ListAndDispatchReminders(ctx, fireAt); err != nil {
		t.Fatalf("dispatch at fire time error = %v", err)
	}
	if len(txManager.calendars.outbox) != 1 {
		t.Fatalf("outbox rows at fire time = %d, want 1", len(txManager.calendars.outbox))
	}
}

func TestPublishPendingReminderOutboxConcurrentPublishersDoNotDoublePublish(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	eventID := uuid.New()
	reminderID := uuid.New()
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	target := reminderTarget(workspaceID, userID, eventID, reminderID, now, now.Add(10*time.Minute))
	payload, err := prepareReminderPayload(target, now)
	if err != nil {
		t.Fatalf("prepare reminder payload error = %v", err)
	}
	repo := &fakeCalendarRepo{outbox: []entity.ReminderOutboxMessage{{
		ID:           uuid.New(),
		ReminderID:   reminderID,
		EventID:      eventID,
		OccurrenceAt: target.Occurrence.InstanceAt,
		UserID:       userID,
		PayloadJSON:  payload,
		EnqueuedAt:   now,
	}}}
	pub := &capturingPublisher{}
	svcA := NewService(repo, fakeMembers{}, nil, pub)
	svcB := NewService(repo, fakeMembers{}, nil, pub)

	var wg sync.WaitGroup
	results := make([]int, 2)
	errs := make([]error, 2)
	for i, svc := range []*Service{svcA, svcB} {
		wg.Add(1)
		go func(i int, svc *Service) {
			defer wg.Done()
			processed, _, _, err := svc.PublishPendingReminderOutbox(ctx)
			results[i] = processed
			errs[i] = err
		}(i, svc)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("publisher %d error = %v", i, err)
		}
	}
	if results[0]+results[1] != 1 {
		t.Fatalf("processed rows = %d + %d, want 1 total", results[0], results[1])
	}
	if len(pub.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(pub.events))
	}
	if len(repo.publishedOutbox) != 1 {
		t.Fatalf("published outbox rows = %d, want 1", len(repo.publishedOutbox))
	}
}

func TestListEventsKeepsOriginatorTimezoneAcrossBerlinDST(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	organizerID := uuid.New()
	eventID := uuid.New()
	calendarID := uuid.New()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	startLocal := time.Date(2026, 3, 28, 9, 30, 0, 0, loc)
	repo := &expandingCalendarRepo{fakeCalendarRepo: fakeCalendarRepo{
		events: map[uuid.UUID]*entity.CalendarEvent{
			eventID: {
				ID:              eventID,
				CalendarID:      calendarID,
				WorkspaceID:     workspaceID,
				OrganizerID:     organizerID,
				Title:           "Berlin standup",
				Location:        entity.EventLocation{Type: entity.EventLocationAloqaMeet},
				ScheduledAt:     startLocal.UTC(),
				OriginatorTZ:    "Europe/Berlin",
				DurationMinutes: 30,
				Recurrence:      &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=4"},
			},
		},
	}}
	svc := NewService(repo, fakeMembers{members: map[[2]uuid.UUID]bool{{workspaceID, organizerID}: true}}, nil, noopPublisher{})

	occurrences, err := svc.ListEvents(ctx, workspaceID, startLocal.Add(-time.Hour).UTC(), startLocal.AddDate(0, 0, 5).UTC(), organizerID)
	if err != nil {
		t.Fatalf("ListEvents error = %v", err)
	}
	if len(occurrences) != 4 {
		t.Fatalf("occurrences = %v, want 4", occurrences)
	}
	for _, occurrence := range occurrences {
		local := occurrence.InstanceAt.In(loc)
		if local.Hour() != 9 || local.Minute() != 30 {
			t.Fatalf("occurrence shifted from 09:30 Europe/Berlin: %v", occurrences)
		}
	}
}

type fakeCalendarRepo struct {
	mu                  sync.Mutex
	calendars           []entity.UserCalendar
	events              map[uuid.UUID]*entity.CalendarEvent
	reminders           []entity.ReminderTarget
	dispatched          map[reminderDispatchKey]time.Time
	outbox              []entity.ReminderOutboxMessage
	publishedOutbox     map[uuid.UUID]time.Time
	enqueueErr          error
	lastReminderHorizon time.Duration
	lastReminderLimit   int
}

type reminderDispatchKey struct {
	reminderID   uuid.UUID
	occurrenceAt time.Time
}

func reminderTarget(workspaceID, userID, eventID, reminderID uuid.UUID, fireAt, occurrenceAt time.Time) entity.ReminderTarget {
	return entity.ReminderTarget{
		ReminderID:    reminderID,
		UserID:        userID,
		OffsetMinutes: int(occurrenceAt.Sub(fireAt) / time.Minute),
		Channel:       entity.ReminderChannelInApp,
		FireAt:        fireAt.UTC(),
		Occurrence: entity.EventOccurrence{
			CalendarEvent: entity.CalendarEvent{
				ID:          eventID,
				WorkspaceID: workspaceID,
				OrganizerID: userID,
				Title:       "Standup",
				ScheduledAt: occurrenceAt.UTC(),
			},
			InstanceAt: occurrenceAt.UTC(),
		},
	}
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
func (r *fakeCalendarRepo) ListDueReminderTargets(_ context.Context, now time.Time, horizon time.Duration, limit int) ([]entity.ReminderTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastReminderHorizon = horizon
	r.lastReminderLimit = limit
	if limit <= 0 {
		limit = len(r.reminders)
	}
	windowStart := now.UTC().Add(-30 * time.Second)
	windowEnd := now.UTC().Add(horizon)
	var targets []entity.ReminderTarget
	for _, reminder := range r.reminders {
		if len(targets) >= limit {
			break
		}
		if reminder.FireAt.After(now.UTC()) {
			continue
		}
		if reminder.Occurrence.InstanceAt.Before(windowStart) || reminder.Occurrence.InstanceAt.After(windowEnd) {
			continue
		}
		if r.dispatched != nil {
			key := reminderDispatchKey{reminderID: reminder.ReminderID, occurrenceAt: reminder.Occurrence.InstanceAt.UTC()}
			if _, ok := r.dispatched[key]; ok {
				continue
			}
		}
		targets = append(targets, reminder)
	}
	return targets, nil
}
func (r *fakeCalendarRepo) EnqueueReminderOutbox(_ context.Context, target entity.ReminderTarget, payloadJSON []byte, enqueuedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.enqueueErr != nil {
		return r.enqueueErr
	}
	r.outbox = append(r.outbox, entity.ReminderOutboxMessage{
		ID:           uuid.New(),
		ReminderID:   target.ReminderID,
		EventID:      target.Occurrence.ID,
		OccurrenceAt: target.Occurrence.InstanceAt.UTC(),
		UserID:       target.UserID,
		PayloadJSON:  append([]byte(nil), payloadJSON...),
		EnqueuedAt:   enqueuedAt.UTC(),
	})
	return nil
}
func (r *fakeCalendarRepo) MarkReminderDispatched(_ context.Context, reminderID uuid.UUID, occurrenceAt, dispatchedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dispatched == nil {
		r.dispatched = map[reminderDispatchKey]time.Time{}
	}
	r.dispatched[reminderDispatchKey{reminderID: reminderID, occurrenceAt: occurrenceAt.UTC()}] = dispatchedAt
	return nil
}

func (r *fakeCalendarRepo) PublishReminderOutbox(ctx context.Context, limit, maxAttempts int, publish func(context.Context, entity.ReminderOutboxMessage) error) (processed, failed, dead int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 {
		limit = len(r.outbox)
	}
	if maxAttempts <= 0 {
		maxAttempts = 10
	}
	if r.publishedOutbox == nil {
		r.publishedOutbox = map[uuid.UUID]time.Time{}
	}
	for i := range r.outbox {
		if processed+failed+dead >= limit {
			break
		}
		msg := r.outbox[i]
		if _, ok := r.publishedOutbox[msg.ID]; ok {
			continue
		}
		if msg.Attempts >= maxAttempts {
			continue
		}
		if publish != nil {
			if err := publish(ctx, msg); err != nil {
				r.outbox[i].Attempts++
				if r.outbox[i].Attempts >= maxAttempts {
					dead++
				} else {
					failed++
				}
				continue
			}
		}
		r.outbox[i].Attempts++
		r.publishedOutbox[msg.ID] = time.Now().UTC()
		processed++
	}
	return processed, failed, dead, nil
}

type expandingCalendarRepo struct {
	fakeCalendarRepo
}

func (r *expandingCalendarRepo) ListEvents(_ context.Context, workspaceID uuid.UUID, fromTS, toTS time.Time, viewerID uuid.UUID) ([]entity.EventOccurrence, error) {
	var occurrences []entity.EventOccurrence
	for _, eventEntity := range r.events {
		if eventEntity.WorkspaceID != workspaceID || !canAccessEvent(eventEntity, viewerID) {
			continue
		}
		if eventEntity.Recurrence == nil {
			occurrences = append(occurrences, entity.EventOccurrence{CalendarEvent: *eventEntity, InstanceAt: eventEntity.ScheduledAt})
			continue
		}
		loc, err := time.LoadLocation(eventEntity.OriginatorTZ)
		if err != nil {
			loc = time.UTC
		}
		expanded, err := calrrule.Expand(eventEntity.Recurrence.RRule, eventEntity.ScheduledAt.In(loc), eventEntity.Recurrence.Exdates, fromTS.In(loc), toTS.In(loc))
		if err != nil {
			return nil, err
		}
		for _, instanceAt := range expanded {
			occurrences = append(occurrences, entity.EventOccurrence{
				CalendarEvent:       *eventEntity,
				InstanceAt:          instanceAt.UTC(),
				IsRecurringInstance: true,
			})
		}
	}
	return occurrences, nil
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
func (r *fakeCalendarRepo) LockEventForStartCall(ctx context.Context, eventID uuid.UUID) (*entity.CalendarEvent, error) {
	return r.GetEvent(ctx, eventID)
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
func (r *fakeCalendarRepo) LinkEventAndCall(_ context.Context, eventID, callID uuid.UUID) error {
	eventEntity := r.events[eventID]
	if eventEntity == nil {
		return cerrors.NotFound("event not found")
	}
	eventEntity.CallID = &callID
	return nil
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
		Status:      entity.CallStatusRinging,
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

type calendarCallTxManager struct {
	mu        sync.Mutex
	calendars *txCalendarRepo
	calls     *txCallRepo
	events    []event.Event
	linkErr   error
}

func newCalendarCallTxManager(eventEntity *entity.CalendarEvent) *calendarCallTxManager {
	repo := &fakeCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{}}
	copy := *eventEntity
	repo.events[eventEntity.ID] = &copy
	return newCalendarReminderTxManager(repo)
}

func newCalendarReminderTxManager(repo *fakeCalendarRepo) *calendarCallTxManager {
	if repo.events == nil {
		repo.events = map[uuid.UUID]*entity.CalendarEvent{}
	}
	return &calendarCallTxManager{
		calendars: &txCalendarRepo{fakeCalendarRepo: repo},
		calls:     &txCallRepo{calls: map[uuid.UUID]*entity.Call{}, participants: map[uuid.UUID][]entity.CallParticipant{}},
	}
}

func (m *calendarCallTxManager) WithinTx(ctx context.Context, fn func(context.Context, txscope.Scope) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	calendars := m.calendars.clone()
	calls := m.calls.clone()
	calendars.calls = calls
	calendars.linkErr = m.linkErr
	scope := &fakeCalendarTxScope{calendars: calendars, calls: calls}
	if err := fn(ctx, scope); err != nil {
		return err
	}
	m.calendars = calendars
	m.calls = calls
	m.events = append(m.events, scope.events...)
	return nil
}

type txCalendarRepo struct {
	*fakeCalendarRepo
	calls   *txCallRepo
	linkErr error
}

func (r *txCalendarRepo) clone() *txCalendarRepo {
	events := map[uuid.UUID]*entity.CalendarEvent{}
	for eventID, eventEntity := range r.events {
		copy := *eventEntity
		copy.Attendees = append([]entity.EventAttendee(nil), eventEntity.Attendees...)
		copy.Reminders = append([]entity.EventReminder(nil), eventEntity.Reminders...)
		events[eventID] = &copy
	}
	return &txCalendarRepo{fakeCalendarRepo: &fakeCalendarRepo{
		calendars:           append([]entity.UserCalendar(nil), r.calendars...),
		events:              events,
		reminders:           append([]entity.ReminderTarget(nil), r.reminders...),
		dispatched:          cloneReminderDispatchMap(r.dispatched),
		outbox:              cloneReminderOutbox(r.outbox),
		publishedOutbox:     cloneUUIDTimeMap(r.publishedOutbox),
		enqueueErr:          r.enqueueErr,
		lastReminderHorizon: r.lastReminderHorizon,
		lastReminderLimit:   r.lastReminderLimit,
	}}
}

func (r *txCalendarRepo) LinkEventAndCall(_ context.Context, eventID, callID uuid.UUID) error {
	if r.linkErr != nil {
		return r.linkErr
	}
	eventEntity := r.events[eventID]
	if eventEntity == nil {
		return cerrors.NotFound("event not found")
	}
	callEntity := r.calls.calls[callID]
	if callEntity == nil {
		return cerrors.NotFound("call not found")
	}
	eventEntity.CallID = &callID
	scheduledCallID := eventID
	callEntity.ScheduledCallID = &scheduledCallID
	return nil
}

type txCallRepo struct {
	calls        map[uuid.UUID]*entity.Call
	participants map[uuid.UUID][]entity.CallParticipant
}

func (r *txCallRepo) clone() *txCallRepo {
	calls := map[uuid.UUID]*entity.Call{}
	for callID, callEntity := range r.calls {
		copy := *callEntity
		calls[callID] = &copy
	}
	participants := map[uuid.UUID][]entity.CallParticipant{}
	for callID, existing := range r.participants {
		participants[callID] = append([]entity.CallParticipant(nil), existing...)
	}
	return &txCallRepo{calls: calls, participants: participants}
}

func (r *txCallRepo) Create(_ context.Context, call *entity.Call) error {
	if r.calls == nil {
		r.calls = map[uuid.UUID]*entity.Call{}
	}
	copy := *call
	r.calls[call.ID] = &copy
	return nil
}

func (r *txCallRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Call, error) {
	call := r.calls[id]
	if call == nil {
		return nil, cerrors.NotFound("call not found")
	}
	copy := *call
	return &copy, nil
}

func (r *txCallRepo) ListActiveByWorkspace(_ context.Context, workspaceID uuid.UUID) ([]entity.Call, error) {
	var calls []entity.Call
	for _, call := range r.calls {
		if call.WorkspaceID == workspaceID && call.Status != entity.CallStatusEnded {
			calls = append(calls, *call)
		}
	}
	return calls, nil
}

func (r *txCallRepo) UpdateStatus(_ context.Context, id uuid.UUID, status entity.CallStatus) error {
	call := r.calls[id]
	if call == nil {
		return cerrors.NotFound("call not found")
	}
	call.Status = status
	return nil
}

func (r *txCallRepo) End(_ context.Context, id uuid.UUID) error {
	call := r.calls[id]
	if call == nil {
		return cerrors.NotFound("call not found")
	}
	now := time.Now().UTC()
	call.Status = entity.CallStatusEnded
	call.EndedAt = &now
	return nil
}

func (r *txCallRepo) AddParticipant(_ context.Context, p *entity.CallParticipant) error {
	if r.participants == nil {
		r.participants = map[uuid.UUID][]entity.CallParticipant{}
	}
	r.participants[p.CallID] = append(r.participants[p.CallID], *p)
	return nil
}

func (r *txCallRepo) AddParticipantIfCapacity(ctx context.Context, p *entity.CallParticipant, _ int) error {
	return r.AddParticipant(ctx, p)
}

func (r *txCallRepo) GetParticipant(context.Context, uuid.UUID, uuid.UUID) (*entity.CallParticipant, error) {
	return nil, cerrors.NotFound("participant not found")
}
func (r *txCallRepo) ListParticipants(context.Context, uuid.UUID) ([]entity.CallParticipant, error) {
	return nil, nil
}
func (r *txCallRepo) UpdateParticipantStatus(context.Context, uuid.UUID, entity.ParticipantStatus) error {
	return nil
}
func (r *txCallRepo) UpdateParticipantRole(context.Context, uuid.UUID, entity.CallRole) error {
	return nil
}
func (r *txCallRepo) UpdateParticipantMedia(context.Context, uuid.UUID, bool, bool, bool) error {
	return nil
}
func (r *txCallRepo) RemoveParticipant(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type fakeCalendarTxScope struct {
	calendars repository.CalendarRepository
	calls     repository.CallRepository
	events    []event.Event
}

func (s *fakeCalendarTxScope) Users() repository.UserRepository                       { return nil }
func (s *fakeCalendarTxScope) Workspaces() repository.WorkspaceRepository             { return nil }
func (s *fakeCalendarTxScope) Messages() repository.MessageRepository                 { return nil }
func (s *fakeCalendarTxScope) Channels() repository.ChannelRepository                 { return nil }
func (s *fakeCalendarTxScope) ChannelGrants() repository.ChannelAccessGrantRepository { return nil }
func (s *fakeCalendarTxScope) Calls() repository.CallRepository                       { return s.calls }
func (s *fakeCalendarTxScope) Calendars() repository.CalendarRepository               { return s.calendars }
func (s *fakeCalendarTxScope) Recordings() repository.RecordingRepository             { return nil }
func (s *fakeCalendarTxScope) Invites() repository.GuestInviteRepository              { return nil }
func (s *fakeCalendarTxScope) GuestGrants() repository.GuestAccessRepository          { return nil }
func (s *fakeCalendarTxScope) Roles() repository.WorkspaceRoleRepository              { return nil }
func (s *fakeCalendarTxScope) Audit() repository.AuditRepository                      { return nil }
func (s *fakeCalendarTxScope) SearchIndexer() searchsvc.Indexer                       { return nil }
func (s *fakeCalendarTxScope) EnqueueRealtime(_ context.Context, evt event.Event, _ []byte) error {
	s.events = append(s.events, evt)
	return nil
}

func cloneReminderDispatchMap(in map[reminderDispatchKey]time.Time) map[reminderDispatchKey]time.Time {
	if in == nil {
		return nil
	}
	out := map[reminderDispatchKey]time.Time{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneUUIDTimeMap(in map[uuid.UUID]time.Time) map[uuid.UUID]time.Time {
	if in == nil {
		return nil
	}
	out := map[uuid.UUID]time.Time{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneReminderOutbox(in []entity.ReminderOutboxMessage) []entity.ReminderOutboxMessage {
	out := make([]entity.ReminderOutboxMessage, len(in))
	for i, msg := range in {
		out[i] = msg
		out[i].PayloadJSON = append([]byte(nil), msg.PayloadJSON...)
	}
	return out
}

type noopPublisher struct{}

func (noopPublisher) Publish(context.Context, string, []byte) error { return nil }

type capturingPublisher struct {
	mu       sync.Mutex
	subjects []string
	events   []event.Event
}

func (p *capturingPublisher) Publish(_ context.Context, subject string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subjects = append(p.subjects, subject)
	var evt event.Event
	if err := json.Unmarshal(data, &evt); err == nil {
		p.events = append(p.events, evt)
	}
	return nil
}
