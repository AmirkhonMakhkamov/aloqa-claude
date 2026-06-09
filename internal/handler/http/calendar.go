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
	callservice "aloqa/internal/service/call"
)

type CalendarHandler struct {
	svc     *calendarservice.Service
	callSvc *callservice.Service
}

func NewCalendarHandler(svc *calendarservice.Service, callSvc ...*callservice.Service) *CalendarHandler {
	h := &CalendarHandler{svc: svc}
	if len(callSvc) > 0 {
		h.callSvc = callSvc[0]
	}
	return h
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

// eventCallSettingsRequest is the optional per-event call-settings preconfig
// accepted on event create/update (ALK-819 / S11). Every field is a pointer so
// an omitted key is distinguishable from a zero value and only present fields
// are stored. entry_mode is restricted to manual_admit | open at this boundary
// (password is rejected — a scheduled event cannot pre-set a join password).
type eventCallSettingsRequest struct {
	EntryMode        *entity.EntryMode              `json:"entry_mode"`
	MuteOnJoin       *bool                          `json:"mute_on_join"`
	BreakoutCreation *entity.BreakoutCreationPolicy `json:"breakout_creation"`
	MaxBreakoutRooms *int                           `json:"max_breakout_rooms"`
}

type createEventRequest struct {
	CalendarID      string                    `json:"calendar_id"`
	ChannelID       *string                   `json:"channel_id"`
	Title           string                    `json:"title"`
	Description     *string                   `json:"description"`
	Location        eventLocationRequest      `json:"location"`
	ScheduledAt     time.Time                 `json:"scheduled_at"`
	OriginatorTZ    string                    `json:"originator_tz"`
	DurationMinutes int                       `json:"duration_minutes"`
	AllDay          bool                      `json:"all_day"`
	Recurrence      *recurrenceRequest        `json:"recurrence"`
	AttendeeUserIDs []string                  `json:"attendee_user_ids"`
	AttendeeEmails  []string                  `json:"attendee_emails"`
	RequiredUserIDs []string                  `json:"required_user_ids"`
	Reminders       []entity.EventReminder    `json:"reminders"`
	Settings        *eventCallSettingsRequest `json:"settings"`
}

type rsvpRequest struct {
	Status entity.RsvpStatus `json:"status"`
}

type moveOccurrenceRequest struct {
	InstanceAt         time.Time                           `json:"instance_at"`
	Scope              calendarservice.MoveOccurrenceScope `json:"scope"`
	NewScheduledAt     time.Time                           `json:"new_scheduled_at"`
	NewDurationMinutes *int                                `json:"new_duration_minutes"`
	ExpectedUpdatedAt  *time.Time                          `json:"expected_updated_at"`
}

type moveOccurrenceResponse struct {
	Updated *entity.CalendarEvent `json:"updated"`
	Created *entity.CalendarEvent `json:"created"`
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
	resp := StartCallResponse{Call: callEntity}
	if h.callSvc != nil && h.callSvc.LiveKitConfigured() {
		info, tokenErr := h.callSvc.IssueLiveKitJoinInfo(r.Context(), callEntity, userID, "")
		if tokenErr != nil {
			writeErr(w, tokenErr)
			return
		}
		resp.LivekitURL = info.URL
		resp.AccessToken = info.AccessToken
		expires := info.ExpiresAt
		tokenExpires := info.TokenExpiresAt
		refreshAfter := info.RefreshAfter
		resp.ExpiresAt = &expires
		resp.TokenExpiresAt = &tokenExpires
		resp.RefreshAfter = &refreshAfter
	}
	writeOK(w, resp)
}

func (h *CalendarHandler) MoveOccurrence(w http.ResponseWriter, r *http.Request) {
	eventID, err := id.Parse(chi.URLParam(r, "eventID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var req moveOccurrenceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.InstanceAt.IsZero() {
		writeErr(w, cerrors.InvalidInput("instance_at is required"))
		return
	}
	if req.NewScheduledAt.IsZero() {
		writeErr(w, cerrors.InvalidInput("new_scheduled_at is required"))
		return
	}
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	result, err := h.svc.MoveEventOccurrence(r.Context(), wsID, eventID, userID, calendarservice.MoveOccurrenceInput{
		InstanceAt:         req.InstanceAt.UTC(),
		Scope:              req.Scope,
		NewScheduledAt:     req.NewScheduledAt.UTC(),
		NewDurationMinutes: req.NewDurationMinutes,
		ExpectedUpdatedAt:  req.ExpectedUpdatedAt,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, moveOccurrenceResponse{Updated: result.Updated, Created: result.Created})
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
	settings, err := eventCallSettingsFromRequest(req.Settings, req.Location.Type)
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
		OriginatorTZ:    req.OriginatorTZ,
		DurationMinutes: req.DurationMinutes,
		AllDay:          req.AllDay,
		Recurrence:      recurrenceFromRequest(req.Recurrence),
		AttendeeUserIDs: attendeeUserIDs,
		AttendeeEmails:  req.AttendeeEmails,
		RequiredUserIDs: requiredUserIDs,
		Reminders:       req.Reminders,
		Settings:        settings,
	}, nil
}

// eventCallSettingsFromRequest validates and maps the optional per-event
// call-settings preconfig (ALK-819 / S11) for the create path. It validates the
// field values FIRST and only then applies the non-meet drop, mirroring the
// update path (which validates before the service drops). This keeps the two
// paths symmetric: a malformed settings payload yields the same 400 whether the
// event is aloqa_meet or not, instead of being silently swallowed on create when
// the location happens to be non-meet (ALK-819 review).
func eventCallSettingsFromRequest(req *eventCallSettingsRequest, locationType entity.EventLocationType) (*entity.EventCallSettings, error) {
	if req == nil {
		return nil, nil
	}
	settings, err := validateEventCallSettingsRequest(req)
	if err != nil {
		return nil, err
	}
	if locationType != entity.EventLocationAloqaMeet {
		// Non-meet events cannot host a call; drop the (now-validated) preconfig so a
		// stale settings payload on a relocated event never persists.
		return nil, nil
	}
	return settings, nil
}

// validateEventCallSettingsRequest performs the location-independent field
// validation and maps the request to the entity. entry_mode is restricted to
// manual_admit | open (password is rejected at this boundary), breakout_creation
// must be host | everyone, and max_breakout_rooms must be 1..8. The aloqa_meet
// drop is applied by the caller (create) or by the service (update, which knows
// the effective location type after merging the incoming change).
func validateEventCallSettingsRequest(req *eventCallSettingsRequest) (*entity.EventCallSettings, error) {
	if req == nil {
		return nil, nil
	}
	if req.EntryMode != nil {
		switch *req.EntryMode {
		case entity.EntryModeManualAdmit, entity.EntryModeOpen:
		case entity.EntryModePassword:
			return nil, cerrors.InvalidInput("entry_mode password is not supported for scheduled events")
		default:
			return nil, cerrors.InvalidInput("invalid entry_mode")
		}
	}
	if req.BreakoutCreation != nil && !req.BreakoutCreation.Valid() {
		return nil, cerrors.InvalidInput("invalid breakout_creation")
	}
	if req.MaxBreakoutRooms != nil && (*req.MaxBreakoutRooms < 1 || *req.MaxBreakoutRooms > 8) {
		return nil, cerrors.InvalidInput("max_breakout_rooms must be between 1 and 8")
	}
	return &entity.EventCallSettings{
		EntryMode:        req.EntryMode,
		MuteOnJoin:       req.MuteOnJoin,
		BreakoutCreation: req.BreakoutCreation,
		MaxBreakoutRooms: req.MaxBreakoutRooms,
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
	if value, ok := raw["originator_tz"]; ok {
		var originatorTZ string
		if err := json.Unmarshal(value, &originatorTZ); err != nil {
			return input, cerrors.InvalidInput("invalid originator_tz")
		}
		input.OriginatorTZ = &originatorTZ
		input.OriginatorTZSet = true
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
	if value, ok := raw["settings"]; ok {
		// Key-presence tri-state: explicit JSON null clears the preconfig, a JSON
		// object replaces it (after boundary validation). The service applies the
		// aloqa_meet drop using the effective location type. (ALK-819 / S11.)
		input.SettingsSet = true
		if !isJSONNull(value) {
			var settingsReq eventCallSettingsRequest
			if err := json.Unmarshal(value, &settingsReq); err != nil {
				return input, cerrors.InvalidInput("invalid settings")
			}
			settings, err := validateEventCallSettingsRequest(&settingsReq)
			if err != nil {
				return input, err
			}
			input.Settings = settings
		}
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
