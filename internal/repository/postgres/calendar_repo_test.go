package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
)

type calendarRepoTestEnv struct {
	workspaceID uuid.UUID
	userID      uuid.UUID
	calendarID  uuid.UUID
}

func TestDiffReminderDefinitionsPreservesStableRowsAndDeletesOnlyRemoved(t *testing.T) {
	userA := uuid.New()
	userB := uuid.New()
	userC := uuid.New()
	idA := uuid.New()
	idB := uuid.New()
	incomingA := uuid.New()
	incomingC := uuid.New()

	keyA := reminderDefinitionKey{userID: userA, offsetMinutes: 10, channel: entity.ReminderChannelInApp}
	keyB := reminderDefinitionKey{userID: userB, offsetMinutes: 30, channel: entity.ReminderChannelOS}

	removed, added := diffReminderDefinitions(map[reminderDefinitionKey]uuid.UUID{
		keyA: idA,
		keyB: idB,
	}, []entity.EventReminder{
		{ID: incomingA, UserID: userA, OffsetMinutes: 10, Channel: entity.ReminderChannelInApp},
		{ID: incomingC, UserID: userC, OffsetMinutes: 60, Channel: entity.ReminderChannelInApp},
		{ID: uuid.New(), UserID: userC, OffsetMinutes: 60, Channel: entity.ReminderChannelInApp},
		{ID: uuid.New(), UserID: uuid.Nil, OffsetMinutes: 5, Channel: entity.ReminderChannelInApp},
	})

	if len(removed) != 1 || removed[0] != idB {
		t.Fatalf("removed reminder ids = %v, want only %s", removed, idB)
	}
	if len(added) != 1 {
		t.Fatalf("added reminders = %v, want exactly one", added)
	}
	if added[0].ID != incomingC || added[0].UserID != userC || added[0].OffsetMinutes != 60 || added[0].Channel != entity.ReminderChannelInApp {
		t.Fatalf("added reminder = %+v, want new user C definition", added[0])
	}
	if added[0].ID == incomingA {
		t.Fatalf("unchanged reminder was re-added with incoming id %s; existing id %s must remain stable", incomingA, idA)
	}
}

func TestCalendarRepoUpdateEventPreservesReminderDispatchedState(t *testing.T) {
	ctx, pool := setupCalendarRepoPostgresTest(t)
	env := setupCalendarTestEnv(t, ctx, pool)
	repo := NewCalendarRepo(pool)

	created, err := repo.CreateEvent(ctx, newCalendarRepoTestEvent(env, "Stable reminder dispatch", []entity.EventReminder{
		{UserID: env.userID, OffsetMinutes: 10, Channel: entity.ReminderChannelInApp},
	}))
	if err != nil {
		t.Fatalf("create calendar event: %v", err)
	}

	reminderID := selectCalendarRepoTestReminderID(t, ctx, pool, created.ID, env.userID, 10, entity.ReminderChannelInApp)
	occurrenceAt := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	dispatchedAt := time.Date(2026, 1, 1, 9, 50, 0, 0, time.UTC)
	insertCalendarRepoTestDispatch(t, ctx, pool, reminderID, occurrenceAt, dispatchedAt)

	update := *created
	update.Title = "Stable reminder dispatch updated"
	update.Reminders = []entity.EventReminder{
		{UserID: env.userID, OffsetMinutes: 10, Channel: entity.ReminderChannelInApp},
	}
	if _, err := repo.UpdateEvent(ctx, &update); err != nil {
		t.Fatalf("update calendar event: %v", err)
	}

	var gotReminderID uuid.UUID
	var gotDispatchedAt time.Time
	err = pool.QueryRow(ctx, `
		SELECT r.id, d.dispatched_at
		FROM event_reminders r
		JOIN event_reminder_dispatches d ON d.reminder_id = r.id
		WHERE r.event_id = $1
		  AND r.user_id = $2
		  AND r.offset_minutes = $3
		  AND r.channel = $4
		  AND d.occurrence_at = $5`,
		created.ID,
		env.userID,
		10,
		entity.ReminderChannelInApp,
		occurrenceAt,
	).Scan(&gotReminderID, &gotDispatchedAt)
	if err != nil {
		t.Fatalf("select preserved reminder dispatch: %v", err)
	}
	if gotReminderID != reminderID {
		t.Fatalf("reminder id = %s, want original %s", gotReminderID, reminderID)
	}
	if !gotDispatchedAt.Equal(dispatchedAt) {
		t.Fatalf("dispatched_at = %s, want %s", gotDispatchedAt, dispatchedAt)
	}
}

func TestCalendarRepoUpdateEventCascadesDispatchOnReminderRemoval(t *testing.T) {
	ctx, pool := setupCalendarRepoPostgresTest(t)
	env := setupCalendarTestEnv(t, ctx, pool)
	repo := NewCalendarRepo(pool)

	created, err := repo.CreateEvent(ctx, newCalendarRepoTestEvent(env, "Removed reminder cascade", []entity.EventReminder{
		{UserID: env.userID, OffsetMinutes: 10, Channel: entity.ReminderChannelInApp},
		{UserID: env.userID, OffsetMinutes: 30, Channel: entity.ReminderChannelOS},
	}))
	if err != nil {
		t.Fatalf("create calendar event: %v", err)
	}

	r1ID := selectCalendarRepoTestReminderID(t, ctx, pool, created.ID, env.userID, 10, entity.ReminderChannelInApp)
	r2ID := selectCalendarRepoTestReminderID(t, ctx, pool, created.ID, env.userID, 30, entity.ReminderChannelOS)
	occurrenceAt := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	dispatchedAt := time.Date(2026, 1, 1, 9, 50, 0, 0, time.UTC)
	insertCalendarRepoTestDispatch(t, ctx, pool, r1ID, occurrenceAt, dispatchedAt)
	insertCalendarRepoTestDispatch(t, ctx, pool, r2ID, occurrenceAt, dispatchedAt)

	update := *created
	update.Title = "Removed reminder cascade updated"
	update.Reminders = []entity.EventReminder{
		{UserID: env.userID, OffsetMinutes: 10, Channel: entity.ReminderChannelInApp},
	}
	if _, err := repo.UpdateEvent(ctx, &update); err != nil {
		t.Fatalf("update calendar event: %v", err)
	}

	reminders := selectCalendarRepoTestReminderRows(t, ctx, pool, created.ID)
	if len(reminders) != 1 {
		t.Fatalf("event_reminders row count = %d, want 1: %+v", len(reminders), reminders)
	}
	if reminders[0].id != r1ID || reminders[0].userID != env.userID || reminders[0].offsetMinutes != 10 || reminders[0].channel != entity.ReminderChannelInApp {
		t.Fatalf("remaining reminder = %+v, want original r1 %s", reminders[0], r1ID)
	}

	if count := countCalendarRepoTestDispatches(t, ctx, pool, r1ID); count != 1 {
		t.Fatalf("r1 dispatch count = %d, want 1", count)
	}
	if count := countCalendarRepoTestDispatches(t, ctx, pool, r2ID); count != 0 {
		t.Fatalf("r2 dispatch count = %d, want 0 after reminder delete cascade", count)
	}
}

func setupCalendarRepoPostgresTest(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()

	dsn := os.Getenv("ALOQA_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("set ALOQA_POSTGRES_TEST_DSN to a disposable migrated Postgres database to run this integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}
	return ctx, pool
}

func setupCalendarTestEnv(t *testing.T, ctx context.Context, pool *pgxpool.Pool) calendarRepoTestEnv {
	t.Helper()

	now := time.Now().UTC()
	env := calendarRepoTestEnv{
		workspaceID: uuid.New(),
		userID:      uuid.New(),
		calendarID:  uuid.New(),
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM user_calendars WHERE id = $1`, env.calendarID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM workspaces WHERE id = $1`, env.workspaceID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, env.userID)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, display_name, avatar_url, password_hash, status, locale, created_at, updated_at)
		VALUES ($1, $2, 'Calendar Repo Test User', '', 'hash', 'active', 'en', $3, $3)`,
		env.userID,
		"calendar-repo-test-"+env.userID.String()+"@example.com",
		now,
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, name, slug, avatar_url, created_by, created_at, updated_at)
		VALUES ($1, 'Calendar Repo Test Workspace', $2, '', $3, $4, $4)`,
		env.workspaceID,
		"calendar-repo-test-"+env.workspaceID.String(),
		env.userID,
		now,
	); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_calendars (id, owner_id, workspace_id, name, color, is_default, is_system, is_visible, created_at)
		VALUES ($1, $2, $3, 'Calendar Repo Test Calendar', 'brand', true, false, true, $4)`,
		env.calendarID,
		env.userID,
		env.workspaceID,
		now,
	); err != nil {
		t.Fatalf("insert user calendar: %v", err)
	}
	return env
}

func newCalendarRepoTestEvent(env calendarRepoTestEnv, title string, reminders []entity.EventReminder) *entity.CalendarEvent {
	now := time.Now().UTC()
	return &entity.CalendarEvent{
		ID:              uuid.New(),
		CalendarID:      env.calendarID,
		WorkspaceID:     env.workspaceID,
		OrganizerID:     env.userID,
		Title:           title,
		Location:        entity.EventLocation{Type: entity.EventLocationAloqaMeet},
		ScheduledAt:     time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		OriginatorTZ:    "UTC",
		DurationMinutes: 30,
		Reminders:       reminders,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func selectCalendarRepoTestReminderID(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	eventID uuid.UUID,
	userID uuid.UUID,
	offsetMinutes int,
	channel entity.ReminderChannel,
) uuid.UUID {
	t.Helper()

	var reminderID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id
		FROM event_reminders
		WHERE event_id = $1
		  AND user_id = $2
		  AND offset_minutes = $3
		  AND channel = $4`,
		eventID,
		userID,
		offsetMinutes,
		channel,
	).Scan(&reminderID); err != nil {
		t.Fatalf("select reminder id: %v", err)
	}
	return reminderID
}

func insertCalendarRepoTestDispatch(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	reminderID uuid.UUID,
	occurrenceAt time.Time,
	dispatchedAt time.Time,
) {
	t.Helper()

	if _, err := pool.Exec(ctx, `
		INSERT INTO event_reminder_dispatches (reminder_id, occurrence_at, dispatched_at)
		VALUES ($1, $2, $3)`,
		reminderID,
		occurrenceAt,
		dispatchedAt,
	); err != nil {
		t.Fatalf("insert reminder dispatch: %v", err)
	}
}

type calendarRepoTestReminderRow struct {
	id            uuid.UUID
	userID        uuid.UUID
	offsetMinutes int
	channel       entity.ReminderChannel
}

func selectCalendarRepoTestReminderRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventID uuid.UUID) []calendarRepoTestReminderRow {
	t.Helper()

	rows, err := pool.Query(ctx, `
		SELECT id, user_id, offset_minutes, channel
		FROM event_reminders
		WHERE event_id = $1
		ORDER BY offset_minutes, channel`,
		eventID,
	)
	if err != nil {
		t.Fatalf("select reminder rows: %v", err)
	}
	defer rows.Close()

	var reminders []calendarRepoTestReminderRow
	for rows.Next() {
		var reminder calendarRepoTestReminderRow
		if err := rows.Scan(&reminder.id, &reminder.userID, &reminder.offsetMinutes, &reminder.channel); err != nil {
			t.Fatalf("scan reminder row: %v", err)
		}
		reminders = append(reminders, reminder)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("select reminder rows: %v", err)
	}
	return reminders
}

func countCalendarRepoTestDispatches(t *testing.T, ctx context.Context, pool *pgxpool.Pool, reminderID uuid.UUID) int {
	t.Helper()

	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM event_reminder_dispatches
		WHERE reminder_id = $1`,
		reminderID,
	).Scan(&count); err != nil {
		t.Fatalf("count reminder dispatches: %v", err)
	}
	return count
}
