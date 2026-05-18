package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/domain/repository"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
	"aloqa/internal/platform/txscope"
	calendarservice "aloqa/internal/service/calendar"
	searchsvc "aloqa/internal/service/search"
)

type calendarHTTPFixture struct {
	wsID   uuid.UUID
	userID uuid.UUID
	repo   *fakeMoveCalendarRepo
	router *chi.Mux
}

func newCalendarHTTPFixture() calendarHTTPFixture {
	wsID := uuid.New()
	userID := uuid.New()
	repo := &fakeMoveCalendarRepo{events: map[uuid.UUID]*entity.CalendarEvent{}}
	svc := calendarservice.NewService(repo, fakeCalHTTPMembers{wsID: wsID, userID: userID}, nil, noopCalHTTPPublisher{})
	svc.SetTransactionManager(&fakeMoveCalHTTPTxManager{repo: repo})
	handler := NewCalendarHandler(svc)
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), middleware.WorkspaceIDKey, wsID)
			ctx = context.WithValue(ctx, middleware.UserIDKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	router.Post("/events/{eventID}/occurrences/move", handler.MoveOccurrence)
	return calendarHTTPFixture{wsID: wsID, userID: userID, repo: repo, router: router}
}

func (f calendarHTTPFixture) serve(eventID uuid.UUID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/events/"+eventID.String()+"/occurrences/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	f.router.ServeHTTP(res, req)
	return res
}

func decodeErrBody(t *testing.T, res *httptest.ResponseRecorder) (code, message string) {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (raw=%s)", err, res.Body.String())
	}
	return body.Error.Code, body.Error.Message
}

func TestMoveOccurrenceHandler_200OK(t *testing.T) {
	f := newCalendarHTTPFixture()
	eventID := uuid.New()
	now := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	f.repo.events[eventID] = &entity.CalendarEvent{
		ID: eventID, WorkspaceID: f.wsID, OrganizerID: f.userID,
		Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
		ScheduledAt: now, DurationMinutes: 30,
	}
	body := `{"instance_at":"2026-05-03T10:00:00Z","scope":"all","new_scheduled_at":"2026-05-05T10:00:00Z"}`
	res := f.serve(eventID, body)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var resp moveOccurrenceResponse
	if err := json.Unmarshal(res.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Updated == nil {
		t.Fatal("updated must not be nil")
	}
}

func TestMoveOccurrenceHandler_400InvalidScope(t *testing.T) {
	f := newCalendarHTTPFixture()
	eventID := uuid.New()
	now := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	f.repo.events[eventID] = &entity.CalendarEvent{
		ID: eventID, WorkspaceID: f.wsID, OrganizerID: f.userID,
		Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
		ScheduledAt: now, DurationMinutes: 30,
	}
	body := `{"instance_at":"2026-05-03T10:00:00Z","scope":"bad","new_scheduled_at":"2026-05-05T10:00:00Z"}`
	res := f.serve(eventID, body)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", res.Code)
	}
	_, msg := decodeErrBody(t, res)
	if !strings.Contains(msg, "INVALID_SCOPE") {
		t.Fatalf("message=%q missing INVALID_SCOPE", msg)
	}
}

func TestMoveOccurrenceHandler_400MissingInstanceAt(t *testing.T) {
	f := newCalendarHTTPFixture()
	eventID := uuid.New()
	now := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	f.repo.events[eventID] = &entity.CalendarEvent{
		ID: eventID, WorkspaceID: f.wsID, OrganizerID: f.userID,
		Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
		ScheduledAt: now, DurationMinutes: 30,
	}
	res := f.serve(eventID, `{"scope":"all","new_scheduled_at":"2026-05-05T10:00:00Z"}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	_, msg := decodeErrBody(t, res)
	if !strings.Contains(msg, "instance_at") {
		t.Fatalf("message=%q must name the missing field instance_at", msg)
	}
}

func TestMoveOccurrenceHandler_400MissingNewScheduledAt(t *testing.T) {
	f := newCalendarHTTPFixture()
	eventID := uuid.New()
	now := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	f.repo.events[eventID] = &entity.CalendarEvent{
		ID: eventID, WorkspaceID: f.wsID, OrganizerID: f.userID,
		Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
		ScheduledAt: now, DurationMinutes: 30,
	}
	res := f.serve(eventID, `{"scope":"all","instance_at":"2026-05-03T10:00:00Z"}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	_, msg := decodeErrBody(t, res)
	if !strings.Contains(msg, "new_scheduled_at") {
		t.Fatalf("message=%q must name the missing field new_scheduled_at", msg)
	}
}

func TestMoveOccurrenceHandler_400InvalidInstance(t *testing.T) {
	f := newCalendarHTTPFixture()
	eventID := uuid.New()
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	f.repo.events[eventID] = &entity.CalendarEvent{
		ID: eventID, WorkspaceID: f.wsID, OrganizerID: f.userID,
		Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
		ScheduledAt: start, DurationMinutes: 30,
		Recurrence: &entity.RecurrenceRule{RRule: "FREQ=DAILY;COUNT=3"},
	}
	body := `{"instance_at":"2026-05-10T10:00:00Z","scope":"this","new_scheduled_at":"2026-05-11T10:00:00Z"}`
	res := f.serve(eventID, body)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	_, msg := decodeErrBody(t, res)
	if !strings.Contains(msg, "INVALID_INSTANCE") {
		t.Fatalf("message=%q missing INVALID_INSTANCE", msg)
	}
}

func TestMoveOccurrenceHandler_403NonOrganizer(t *testing.T) {
	f := newCalendarHTTPFixture()
	eventID := uuid.New()
	otherOrg := uuid.New()
	now := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	f.repo.events[eventID] = &entity.CalendarEvent{
		ID: eventID, WorkspaceID: f.wsID, OrganizerID: otherOrg,
		Title: "X", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
		ScheduledAt: now, DurationMinutes: 30,
	}
	body := `{"instance_at":"2026-05-03T10:00:00Z","scope":"all","new_scheduled_at":"2026-05-05T10:00:00Z"}`
	res := f.serve(eventID, body)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status=%d", res.Code)
	}
	_, msg := decodeErrBody(t, res)
	if !strings.Contains(msg, "FORBIDDEN_NOT_ORGANIZER") {
		t.Fatalf("message=%q missing FORBIDDEN_NOT_ORGANIZER", msg)
	}
}

func TestMoveOccurrenceHandler_409ConcurrentUpdate_WithToken(t *testing.T) {
	f := newCalendarHTTPFixture()
	eventID := uuid.New()
	storedAt := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	f.repo.events[eventID] = &entity.CalendarEvent{
		ID: eventID, WorkspaceID: f.wsID, OrganizerID: f.userID,
		Title: "D", Location: entity.EventLocation{Type: entity.EventLocationAloqaMeet},
		ScheduledAt: storedAt, DurationMinutes: 30, UpdatedAt: storedAt,
	}
	staleTs := storedAt.Add(-time.Second).UTC().Format(time.RFC3339Nano)
	body := `{"instance_at":"2026-05-01T08:00:00Z","scope":"all","new_scheduled_at":"2026-05-02T10:00:00Z","expected_updated_at":"` + staleTs + `"}`
	res := f.serve(eventID, body)
	if res.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	_, msg := decodeErrBody(t, res)
	if !strings.Contains(msg, "CONCURRENT_UPDATE") {
		t.Fatalf("message=%q missing CONCURRENT_UPDATE", msg)
	}
	if !f.repo.lastUpdateTxCalled {
		t.Fatal("UpdateEventTx not invoked")
	}
	if f.repo.lastUpdateTxExpected == nil {
		t.Fatal("UpdateEventTx received nil expected_updated_at")
	}
}

type fakeMoveCalHTTPTxManager struct {
	repo *fakeMoveCalendarRepo
}

func (m *fakeMoveCalHTTPTxManager) WithinTx(ctx context.Context, fn func(context.Context, txscope.Scope) error) error {
	return fn(ctx, &fakeMoveCalHTTPScope{calendars: m.repo})
}

type fakeMoveCalHTTPScope struct {
	calendars *fakeMoveCalendarRepo
}

func (s *fakeMoveCalHTTPScope) Users() repository.UserRepository                       { return nil }
func (s *fakeMoveCalHTTPScope) Workspaces() repository.WorkspaceRepository             { return nil }
func (s *fakeMoveCalHTTPScope) Messages() repository.MessageRepository                 { return nil }
func (s *fakeMoveCalHTTPScope) Channels() repository.ChannelRepository                 { return nil }
func (s *fakeMoveCalHTTPScope) ChannelGrants() repository.ChannelAccessGrantRepository { return nil }
func (s *fakeMoveCalHTTPScope) Calls() repository.CallRepository                       { return nil }
func (s *fakeMoveCalHTTPScope) CallMessages() repository.CallMessageRepository         { return nil }
func (s *fakeMoveCalHTTPScope) Calendars() repository.CalendarRepository               { return s.calendars }
func (s *fakeMoveCalHTTPScope) Recordings() repository.RecordingRepository             { return nil }
func (s *fakeMoveCalHTTPScope) Invites() repository.GuestInviteRepository              { return nil }
func (s *fakeMoveCalHTTPScope) GuestGrants() repository.GuestAccessRepository          { return nil }
func (s *fakeMoveCalHTTPScope) Roles() repository.WorkspaceRoleRepository              { return nil }
func (s *fakeMoveCalHTTPScope) Audit() repository.AuditRepository                      { return nil }
func (s *fakeMoveCalHTTPScope) SearchIndexer() searchsvc.Indexer                       { return nil }
func (s *fakeMoveCalHTTPScope) EnqueueRealtime(context.Context, event.Event, []byte) error {
	return nil
}

type fakeMoveCalendarRepo struct {
	events               map[uuid.UUID]*entity.CalendarEvent
	lastUpdateTxCalled   bool
	lastUpdateTxExpected *time.Time
}

func (r *fakeMoveCalendarRepo) GetEvent(_ context.Context, eventID uuid.UUID) (*entity.CalendarEvent, error) {
	eventEntity := r.events[eventID]
	if eventEntity == nil {
		return nil, cerrors.NotFound("event not found")
	}
	return cloneMoveHTTPEvent(eventEntity), nil
}

func (r *fakeMoveCalendarRepo) CreateEvent(ctx context.Context, eventEntity *entity.CalendarEvent) (*entity.CalendarEvent, error) {
	if r.events == nil {
		r.events = map[uuid.UUID]*entity.CalendarEvent{}
	}
	r.events[eventEntity.ID] = cloneMoveHTTPEvent(eventEntity)
	return r.GetEvent(ctx, eventEntity.ID)
}

func (r *fakeMoveCalendarRepo) UpdateEvent(ctx context.Context, eventEntity *entity.CalendarEvent) (*entity.CalendarEvent, error) {
	if r.events == nil {
		r.events = map[uuid.UUID]*entity.CalendarEvent{}
	}
	if _, ok := r.events[eventEntity.ID]; !ok {
		return nil, cerrors.NotFound("event not found")
	}
	r.events[eventEntity.ID] = cloneMoveHTTPEvent(eventEntity)
	return r.GetEvent(ctx, eventEntity.ID)
}

func (r *fakeMoveCalendarRepo) CreateEventTx(ctx context.Context, eventEntity *entity.CalendarEvent) (*entity.CalendarEvent, error) {
	return r.CreateEvent(ctx, eventEntity)
}

func (r *fakeMoveCalendarRepo) UpdateEventTx(ctx context.Context, eventEntity *entity.CalendarEvent, expectedUpdatedAt *time.Time) (*entity.CalendarEvent, error) {
	r.lastUpdateTxCalled = true
	r.lastUpdateTxExpected = expectedUpdatedAt
	if expectedUpdatedAt != nil {
		existing, err := r.GetEvent(ctx, eventEntity.ID)
		if err != nil {
			return nil, err
		}
		if !existing.UpdatedAt.UTC().Equal(expectedUpdatedAt.UTC()) {
			return nil, cerrors.Conflict("CONCURRENT_UPDATE: event was modified concurrently")
		}
	}
	return r.UpdateEvent(ctx, eventEntity)
}

func (r *fakeMoveCalendarRepo) ListUserCalendars(context.Context, uuid.UUID, uuid.UUID) ([]entity.UserCalendar, error) {
	return nil, nil
}
func (r *fakeMoveCalendarRepo) GetUserCalendar(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*entity.UserCalendar, error) {
	return nil, cerrors.NotFound("calendar not found")
}
func (r *fakeMoveCalendarRepo) CreateUserCalendar(context.Context, *entity.UserCalendar) error {
	return nil
}
func (r *fakeMoveCalendarRepo) UpdateUserCalendar(context.Context, *entity.UserCalendar) error {
	return nil
}
func (r *fakeMoveCalendarRepo) DeleteUserCalendar(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *fakeMoveCalendarRepo) SetCalendarVisibility(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, bool) (*entity.UserCalendar, error) {
	return nil, nil
}
func (r *fakeMoveCalendarRepo) EnsureDefaultCalendar(context.Context, uuid.UUID, uuid.UUID) (*entity.UserCalendar, error) {
	return nil, nil
}
func (r *fakeMoveCalendarRepo) ListEvents(context.Context, uuid.UUID, time.Time, time.Time, uuid.UUID) ([]entity.EventOccurrence, error) {
	return nil, nil
}
func (r *fakeMoveCalendarRepo) ListUpcoming(context.Context, uuid.UUID, uuid.UUID, int) ([]entity.EventOccurrence, error) {
	return nil, nil
}
func (r *fakeMoveCalendarRepo) ListDueReminderTargets(context.Context, time.Time, time.Duration, int) ([]entity.ReminderTarget, error) {
	return nil, nil
}
func (r *fakeMoveCalendarRepo) EnqueueReminderOutbox(context.Context, entity.ReminderTarget, []byte, time.Time) error {
	return nil
}
func (r *fakeMoveCalendarRepo) MarkReminderDispatched(context.Context, uuid.UUID, time.Time, time.Time) error {
	return nil
}
func (r *fakeMoveCalendarRepo) PublishReminderOutbox(context.Context, int, int, func(context.Context, entity.ReminderOutboxMessage) error) (int, int, int, error) {
	return 0, 0, 0, nil
}
func (r *fakeMoveCalendarRepo) LockEventForStartCall(ctx context.Context, eventID uuid.UUID) (*entity.CalendarEvent, error) {
	return r.GetEvent(ctx, eventID)
}
func (r *fakeMoveCalendarRepo) DeleteEvent(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (r *fakeMoveCalendarRepo) SetEventCallIDIfUnset(context.Context, uuid.UUID, uuid.UUID) (*entity.CalendarEvent, error) {
	return nil, nil
}
func (r *fakeMoveCalendarRepo) LinkEventAndCall(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *fakeMoveCalendarRepo) UpsertRsvp(context.Context, uuid.UUID, uuid.UUID, entity.RsvpStatus) (*entity.EventAttendee, error) {
	return nil, nil
}

func cloneMoveHTTPEvent(eventEntity *entity.CalendarEvent) *entity.CalendarEvent {
	if eventEntity == nil {
		return nil
	}
	copy := *eventEntity
	copy.Attendees = append([]entity.EventAttendee(nil), eventEntity.Attendees...)
	copy.Reminders = append([]entity.EventReminder(nil), eventEntity.Reminders...)
	if eventEntity.Recurrence != nil {
		copy.Recurrence = &entity.RecurrenceRule{
			RRule:   eventEntity.Recurrence.RRule,
			Exdates: append([]time.Time(nil), eventEntity.Recurrence.Exdates...),
		}
	}
	return &copy
}

type fakeCalHTTPMembers struct {
	wsID   uuid.UUID
	userID uuid.UUID
}

func (m fakeCalHTTPMembers) Create(context.Context, *entity.Workspace) error { return nil }
func (m fakeCalHTTPMembers) GetByID(context.Context, uuid.UUID) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}
func (m fakeCalHTTPMembers) GetBySlug(context.Context, string) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}
func (m fakeCalHTTPMembers) ListByUser(context.Context, uuid.UUID) ([]entity.Workspace, error) {
	return nil, nil
}
func (m fakeCalHTTPMembers) Update(context.Context, *entity.Workspace) error          { return nil }
func (m fakeCalHTTPMembers) AddMember(context.Context, *entity.WorkspaceMember) error { return nil }
func (m fakeCalHTTPMembers) GetMember(_ context.Context, workspaceID, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	if workspaceID == m.wsID && userID == m.userID {
		return &entity.WorkspaceMember{ID: uuid.New(), WorkspaceID: workspaceID, UserID: userID}, nil
	}
	return nil, cerrors.NotFound("workspace member not found")
}
func (m fakeCalHTTPMembers) ListMembers(context.Context, uuid.UUID, pagination.Params) ([]entity.WorkspaceMember, error) {
	return nil, nil
}
func (m fakeCalHTTPMembers) UpdateMemberRole(context.Context, uuid.UUID, uuid.UUID, entity.WorkspaceRole) error {
	return nil
}
func (m fakeCalHTTPMembers) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type noopCalHTTPPublisher struct{}

func (noopCalHTTPPublisher) Publish(context.Context, string, []byte) error { return nil }
