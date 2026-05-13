package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	calrrule "aloqa/internal/pkg/rrule"
)

type CalendarRepo struct {
	pool *pgxpool.Pool
	db   queryable
}

func NewCalendarRepo(pool *pgxpool.Pool) *CalendarRepo {
	return &CalendarRepo{pool: pool, db: pool}
}

func (r *CalendarRepo) withTx(tx pgx.Tx) *CalendarRepo {
	if r == nil {
		return nil
	}
	return &CalendarRepo{pool: r.pool, db: tx}
}

func (r *CalendarRepo) ListUserCalendars(ctx context.Context, workspaceID, ownerID uuid.UUID) ([]entity.UserCalendar, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, owner_id, workspace_id, name, color, is_default, is_system, is_visible, created_at
		FROM user_calendars
		WHERE workspace_id = $1 AND owner_id = $2
		ORDER BY is_default DESC, created_at ASC`, workspaceID, ownerID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list user calendars: %w", err)
	}
	defer rows.Close()

	var calendars []entity.UserCalendar
	for rows.Next() {
		calendar, err := scanUserCalendar(rows)
		if err != nil {
			return nil, err
		}
		calendars = append(calendars, *calendar)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list user calendars rows: %w", err)
	}
	return calendars, nil
}

func (r *CalendarRepo) GetUserCalendar(ctx context.Context, workspaceID, calendarID, ownerID uuid.UUID) (*entity.UserCalendar, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, owner_id, workspace_id, name, color, is_default, is_system, is_visible, created_at
		FROM user_calendars
		WHERE workspace_id = $1 AND id = $2 AND owner_id = $3`, workspaceID, calendarID, ownerID)
	calendar, err := scanUserCalendar(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("calendar not found")
		}
		return nil, err
	}
	return calendar, nil
}

func (r *CalendarRepo) CreateUserCalendar(ctx context.Context, calendar *entity.UserCalendar) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO user_calendars (id, owner_id, workspace_id, name, color, is_default, is_system, is_visible, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		calendar.ID,
		calendar.OwnerID,
		calendar.WorkspaceID,
		calendar.Name,
		calendar.Color,
		calendar.IsDefault,
		calendar.IsSystem,
		calendar.IsVisible,
		calendar.CreatedAt,
	)
	if err != nil {
		return wrapCalendarWriteErr(err, "create user calendar")
	}
	return nil
}

func (r *CalendarRepo) UpdateUserCalendar(ctx context.Context, calendar *entity.UserCalendar) error {
	row := r.db.QueryRow(ctx, `
		UPDATE user_calendars
		SET name = $4, color = $5
		WHERE workspace_id = $1 AND id = $2 AND owner_id = $3 AND is_system = false
		RETURNING id, owner_id, workspace_id, name, color, is_default, is_system, is_visible, created_at`,
		calendar.WorkspaceID, calendar.ID, calendar.OwnerID, calendar.Name, calendar.Color)
	updated, err := scanUserCalendar(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return cerrors.NotFound("calendar not found")
		}
		return err
	}
	*calendar = *updated
	return nil
}

func (r *CalendarRepo) DeleteUserCalendar(ctx context.Context, workspaceID, calendarID, ownerID uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `
		DELETE FROM user_calendars
		WHERE workspace_id = $1 AND id = $2 AND owner_id = $3 AND is_default = false AND is_system = false`,
		workspaceID, calendarID, ownerID)
	if err != nil {
		return fmt.Errorf("postgres: delete user calendar: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("calendar not found")
	}
	return nil
}

func (r *CalendarRepo) SetCalendarVisibility(ctx context.Context, workspaceID, calendarID, ownerID uuid.UUID, visible bool) (*entity.UserCalendar, error) {
	row := r.db.QueryRow(ctx, `
		UPDATE user_calendars
		SET is_visible = $4
		WHERE workspace_id = $1 AND id = $2 AND owner_id = $3
		RETURNING id, owner_id, workspace_id, name, color, is_default, is_system, is_visible, created_at`,
		workspaceID, calendarID, ownerID, visible)
	calendar, err := scanUserCalendar(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("calendar not found")
		}
		return nil, err
	}
	return calendar, nil
}

func (r *CalendarRepo) EnsureDefaultCalendar(ctx context.Context, workspaceID, ownerID uuid.UUID) (*entity.UserCalendar, error) {
	now := time.Now().UTC()
	row := r.db.QueryRow(ctx, `
		INSERT INTO user_calendars (id, owner_id, workspace_id, name, color, is_default, is_system, is_visible, created_at)
		VALUES ($1, $2, $3, $4, $5, true, false, true, $6)
		ON CONFLICT DO NOTHING
		RETURNING id, owner_id, workspace_id, name, color, is_default, is_system, is_visible, created_at`,
		id.New(), ownerID, workspaceID, "Рабочий", entity.CalendarColorBrand, now)
	calendar, err := scanUserCalendar(row)
	if err == nil {
		return calendar, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return r.defaultCalendar(ctx, workspaceID, ownerID)
}

func (r *CalendarRepo) ListEvents(ctx context.Context, workspaceID uuid.UUID, fromTS, toTS time.Time, viewerID uuid.UUID) ([]entity.EventOccurrence, error) {
	rows, err := r.db.Query(ctx, `
		SELECT e.id, e.calendar_id, e.workspace_id, e.channel_id, e.organizer_id, e.title,
		       e.description, e.location_type, e.location_value, e.scheduled_at, e.originator_tz, e.duration_minutes,
		       e.all_day, e.recurrence_rrule, e.recurrence_exdates, e.call_id, e.created_at, e.updated_at
		FROM calendar_events e
		JOIN user_calendars uc ON uc.id = e.calendar_id
		WHERE e.workspace_id = $1
		  AND uc.is_visible = true
		  AND e.scheduled_at <= $3
		  AND (
		      e.recurrence_rrule IS NOT NULL
		      OR e.scheduled_at + make_interval(mins => e.duration_minutes) >= $2
		  )
		  AND (
		      uc.owner_id = $4
		      OR e.organizer_id = $4
		      OR EXISTS (
		          SELECT 1 FROM event_attendees ea
		          WHERE ea.event_id = e.id AND ea.user_id = $4
		      )
		  )
		ORDER BY e.scheduled_at ASC`, workspaceID, fromTS, toTS, viewerID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list calendar events: %w", err)
	}
	defer rows.Close()

	events, err := scanCalendarEvents(rows)
	if err != nil {
		return nil, err
	}
	if err := r.hydrateEvents(ctx, events); err != nil {
		return nil, err
	}
	return expandOccurrences(events, fromTS, toTS)
}

func (r *CalendarRepo) ListUpcoming(ctx context.Context, workspaceID, viewerID uuid.UUID, withinMinutes int) ([]entity.EventOccurrence, error) {
	if withinMinutes <= 0 {
		withinMinutes = 60
	}
	now := time.Now().UTC()
	occurrences, err := r.ListEvents(ctx, workspaceID, now, now.Add(time.Duration(withinMinutes)*time.Minute), viewerID)
	if err != nil {
		return nil, err
	}
	filtered := occurrences[:0]
	for _, occurrence := range occurrences {
		if occurrence.InstanceAt.Before(now) {
			continue
		}
		filtered = append(filtered, occurrence)
	}
	sortOccurrences(filtered)
	return filtered, nil
}

func (r *CalendarRepo) ListEventsForReminderFanout(ctx context.Context, withinWindow time.Duration) ([]entity.ReminderTarget, error) {
	if withinWindow <= 0 {
		withinWindow = 30 * time.Second
	}
	now := time.Now().UTC()
	windowStart := now.Add(-withinWindow)
	horizon := now.Add(10080 * time.Minute)
	return r.computeReminderTargets(ctx, windowStart, now, horizon)
}

func (r *CalendarRepo) computeReminderTargets(ctx context.Context, windowStart, now, horizon time.Time) ([]entity.ReminderTarget, error) {
	rows, err := r.db.Query(ctx, `
		SELECT e.id, e.calendar_id, e.workspace_id, e.channel_id, e.organizer_id, e.title,
		       e.description, e.location_type, e.location_value, e.scheduled_at, e.originator_tz, e.duration_minutes,
		       e.all_day, e.recurrence_rrule, e.recurrence_exdates, e.call_id, e.created_at, e.updated_at,
		       r.user_id, r.offset_minutes
		FROM calendar_events e
		JOIN event_reminders r ON r.event_id = e.id
		WHERE e.scheduled_at <= $2
		  AND (
		      e.recurrence_rrule IS NOT NULL
		      OR e.scheduled_at >= $1
		  )
		ORDER BY e.scheduled_at ASC`, windowStart, horizon)
	if err != nil {
		return nil, fmt.Errorf("postgres: compute reminder targets: %w", err)
	}
	defer rows.Close()

	type reminderRow struct {
		eventID       uuid.UUID
		userID        uuid.UUID
		offsetMinutes int
	}
	eventsByID := map[uuid.UUID]*entity.CalendarEvent{}
	var events []*entity.CalendarEvent
	var reminders []reminderRow
	for rows.Next() {
		event, userID, offset, err := scanCalendarEventReminderRow(rows)
		if err != nil {
			return nil, err
		}
		if _, ok := eventsByID[event.ID]; !ok {
			eventsByID[event.ID] = event
			events = append(events, event)
		}
		reminders = append(reminders, reminderRow{eventID: event.ID, userID: userID, offsetMinutes: offset})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: compute reminder targets rows: %w", err)
	}
	if err := r.hydrateEvents(ctx, events); err != nil {
		return nil, err
	}

	var targets []entity.ReminderTarget
	seen := map[string]struct{}{}
	for _, reminder := range reminders {
		event := eventsByID[reminder.eventID]
		if event == nil {
			continue
		}
		occurrences, err := expandOccurrences([]*entity.CalendarEvent{event}, windowStart, horizon)
		if err != nil {
			return nil, err
		}
		for _, occurrence := range occurrences {
			dueAt := occurrence.InstanceAt.Add(-time.Duration(reminder.offsetMinutes) * time.Minute)
			if !dueAt.After(windowStart) || dueAt.After(now) {
				continue
			}
			key := fmt.Sprintf("%s:%s:%d:%s", reminder.eventID, reminder.userID, reminder.offsetMinutes, occurrence.InstanceAt.UTC().Format(time.RFC3339Nano))
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			targets = append(targets, entity.ReminderTarget{
				Occurrence:    occurrence,
				UserID:        reminder.userID,
				OffsetMinutes: reminder.offsetMinutes,
			})
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].Occurrence.InstanceAt.Before(targets[j].Occurrence.InstanceAt)
	})
	return targets, nil
}

func (r *CalendarRepo) GetEvent(ctx context.Context, eventID uuid.UUID) (*entity.CalendarEvent, error) {
	row := r.db.QueryRow(ctx, calendarEventSelectSQL()+` WHERE e.id = $1`, eventID)
	event, err := scanCalendarEvent(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("event not found")
		}
		return nil, err
	}
	if err := r.hydrateEvents(ctx, []*entity.CalendarEvent{event}); err != nil {
		return nil, err
	}
	return event, nil
}

func (r *CalendarRepo) CreateEvent(ctx context.Context, event *entity.CalendarEvent) (*entity.CalendarEvent, error) {
	if r.pool == nil {
		return nil, cerrors.Unavailable("calendar repository transaction support is not configured")
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("postgres: begin create calendar event tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txRepo := r.withTx(tx)
	if err := txRepo.insertEvent(ctx, event); err != nil {
		return nil, err
	}
	if err := txRepo.replaceAttendees(ctx, event.ID, event.Attendees); err != nil {
		return nil, err
	}
	if err := txRepo.replaceReminders(ctx, event.ID, event.Reminders); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("postgres: commit create calendar event tx: %w", err)
	}
	return r.GetEvent(ctx, event.ID)
}

func (r *CalendarRepo) UpdateEvent(ctx context.Context, event *entity.CalendarEvent) (*entity.CalendarEvent, error) {
	if r.pool == nil {
		return nil, cerrors.Unavailable("calendar repository transaction support is not configured")
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("postgres: begin update calendar event tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txRepo := r.withTx(tx)
	if err := txRepo.updateEventRow(ctx, event); err != nil {
		return nil, err
	}
	if err := txRepo.replaceAttendees(ctx, event.ID, event.Attendees); err != nil {
		return nil, err
	}
	if err := txRepo.replaceReminders(ctx, event.ID, event.Reminders); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("postgres: commit update calendar event tx: %w", err)
	}
	return r.GetEvent(ctx, event.ID)
}

func (r *CalendarRepo) DeleteEvent(ctx context.Context, workspaceID, eventID uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `
		DELETE FROM calendar_events
		WHERE workspace_id = $1 AND id = $2`, workspaceID, eventID)
	if err != nil {
		return fmt.Errorf("postgres: delete calendar event: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("event not found")
	}
	return nil
}

func (r *CalendarRepo) SetEventCallIDIfUnset(ctx context.Context, eventID, callID uuid.UUID) (*entity.CalendarEvent, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE calendar_events
		SET call_id = $2
		WHERE id = $1 AND call_id IS NULL`, eventID, callID)
	if err != nil {
		return nil, fmt.Errorf("postgres: set event call id: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return r.GetEvent(ctx, eventID)
	}
	return r.GetEvent(ctx, eventID)
}

func (r *CalendarRepo) UpsertRsvp(ctx context.Context, eventID, userID uuid.UUID, status entity.RsvpStatus) (*entity.EventAttendee, error) {
	now := time.Now().UTC()
	row := r.db.QueryRow(ctx, `
		INSERT INTO event_attendees (id, event_id, user_id, email, is_required, rsvp_status, responded_at)
		VALUES ($1, $2, $3, NULL, true, $4, $5)
		ON CONFLICT (event_id, user_id) WHERE user_id IS NOT NULL
		DO UPDATE SET rsvp_status = EXCLUDED.rsvp_status, responded_at = EXCLUDED.responded_at
		RETURNING id, event_id, user_id, email, is_required, rsvp_status, responded_at`,
		id.New(), eventID, userID, status, now)
	attendee, err := scanEventAttendee(row)
	if err != nil {
		return nil, wrapCalendarWriteErr(err, "upsert event rsvp")
	}
	return attendee, nil
}

func (r *CalendarRepo) defaultCalendar(ctx context.Context, workspaceID, ownerID uuid.UUID) (*entity.UserCalendar, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, owner_id, workspace_id, name, color, is_default, is_system, is_visible, created_at
		FROM user_calendars
		WHERE workspace_id = $1 AND owner_id = $2 AND is_default = true`, workspaceID, ownerID)
	calendar, err := scanUserCalendar(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("default calendar not found")
		}
		return nil, err
	}
	return calendar, nil
}

func (r *CalendarRepo) insertEvent(ctx context.Context, event *entity.CalendarEvent) error {
	rrule, exdates := recurrenceColumns(event.Recurrence)
	_, err := r.db.Exec(ctx, `
		INSERT INTO calendar_events (
			id, calendar_id, workspace_id, channel_id, organizer_id, title, description,
			location_type, location_value, scheduled_at, originator_tz, duration_minutes, all_day,
			recurrence_rrule, recurrence_exdates, call_id, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
		event.ID, event.CalendarID, event.WorkspaceID, event.ChannelID, event.OrganizerID,
		event.Title, event.Description, event.Location.Type, event.Location.Value, event.ScheduledAt,
		originatorTZOrDefault(event.OriginatorTZ), event.DurationMinutes, event.AllDay, rrule, exdates, event.CallID, event.CreatedAt, event.UpdatedAt)
	if err != nil {
		return wrapCalendarWriteErr(err, "create calendar event")
	}
	return nil
}

func (r *CalendarRepo) updateEventRow(ctx context.Context, event *entity.CalendarEvent) error {
	rrule, exdates := recurrenceColumns(event.Recurrence)
	row := r.db.QueryRow(ctx, `
		UPDATE calendar_events
		SET calendar_id = $2, channel_id = $3, title = $4, description = $5,
		    location_type = $6, location_value = $7, scheduled_at = $8, originator_tz = $9, duration_minutes = $10,
		    all_day = $11, recurrence_rrule = $12, recurrence_exdates = $13, call_id = $14
		WHERE id = $1 AND workspace_id = $15
		RETURNING id, calendar_id, workspace_id, channel_id, organizer_id, title,
		          description, location_type, location_value, scheduled_at, originator_tz, duration_minutes,
		          all_day, recurrence_rrule, recurrence_exdates, call_id, created_at, updated_at`,
		event.ID, event.CalendarID, event.ChannelID, event.Title, event.Description,
		event.Location.Type, event.Location.Value, event.ScheduledAt, originatorTZOrDefault(event.OriginatorTZ), event.DurationMinutes,
		event.AllDay, rrule, exdates, event.CallID, event.WorkspaceID)
	updated, err := scanCalendarEvent(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return cerrors.NotFound("event not found")
		}
		return err
	}
	event.CreatedAt = updated.CreatedAt
	event.UpdatedAt = updated.UpdatedAt
	return nil
}

func (r *CalendarRepo) replaceAttendees(ctx context.Context, eventID uuid.UUID, attendees []entity.EventAttendee) error {
	if _, err := r.db.Exec(ctx, `DELETE FROM event_attendees WHERE event_id = $1`, eventID); err != nil {
		return fmt.Errorf("postgres: replace event attendees delete: %w", err)
	}
	for _, attendee := range attendees {
		if attendee.ID == uuid.Nil {
			attendee.ID = id.New()
		}
		attendee.EventID = eventID
		_, err := r.db.Exec(ctx, `
			INSERT INTO event_attendees (id, event_id, user_id, email, is_required, rsvp_status, responded_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			attendee.ID, attendee.EventID, attendee.UserID, attendee.Email, attendee.IsRequired, attendee.RsvpStatus, attendee.RespondedAt)
		if err != nil {
			return wrapCalendarWriteErr(err, "replace event attendees")
		}
	}
	return nil
}

func (r *CalendarRepo) replaceReminders(ctx context.Context, eventID uuid.UUID, reminders []entity.EventReminder) error {
	if _, err := r.db.Exec(ctx, `DELETE FROM event_reminders WHERE event_id = $1`, eventID); err != nil {
		return fmt.Errorf("postgres: replace event reminders delete: %w", err)
	}
	for _, reminder := range reminders {
		if reminder.UserID == uuid.Nil {
			continue
		}
		if reminder.ID == uuid.Nil {
			reminder.ID = id.New()
		}
		_, err := r.db.Exec(ctx, `
			INSERT INTO event_reminders (id, event_id, user_id, offset_minutes, channel, reminders_dispatched_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (event_id, user_id, offset_minutes, channel) DO NOTHING`,
			reminder.ID, eventID, reminder.UserID, reminder.OffsetMinutes, reminder.Channel, reminder.RemindersDispatchedAt)
		if err != nil {
			return wrapCalendarWriteErr(err, "replace event reminders")
		}
	}
	return nil
}

func (r *CalendarRepo) hydrateEvents(ctx context.Context, events []*entity.CalendarEvent) error {
	if len(events) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(events))
	byID := make(map[uuid.UUID]*entity.CalendarEvent, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
		byID[event.ID] = event
	}

	rows, err := r.db.Query(ctx, `
		SELECT id, event_id, user_id, email, is_required, rsvp_status, responded_at
		FROM event_attendees
		WHERE event_id = ANY($1)
		ORDER BY event_id, user_id NULLS LAST, email NULLS LAST`, ids)
	if err != nil {
		return fmt.Errorf("postgres: hydrate event attendees: %w", err)
	}
	for rows.Next() {
		attendee, err := scanEventAttendee(rows)
		if err != nil {
			rows.Close()
			return err
		}
		if event := byID[attendee.EventID]; event != nil {
			event.Attendees = append(event.Attendees, *attendee)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("postgres: hydrate event attendees rows: %w", err)
	}
	rows.Close()

	rows, err = r.db.Query(ctx, `
		SELECT DISTINCT event_id, offset_minutes, channel
		FROM event_reminders
		WHERE event_id = ANY($1)
		ORDER BY event_id, offset_minutes, channel`, ids)
	if err != nil {
		return fmt.Errorf("postgres: hydrate event reminders: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var eventID uuid.UUID
		var reminder entity.EventReminder
		if err := rows.Scan(&eventID, &reminder.OffsetMinutes, &reminder.Channel); err != nil {
			return fmt.Errorf("postgres: hydrate event reminders scan: %w", err)
		}
		if event := byID[eventID]; event != nil {
			event.Reminders = append(event.Reminders, reminder)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: hydrate event reminders rows: %w", err)
	}
	return nil
}

func scanCalendarEvents(rows pgx.Rows) ([]*entity.CalendarEvent, error) {
	var events []*entity.CalendarEvent
	for rows.Next() {
		event, err := scanCalendarEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: calendar events rows: %w", err)
	}
	return events, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanUserCalendar(row scanner) (*entity.UserCalendar, error) {
	var calendar entity.UserCalendar
	if err := row.Scan(
		&calendar.ID,
		&calendar.OwnerID,
		&calendar.WorkspaceID,
		&calendar.Name,
		&calendar.Color,
		&calendar.IsDefault,
		&calendar.IsSystem,
		&calendar.IsVisible,
		&calendar.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("postgres: scan user calendar: %w", err)
	}
	return &calendar, nil
}

func scanCalendarEvent(row scanner) (*entity.CalendarEvent, error) {
	var event entity.CalendarEvent
	var locationValue *string
	var recurrenceRRule *string
	var recurrenceExdates []time.Time
	if err := row.Scan(
		&event.ID,
		&event.CalendarID,
		&event.WorkspaceID,
		&event.ChannelID,
		&event.OrganizerID,
		&event.Title,
		&event.Description,
		&event.Location.Type,
		&locationValue,
		&event.ScheduledAt,
		&event.OriginatorTZ,
		&event.DurationMinutes,
		&event.AllDay,
		&recurrenceRRule,
		&recurrenceExdates,
		&event.CallID,
		&event.CreatedAt,
		&event.UpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("postgres: scan calendar event: %w", err)
	}
	event.Location.Value = locationValue
	if recurrenceRRule != nil {
		event.Recurrence = &entity.RecurrenceRule{
			RRule:   *recurrenceRRule,
			Exdates: recurrenceExdates,
		}
	}
	event.OriginatorTZ = originatorTZOrDefault(event.OriginatorTZ)
	return &event, nil
}

func scanCalendarEventReminderRow(row scanner) (*entity.CalendarEvent, uuid.UUID, int, error) {
	var event entity.CalendarEvent
	var locationValue *string
	var recurrenceRRule *string
	var recurrenceExdates []time.Time
	var userID uuid.UUID
	var offsetMinutes int
	if err := row.Scan(
		&event.ID,
		&event.CalendarID,
		&event.WorkspaceID,
		&event.ChannelID,
		&event.OrganizerID,
		&event.Title,
		&event.Description,
		&event.Location.Type,
		&locationValue,
		&event.ScheduledAt,
		&event.OriginatorTZ,
		&event.DurationMinutes,
		&event.AllDay,
		&recurrenceRRule,
		&recurrenceExdates,
		&event.CallID,
		&event.CreatedAt,
		&event.UpdatedAt,
		&userID,
		&offsetMinutes,
	); err != nil {
		return nil, uuid.Nil, 0, fmt.Errorf("postgres: scan calendar event reminder row: %w", err)
	}
	event.Location.Value = locationValue
	if recurrenceRRule != nil {
		event.Recurrence = &entity.RecurrenceRule{RRule: *recurrenceRRule, Exdates: recurrenceExdates}
	}
	event.OriginatorTZ = originatorTZOrDefault(event.OriginatorTZ)
	return &event, userID, offsetMinutes, nil
}

func scanEventAttendee(row scanner) (*entity.EventAttendee, error) {
	var attendee entity.EventAttendee
	if err := row.Scan(
		&attendee.ID,
		&attendee.EventID,
		&attendee.UserID,
		&attendee.Email,
		&attendee.IsRequired,
		&attendee.RsvpStatus,
		&attendee.RespondedAt,
	); err != nil {
		return nil, fmt.Errorf("postgres: scan event attendee: %w", err)
	}
	return &attendee, nil
}

func calendarEventSelectSQL() string {
	return `
		SELECT e.id, e.calendar_id, e.workspace_id, e.channel_id, e.organizer_id, e.title,
		       e.description, e.location_type, e.location_value, e.scheduled_at, e.originator_tz, e.duration_minutes,
		       e.all_day, e.recurrence_rrule, e.recurrence_exdates, e.call_id, e.created_at, e.updated_at
		FROM calendar_events e`
}

func recurrenceColumns(recurrence *entity.RecurrenceRule) (*string, []time.Time) {
	if recurrence == nil {
		return nil, nil
	}
	return &recurrence.RRule, recurrence.Exdates
}

func originatorTZOrDefault(originatorTZ string) string {
	originatorTZ = strings.TrimSpace(originatorTZ)
	if originatorTZ == "" {
		return "UTC"
	}
	return originatorTZ
}

func expandOccurrences(events []*entity.CalendarEvent, fromTS, toTS time.Time) ([]entity.EventOccurrence, error) {
	var occurrences []entity.EventOccurrence
	for _, event := range events {
		if event.Recurrence == nil || event.Recurrence.RRule == "" {
			endAt := event.ScheduledAt.Add(time.Duration(event.DurationMinutes) * time.Minute)
			if event.ScheduledAt.After(toTS) || endAt.Before(fromTS) {
				continue
			}
			occurrences = append(occurrences, entity.EventOccurrence{
				CalendarEvent:       *event,
				InstanceAt:          event.ScheduledAt,
				IsRecurringInstance: false,
			})
			continue
		}
		loc := loadOriginatorLocation(event.OriginatorTZ)
		expanded, err := calrrule.Expand(event.Recurrence.RRule, event.ScheduledAt.In(loc), event.Recurrence.Exdates, fromTS.In(loc), toTS.In(loc))
		if err != nil {
			return nil, err
		}
		for _, instanceAt := range expanded {
			occurrences = append(occurrences, entity.EventOccurrence{
				CalendarEvent:       *event,
				InstanceAt:          instanceAt.UTC(),
				IsRecurringInstance: true,
			})
		}
	}
	sortOccurrences(occurrences)
	return occurrences, nil
}

func loadOriginatorLocation(originatorTZ string) *time.Location {
	originatorTZ = originatorTZOrDefault(originatorTZ)
	loc, err := time.LoadLocation(originatorTZ)
	if err == nil {
		return loc
	}
	slog.Warn("invalid calendar event originator_tz, falling back to UTC", "originator_tz", originatorTZ, "error", err)
	return time.UTC
}

func sortOccurrences(occurrences []entity.EventOccurrence) {
	sort.Slice(occurrences, func(i, j int) bool {
		if occurrences[i].InstanceAt.Equal(occurrences[j].InstanceAt) {
			return occurrences[i].ID.String() < occurrences[j].ID.String()
		}
		return occurrences[i].InstanceAt.Before(occurrences[j].InstanceAt)
	})
}

func wrapCalendarWriteErr(err error, action string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503":
			return cerrors.NotFound("referenced calendar resource not found")
		case "23505":
			return cerrors.AlreadyExists("calendar resource already exists")
		case "23514":
			return cerrors.InvalidInput("calendar resource violates constraints")
		}
	}
	return fmt.Errorf("postgres: %s: %w", action, err)
}
