package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	calendarservice "aloqa/internal/service/calendar"
)

type CalendarHandler struct {
	svc *calendarservice.Service
}

func NewCalendarHandler(svc *calendarservice.Service) *CalendarHandler {
	return &CalendarHandler{svc: svc}
}

type createCalendarRequest struct {
	Name  string               `json:"name"`
	Color entity.CalendarColor `json:"color"`
}

type updateCalendarRequest struct {
	Name  *string               `json:"name"`
	Color *entity.CalendarColor `json:"color"`
}

type visibilityRequest struct {
	IsVisible bool `json:"is_visible"`
}

type eventLocationRequest struct {
	Type  entity.EventLocationType `json:"type"`
	Value *string                  `json:"value"`
}

type recurrenceRequest struct {
	RRule   string      `json:"rrule"`
	Exdates []time.Time `json:"exdates"`
}

type createEventRequest struct {
	CalendarID      string                 `json:"calendar_id"`
	ChannelID       *string                `json:"channel_id"`
	Title           string                 `json:"title"`
	Description     *string                `json:"description"`
	Location        eventLocationRequest   `json:"location"`
	ScheduledAt     time.Time              `json:"scheduled_at"`
	DurationMinutes int                    `json:"duration_minutes"`
	AllDay          bool                   `json:"all_day"`
	Recurrence      *recurrenceRequest     `json:"recurrence"`
	AttendeeUserIDs []string               `json:"attendee_user_ids"`
	AttendeeEmails  []string               `json:"attendee_emails"`
	RequiredUserIDs []string               `json:"required_user_ids"`
	Reminders       []entity.EventReminder `json:"reminders"`
}

type rsvpRequest struct {
	Status entity.RsvpStatus `json:"status"`
}

func (h *CalendarHandler) ListCalendars(w http.ResponseWriter, r *http.Request) {
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	calendars, err := h.svc.ListUserCalendars(r.Context(), wsID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, calendars)
}

func (h *CalendarHandler) CreateCalendar(w http.ResponseWriter, r *http.Request) {
	var req createCalendarRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	calendar, err := h.svc.CreateUserCalendar(r.Context(), wsID, userID, req.Name, req.Color)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeCreated(w, calendar)
}

func (h *CalendarHandler) UpdateCalendar(w http.ResponseWriter, r *http.Request) {
	var req updateCalendarRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	calendarID, err := id.Parse(chi.URLParam(r, "calendarID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	calendar, err := h.svc.UpdateUserCalendar(r.Context(), wsID, userID, calendarID, req.Name, req.Color)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, calendar)
}

func (h *CalendarHandler) DeleteCalendar(w http.ResponseWriter, r *http.Request) {
	calendarID, err := id.Parse(chi.URLParam(r, "calendarID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	if err := h.svc.DeleteUserCalendar(r.Context(), wsID, userID, calendarID); err != nil {
		writeErr(w, err)
		return
	}
	writeNoContent(w)
}

func (h *CalendarHandler) SetCalendarVisibility(w http.ResponseWriter, r *http.Request) {
	var req visibilityRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	calendarID, err := id.Parse(chi.URLParam(r, "calendarID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	calendar, err := h.svc.SetCalendarVisibility(r.Context(), wsID, userID, calendarID, req.IsVisible)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, calendar)
}

func (h *CalendarHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	fromTS, err := parseQueryTime(r, "from")
	if err != nil {
		writeErr(w, err)
		return
	}
	toTS, err := parseQueryTime(r, "to")
	if err != nil {
		writeErr(w, err)
		return
	}
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	events, err := h.svc.ListEvents(r.Context(), wsID, fromTS, toTS, userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, events)
}

func (h *CalendarHandler) GetEvent(w http.ResponseWriter, r *http.Request) {
	eventID, err := id.Parse(chi.URLParam(r, "eventID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	eventEntity, err := h.svc.GetEvent(r.Context(), wsID, eventID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, eventEntity)
}

func (h *CalendarHandler) CreateEvent(w http.ResponseWriter, r *http.Request) {
	var req createEventRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	input, err := createEventInputFromRequest(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	eventEntity, err := h.svc.CreateEvent(r.Context(), wsID, userID, input)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeCreated(w, eventEntity)
}

func (h *CalendarHandler) UpdateEvent(w http.ResponseWriter, r *http.Request) {
	eventID, err := id.Parse(chi.URLParam(r, "eventID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var raw map[string]json.RawMessage
	if err := decodeJSON(r, &raw); err != nil {
		writeErr(w, err)
		return
	}
	input, err := updateEventInputFromRaw(raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	eventEntity, err := h.svc.UpdateEvent(r.Context(), wsID, eventID, userID, input)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, eventEntity)
}

func (h *CalendarHandler) DeleteEvent(w http.ResponseWriter, r *http.Request) {
	eventID, err := id.Parse(chi.URLParam(r, "eventID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	if err := h.svc.DeleteEvent(r.Context(), wsID, eventID, userID); err != nil {
		writeErr(w, err)
		return
	}
	writeNoContent(w)
}

func (h *CalendarHandler) UpsertRsvp(w http.ResponseWriter, r *http.Request) {
	var req rsvpRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	eventID, err := id.Parse(chi.URLParam(r, "eventID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	attendee, err := h.svc.UpsertRsvp(r.Context(), wsID, eventID, userID, req.Status)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, attendee)
}

func (h *CalendarHandler) StartCallFromEvent(w http.ResponseWriter, r *http.Request) {
	eventID, err := id.Parse(chi.URLParam(r, "eventID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	callEntity, err := h.svc.StartCallFromEvent(r.Context(), wsID, eventID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, callEntity)
}

func (h *CalendarHandler) ListUpcoming(w http.ResponseWriter, r *http.Request) {
	withinMinutes := 60
	if raw := r.URL.Query().Get("within_minutes"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeErr(w, cerrors.InvalidInput("invalid within_minutes"))
			return
		}
		withinMinutes = parsed
	}
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	events, err := h.svc.ListUpcoming(r.Context(), wsID, userID, withinMinutes)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, events)
}

func createEventInputFromRequest(req createEventRequest) (calendarservice.CreateEventInput, error) {
	calendarID, err := id.Parse(req.CalendarID)
	if err != nil {
		return calendarservice.CreateEventInput{}, err
	}
	channelID, err := parseOptionalUUID(req.ChannelID)
	if err != nil {
		return calendarservice.CreateEventInput{}, err
	}
	attendeeUserIDs, err := parseUUIDList(req.AttendeeUserIDs)
	if err != nil {
		return calendarservice.CreateEventInput{}, err
	}
	requiredUserIDs, err := parseUUIDList(req.RequiredUserIDs)
	if err != nil {
		return calendarservice.CreateEventInput{}, err
	}
	return calendarservice.CreateEventInput{
		CalendarID:      calendarID,
		ChannelID:       channelID,
		Title:           req.Title,
		Description:     req.Description,
		Location:        entity.EventLocation{Type: req.Location.Type, Value: req.Location.Value},
		ScheduledAt:     req.ScheduledAt,
		DurationMinutes: req.DurationMinutes,
		AllDay:          req.AllDay,
		Recurrence:      recurrenceFromRequest(req.Recurrence),
		AttendeeUserIDs: attendeeUserIDs,
		AttendeeEmails:  req.AttendeeEmails,
		RequiredUserIDs: requiredUserIDs,
		Reminders:       req.Reminders,
	}, nil
}

func updateEventInputFromRaw(raw map[string]json.RawMessage) (calendarservice.UpdateEventInput, error) {
	var input calendarservice.UpdateEventInput
	if value, ok := raw["calendar_id"]; ok {
		var calendarIDText string
		if err := json.Unmarshal(value, &calendarIDText); err != nil {
			return input, cerrors.InvalidInput("invalid calendar_id")
		}
		calendarID, err := id.Parse(calendarIDText)
		if err != nil {
			return input, err
		}
		input.CalendarID = &calendarID
	}
	if value, ok := raw["channel_id"]; ok {
		input.ChannelIDSet = true
		if !isJSONNull(value) {
			var channelIDText string
			if err := json.Unmarshal(value, &channelIDText); err != nil {
				return input, cerrors.InvalidInput("invalid channel_id")
			}
			channelID, err := id.Parse(channelIDText)
			if err != nil {
				return input, err
			}
			input.ChannelID = &channelID
		}
	}
	if value, ok := raw["title"]; ok {
		var title string
		if err := json.Unmarshal(value, &title); err != nil {
			return input, cerrors.InvalidInput("invalid title")
		}
		input.Title = &title
	}
	if value, ok := raw["description"]; ok {
		input.DescriptionSet = true
		if !isJSONNull(value) {
			var description string
			if err := json.Unmarshal(value, &description); err != nil {
				return input, cerrors.InvalidInput("invalid description")
			}
			input.Description = &description
		}
	}
	if value, ok := raw["location"]; ok {
		var location eventLocationRequest
		if err := json.Unmarshal(value, &location); err != nil {
			return input, cerrors.InvalidInput("invalid location")
		}
		input.Location = &entity.EventLocation{Type: location.Type, Value: location.Value}
	}
	if value, ok := raw["scheduled_at"]; ok {
		var scheduledAt time.Time
		if err := json.Unmarshal(value, &scheduledAt); err != nil {
			return input, cerrors.InvalidInput("invalid scheduled_at")
		}
		input.ScheduledAt = &scheduledAt
	}
	if value, ok := raw["duration_minutes"]; ok {
		var duration int
		if err := json.Unmarshal(value, &duration); err != nil {
			return input, cerrors.InvalidInput("invalid duration_minutes")
		}
		input.DurationMinutes = &duration
	}
	if value, ok := raw["all_day"]; ok {
		var allDay bool
		if err := json.Unmarshal(value, &allDay); err != nil {
			return input, cerrors.InvalidInput("invalid all_day")
		}
		input.AllDay = &allDay
	}
	if value, ok := raw["recurrence"]; ok {
		input.RecurrenceSet = true
		if !isJSONNull(value) {
			var recurrence recurrenceRequest
			if err := json.Unmarshal(value, &recurrence); err != nil {
				return input, cerrors.InvalidInput("invalid recurrence")
			}
			input.Recurrence = &entity.RecurrenceRule{RRule: recurrence.RRule, Exdates: recurrence.Exdates}
		}
	}
	if value, ok := raw["reminders"]; ok {
		var reminders []entity.EventReminder
		if err := json.Unmarshal(value, &reminders); err != nil {
			return input, cerrors.InvalidInput("invalid reminders")
		}
		input.Reminders = reminders
		input.RemindersSet = true
	}
	attendeesTouched := false
	if value, ok := raw["attendee_user_ids"]; ok {
		var ids []string
		if err := json.Unmarshal(value, &ids); err != nil {
			return input, cerrors.InvalidInput("invalid attendee_user_ids")
		}
		parsed, err := parseUUIDList(ids)
		if err != nil {
			return input, err
		}
		input.AttendeeUserIDs = parsed
		attendeesTouched = true
	}
	if value, ok := raw["attendee_emails"]; ok {
		if err := json.Unmarshal(value, &input.AttendeeEmails); err != nil {
			return input, cerrors.InvalidInput("invalid attendee_emails")
		}
		attendeesTouched = true
	}
	if value, ok := raw["required_user_ids"]; ok {
		var ids []string
		if err := json.Unmarshal(value, &ids); err != nil {
			return input, cerrors.InvalidInput("invalid required_user_ids")
		}
		parsed, err := parseUUIDList(ids)
		if err != nil {
			return input, err
		}
		input.RequiredUserIDs = parsed
		attendeesTouched = true
	}
	input.AttendeesSet = attendeesTouched
	return input, nil
}

func recurrenceFromRequest(req *recurrenceRequest) *entity.RecurrenceRule {
	if req == nil {
		return nil
	}
	return &entity.RecurrenceRule{RRule: req.RRule, Exdates: req.Exdates}
}

func parseQueryTime(r *http.Request, name string) (time.Time, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return time.Time{}, cerrors.InvalidInput(name + " is required")
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, cerrors.InvalidInput("invalid " + name)
	}
	return parsed, nil
}

func parseOptionalUUID(raw *string) (*uuid.UUID, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	parsed, err := id.Parse(*raw)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func parseUUIDList(raw []string) ([]uuid.UUID, error) {
	result := make([]uuid.UUID, 0, len(raw))
	for _, value := range raw {
		parsed, err := id.Parse(value)
		if err != nil {
			return nil, err
		}
		result = append(result, parsed)
	}
	return result, nil
}

func isJSONNull(value json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}
