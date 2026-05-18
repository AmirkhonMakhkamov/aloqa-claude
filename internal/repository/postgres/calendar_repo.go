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
	_, err := r.db.Exec(
		ctx, `
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

func (r *CalendarRepo) ListDueReminderTargets(ctx context.Context, now time.Time, horizonWindow time.Duration, limit int) ([]entity.ReminderTarget, error) {
	if limit <= 0 {
		limit = 100
	}
	if horizonWindow <= 0 {
		horizonWindow = 24 * time.Hour
	}
	now = now.UTC()
	if err := r.discoverDueReminderDispatches(ctx, now, horizonWindow); err != nil {
		return nil, err
	}

	rows, err := r.db.Query(ctx, `
		SELECT d.reminder_id, d.occurrence_at,
		       e.id, e.calendar_id, e.workspace_id, e.channel_id, e.organizer_id, e.title,
		       e.description, e.location_type, e.location_value, e.scheduled_at, e.originator_tz, e.duration_minutes,
		       e.all_day, e.recurrence_rrule, e.recurrence_exdates, e.call_id, e.created_at, e.updated_at,
		       r.user_id, r.offset_minutes, r.channel
		FROM event_reminder_dispatches d
		JOIN event_reminders r ON r.id = d.reminder_id
		JOIN calendar_events e ON e.id = r.event_id
		WHERE d.dispatched_at IS NULL
		  AND d.occurrence_at - make_interval(mins => r.offset_minutes) <= $1
		ORDER BY d.occurrence_at - make_interval(mins => r.offset_minutes), d.reminder_id
		LIMIT $2
		FOR UPDATE OF d SKIP LOCKED`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list due reminder targets: %w", err)
	}
	defer rows.Close()

	type dueReminderRow struct {
		reminderID    uuid.UUID
		occurrenceAt  time.Time
		event         *entity.CalendarEvent
		userID        uuid.UUID
		offsetMinutes int
		channel       entity.ReminderChannel
	}

	var dueRows []dueReminderRow
	var events []*entity.CalendarEvent
	for rows.Next() {
		var reminderID uuid.UUID
		var occurrenceAt time.Time
		var event entity.CalendarEvent
		var locationValue *string
		var recurrenceRRule *string
		var recurrenceExdates []time.Time
		var userID uuid.UUID
		var offsetMinutes int
		var channel entity.ReminderChannel
		if err := rows.Scan(
			&reminderID,
			&occurrenceAt,
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
			&channel,
		); err != nil {
			return nil, fmt.Errorf("postgres: list due reminder targets scan: %w", err)
		}
		event.Location.Value = locationValue
		if recurrenceRRule != nil {
			event.Recurrence = &entity.RecurrenceRule{RRule: *recurrenceRRule, Exdates: recurrenceExdates}
		}
		event.OriginatorTZ = originatorTZOrDefault(event.OriginatorTZ)
		eventCopy := &event
		events = append(events, eventCopy)
		dueRows = append(dueRows, dueReminderRow{
			reminderID:    reminderID,
			occurrenceAt:  occurrenceAt.UTC(),
			event:         eventCopy,
			userID:        userID,
			offsetMinutes: offsetMinutes,
			channel:       channel,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list due reminder targets rows: %w", err)
	}
	if err := r.hydrateEvents(ctx, events); err != nil {
		return nil, err
	}

	targets := make([]entity.ReminderTarget, 0, len(dueRows))
	for _, row := range dueRows {
		instanceAt := row.occurrenceAt
		targets = append(targets, entity.ReminderTarget{
			ReminderID: row.reminderID,
			Occurrence: entity.EventOccurrence{
				CalendarEvent:       *row.event,
				InstanceAt:          instanceAt,
				IsRecurringInstance: row.event.Recurrence != nil,
			},
			UserID:        row.userID,
			OffsetMinutes: row.offsetMinutes,
			Channel:       row.channel,
			FireAt:        instanceAt.Add(-time.Duration(row.offsetMinutes) * time.Minute),
		})
	}
	return targets, nil
}

func (r *CalendarRepo) discoverDueReminderDispatches(ctx context.Context, now time.Time, horizonWindow time.Duration) error {
	windowStart := now.Add(-30 * time.Second)
	horizon := now.Add(horizonWindow)
	rows, err := r.db.Query(ctx, `
		SELECT r.id, e.id, e.calendar_id, e.workspace_id, e.channel_id, e.organizer_id, e.title,
		       e.description, e.location_type, e.location_value, e.scheduled_at, e.originator_tz, e.duration_minutes,
		       e.all_day, e.recurrence_rrule, e.recurrence_exdates, e.call_id, e.created_at, e.updated_at,
		       r.user_id, r.offset_minutes, r.channel
		FROM calendar_events e
		JOIN event_reminders r ON r.event_id = e.id
		WHERE e.scheduled_at <= $2
		  AND (
		      e.recurrence_rrule IS NOT NULL
		      OR e.scheduled_at >= $1
		  )
		ORDER BY e.scheduled_at ASC`, windowStart, horizon)
	if err != nil {
		return fmt.Errorf("postgres: discover reminder dispatches: %w", err)
	}
	defer rows.Close()

	type reminderDefinition struct {
		reminderID    uuid.UUID
		eventID       uuid.UUID
		offsetMinutes int
	}
	eventsByID := map[uuid.UUID]*entity.CalendarEvent{}
	var events []*entity.CalendarEvent
	var reminders []reminderDefinition
	for rows.Next() {
		event, reminderID, _, offset, _, err := scanCalendarEventReminderRow(rows)
		if err != nil {
			return err
		}
		if _, ok := eventsByID[event.ID]; !ok {
			eventsByID[event.ID] = event
			events = append(events, event)
		}
		reminders = append(reminders, reminderDefinition{
			reminderID:    reminderID,
			eventID:       event.ID,
			offsetMinutes: offset,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: discover reminder dispatches rows: %w", err)
	}
	if err := r.hydrateEvents(ctx, events); err != nil {
		return err
	}

	seen := map[string]struct{}{}
	for _, reminder := range reminders {
		event := eventsByID[reminder.eventID]
		if event == nil {
			continue
		}
		occurrences, err := expandOccurrences([]*entity.CalendarEvent{event}, windowStart, horizon)
		if err != nil {
			return err
		}
		for _, occurrence := range occurrences {
			occurrenceAt := occurrence.InstanceAt.UTC()
			fireAt := occurrenceAt.Add(-time.Duration(reminder.offsetMinutes) * time.Minute)
			if fireAt.After(now) {
				continue
			}
			key := reminder.reminderID.String() + ":" + occurrenceAt.Format(time.RFC3339Nano)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			if _, err := r.db.Exec(ctx, `
				INSERT INTO event_reminder_dispatches (reminder_id, occurrence_at)
				VALUES ($1, $2)
				ON CONFLICT DO NOTHING`, reminder.reminderID, occurrenceAt); err != nil {
				return fmt.Errorf("postgres: insert reminder dispatch: %w", err)
			}
		}
	}
	return nil
}

func (r *CalendarRepo) EnqueueReminderOutbox(ctx context.Context, target entity.ReminderTarget, payloadJSON []byte, enqueuedAt time.Time) error {
	if _, err := r.db.Exec(
		ctx, `
		INSERT INTO reminder_outbox (reminder_id, event_id, occurrence_at, user_id, payload_json, enqueued_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		target.ReminderID,
		target.Occurrence.ID,
		target.Occurrence.InstanceAt.UTC(),
		target.UserID,
		string(payloadJSON),
		enqueuedAt.UTC(),
	); err != nil {
		return fmt.Errorf("postgres: enqueue reminder outbox: %w", err)
	}
	return nil
}

func (r *CalendarRepo) MarkReminderDispatched(ctx context.Context, reminderID uuid.UUID, occurrenceAt, dispatchedAt time.Time) error {
	_, err := r.db.Exec(ctx, `
		UPDATE event_reminder_dispatches
		SET dispatched_at = $3
		WHERE reminder_id = $1
		  AND occurrence_at = $2
		  AND dispatched_at IS NULL`, reminderID, occurrenceAt.UTC(), dispatchedAt.UTC())
	if err != nil {
		return fmt.Errorf("postgres: mark reminder dispatched: %w", err)
	}
	return nil
}

func (r *CalendarRepo) PublishReminderOutbox(ctx context.Context, limit, maxAttempts int, publish func(context.Context, entity.ReminderOutboxMessage) error) (processed, failed, dead int, err error) {
	if limit <= 0 {
		limit = 100
	}
	if maxAttempts <= 0 {
		maxAttempts = 10
	}
	if r.pool == nil {
		return 0, 0, 0, cerrors.Unavailable("calendar repository transaction support is not configured")
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, 0, 0, fmt.Errorf("postgres: begin reminder outbox tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	rows, err := tx.Query(ctx, `
		SELECT id, reminder_id, event_id, occurrence_at, user_id, payload_json, enqueued_at, attempts
		FROM reminder_outbox
		WHERE published_at IS NULL
		  AND attempts < $2
		ORDER BY enqueued_at ASC, id ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, limit, maxAttempts)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("postgres: claim reminder outbox: %w", err)
	}

	var messages []entity.ReminderOutboxMessage
	for rows.Next() {
		var msg entity.ReminderOutboxMessage
		if err := rows.Scan(
			&msg.ID,
			&msg.ReminderID,
			&msg.EventID,
			&msg.OccurrenceAt,
			&msg.UserID,
			&msg.PayloadJSON,
			&msg.EnqueuedAt,
			&msg.Attempts,
		); err != nil {
			rows.Close()
			return 0, 0, 0, fmt.Errorf("postgres: scan reminder outbox: %w", err)
		}
		msg.OccurrenceAt = msg.OccurrenceAt.UTC()
		msg.EnqueuedAt = msg.EnqueuedAt.UTC()
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, 0, fmt.Errorf("postgres: reminder outbox rows: %w", err)
	}
	rows.Close()

	for _, msg := range messages {
		if publish != nil {
			if err := publish(ctx, msg); err != nil {
				if _, updateErr := tx.Exec(ctx, `
					UPDATE reminder_outbox
					SET attempts = attempts + 1
					WHERE id = $1`, msg.ID); updateErr != nil {
					return processed, failed, dead, fmt.Errorf("postgres: mark reminder outbox failed: %w", updateErr)
				}
				if msg.Attempts+1 >= maxAttempts {
					dead++
				} else {
					failed++
				}
				continue
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE reminder_outbox
			SET published_at = NOW(),
			    attempts = attempts + 1
			WHERE id = $1
			  AND published_at IS NULL`, msg.ID); err != nil {
			return processed, failed, dead, fmt.Errorf("postgres: mark reminder outbox published: %w", err)
		}
		processed++
	}
	if err := tx.Commit(ctx); err != nil {
		return processed, failed, dead, fmt.Errorf("postgres: commit reminder outbox tx: %w", err)
	}
	committed = true
	return processed, failed, dead, nil
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

func (r *CalendarRepo) LockEventForStartCall(ctx context.Context, eventID uuid.UUID) (*entity.CalendarEvent, error) {
	row := r.db.QueryRow(ctx, calendarEventSelectSQL()+` WHERE e.id = $1 FOR UPDATE OF e`, eventID)
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

func (r *CalendarRepo) CreateEventTx(ctx context.Context, event *entity.CalendarEvent) (*entity.CalendarEvent, error) {
	if err := r.insertEvent(ctx, event); err != nil {
		return nil, err
	}
	if err := r.replaceAttendees(ctx, event.ID, event.Attendees); err != nil {
		return nil, err
	}
	if err := r.replaceReminders(ctx, event.ID, event.Reminders); err != nil {
		return nil, err
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
	if _, err := txRepo.UpdateEventTx(ctx, event, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("postgres: commit update calendar event tx: %w", err)
	}
	return r.GetEvent(ctx, event.ID)
}

func (r *CalendarRepo) UpdateEventTx(ctx context.Context, event *entity.CalendarEvent, expectedUpdatedAt *time.Time) (*entity.CalendarEvent, error) {
	if err := r.updateEventRowTx(ctx, event, expectedUpdatedAt); err != nil {
		return nil, err
	}
	if err := r.replaceAttendees(ctx, event.ID, event.Attendees); err != nil {
		return nil, err
	}
	if err := r.replaceReminders(ctx, event.ID, event.Reminders); err != nil {
		return nil, err
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

func (r *CalendarRepo) LinkEventAndCall(ctx context.Context, eventID, callID uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE calendar_events
		SET call_id = $2
		WHERE id = $1`, eventID, callID)
	if err != nil {
		return fmt.Errorf("postgres: link calendar event to call: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("event not found")
	}

	tag, err = r.db.Exec(ctx, `
		UPDATE calls
		SET scheduled_call_id = $1
		WHERE id = $2`, eventID, callID)
	if err != nil {
		return fmt.Errorf("postgres: link call to calendar event: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return cerrors.NotFound("call not found")
	}
	return nil
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

func (r *CalendarRepo) updateEventRowTx(ctx context.Context, event *entity.CalendarEvent, expectedUpdatedAt *time.Time) error {
	rrule, exdates := recurrenceColumns(event.Recurrence)
	args := []any{
		event.ID, event.CalendarID, event.ChannelID, event.Title, event.Description,
		event.Location.Type, event.Location.Value, event.ScheduledAt,
		originatorTZOrDefault(event.OriginatorTZ), event.DurationMinutes,
		event.AllDay, rrule, exdates, event.CallID, event.WorkspaceID,
	}
	sql := `
		UPDATE calendar_events
		SET calendar_id = $2, channel_id = $3, title = $4, description = $5,
		    location_type = $6, location_value = $7, scheduled_at = $8, originator_tz = $9, duration_minutes = $10,
		    all_day = $11, recurrence_rrule = $12, recurrence_exdates = $13, call_id = $14, updated_at = NOW()
		WHERE id = $1 AND workspace_id = $15`
	if expectedUpdatedAt != nil {
		sql += ` AND updated_at = $16`
		args = append(args, expectedUpdatedAt.UTC())
	}
	sql += `
		RETURNING id, calendar_id, workspace_id, channel_id, organizer_id, title,
		          description, location_type, location_value, scheduled_at, originator_tz, duration_minutes,
		          all_day, recurrence_rrule, recurrence_exdates, call_id, created_at, updated_at`
	row := r.db.QueryRow(ctx, sql, args...)
	updated, err := scanCalendarEvent(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if expectedUpdatedAt != nil {
				return cerrors.Conflict("CONCURRENT_UPDATE: event was modified concurrently")
			}
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
	rows, err := r.db.Query(ctx, `
		SELECT id, user_id, offset_minutes, channel
		FROM event_reminders
		WHERE event_id = $1`, eventID)
	if err != nil {
		return fmt.Errorf("postgres: load event reminders: %w", err)
	}

	existing := map[reminderDefinitionKey]uuid.UUID{}
	for rows.Next() {
		var reminderID uuid.UUID
		var key reminderDefinitionKey
		if err := rows.Scan(&reminderID, &key.userID, &key.offsetMinutes, &key.channel); err != nil {
			rows.Close()
			return fmt.Errorf("postgres: load event reminders scan: %w", err)
		}
		existing[key] = reminderID
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("postgres: load event reminders rows: %w", err)
	}
	rows.Close()

	removedReminderIDs, addedReminders := diffReminderDefinitions(existing, reminders)
	if len(removedReminderIDs) > 0 {
		if _, err := r.db.Exec(ctx, `
			DELETE FROM event_reminders
			WHERE event_id = $1
			  AND id = ANY($2)`, eventID, removedReminderIDs); err != nil {
			return fmt.Errorf("postgres: replace event reminders delete removed: %w", err)
		}
	}

	for _, reminder := range addedReminders {
		if reminder.UserID == uuid.Nil {
			continue
		}
		if reminder.ID == uuid.Nil {
			reminder.ID = id.New()
		}
		_, err := r.db.Exec(ctx, `
			INSERT INTO event_reminders (id, event_id, user_id, offset_minutes, channel)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (event_id, user_id, offset_minutes, channel) DO NOTHING`,
			reminder.ID, eventID, reminder.UserID, reminder.OffsetMinutes, reminder.Channel)
		if err != nil {
			return wrapCalendarWriteErr(err, "replace event reminders")
		}
	}
	return nil
}

type reminderDefinitionKey struct {
	userID        uuid.UUID
	offsetMinutes int
	channel       entity.ReminderChannel
}

func diffReminderDefinitions(existing map[reminderDefinitionKey]uuid.UUID, reminders []entity.EventReminder) ([]uuid.UUID, []entity.EventReminder) {
	desired := make(map[reminderDefinitionKey]entity.EventReminder, len(reminders))
	orderedDesiredKeys := make([]reminderDefinitionKey, 0, len(reminders))
	for _, reminder := range reminders {
		if reminder.UserID == uuid.Nil {
			continue
		}
		key := reminderDefinitionKey{
			userID:        reminder.UserID,
			offsetMinutes: reminder.OffsetMinutes,
			channel:       reminder.Channel,
		}
		if _, ok := desired[key]; ok {
			continue
		}
		desired[key] = reminder
		orderedDesiredKeys = append(orderedDesiredKeys, key)
	}

	removedReminderIDs := make([]uuid.UUID, 0)
	for key, reminderID := range existing {
		if _, ok := desired[key]; !ok {
			removedReminderIDs = append(removedReminderIDs, reminderID)
		}
	}

	addedReminders := make([]entity.EventReminder, 0)
	for _, key := range orderedDesiredKeys {
		if _, ok := existing[key]; ok {
			continue
		}
		addedReminders = append(addedReminders, desired[key])
	}
	return removedReminderIDs, addedReminders
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

func scanCalendarEventReminderRow(row scanner) (*entity.CalendarEvent, uuid.UUID, uuid.UUID, int, entity.ReminderChannel, error) {
	var event entity.CalendarEvent
	var reminderID uuid.UUID
	var locationValue *string
	var recurrenceRRule *string
	var recurrenceExdates []time.Time
	var userID uuid.UUID
	var offsetMinutes int
	var channel entity.ReminderChannel
	if err := row.Scan(
		&reminderID,
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
		&channel,
	); err != nil {
		return nil, uuid.Nil, uuid.Nil, 0, "", fmt.Errorf("postgres: scan calendar event reminder row: %w", err)
	}
	event.Location.Value = locationValue
	if recurrenceRRule != nil {
		event.Recurrence = &entity.RecurrenceRule{RRule: *recurrenceRRule, Exdates: recurrenceExdates}
	}
	event.OriginatorTZ = originatorTZOrDefault(event.OriginatorTZ)
	return &event, reminderID, userID, offsetMinutes, channel, nil
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
