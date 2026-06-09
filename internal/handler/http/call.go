package http

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/service/call"
	"aloqa/internal/service/guest"
)

// StartCallResponse carries the created call plus the LiveKit connection info
// the FE needs to join the SFU room.
type StartCallResponse struct {
	Call           *entity.Call `json:"call"`
	LivekitURL     string       `json:"livekit_url,omitempty"`
	AccessToken    string       `json:"access_token,omitempty"`
	ExpiresAt      *time.Time   `json:"expires_at,omitempty"`
	TokenExpiresAt *time.Time   `json:"token_expires_at,omitempty"`
	RefreshAfter   *time.Time   `json:"refresh_after,omitempty"`
}

// JoinCallResponse mirrors StartCallResponse for the join endpoint.
type JoinCallResponse struct {
	Participant    *entity.CallParticipant `json:"participant"`
	LivekitURL     string                  `json:"livekit_url,omitempty"`
	AccessToken    string                  `json:"access_token,omitempty"`
	ExpiresAt      *time.Time              `json:"expires_at,omitempty"`
	TokenExpiresAt *time.Time              `json:"token_expires_at,omitempty"`
	RefreshAfter   *time.Time              `json:"refresh_after,omitempty"`
}

// SessionUserResolver resolves the owning user of a session cookie value with a
// strictly read-only lookup (no token refresh / rotation). It lets the public
// resolve endpoint identify a signed-in member from the `aloqa_session` cookie
// when no Bearer token is present (Next.js server components forward cookies but
// no Authorization header). (unified guest link)
type SessionUserResolver interface {
	UserIDForSession(ctx context.Context, sessionID string) (uuid.UUID, error)
}

type CallHandler struct {
	svc             *call.Service
	guests          *guest.Service
	validator       middleware.TokenValidator // optional: enables optional-auth resolve
	sessionResolver SessionUserResolver       // optional: session-cookie fallback for resolve
}

func NewCallHandler(svc *call.Service, guests *guest.Service) *CallHandler {
	return &CallHandler{svc: svc, guests: guests}
}

// SetTokenValidator wires the optional token validator used by the public
// resolve endpoint to best-effort identify a signed-in visitor. When nil, the
// resolve endpoint treats every visitor as anonymous. (unified guest link)
func (h *CallHandler) SetTokenValidator(validator middleware.TokenValidator) {
	h.validator = validator
}

// SetSessionResolver wires the optional session-cookie resolver used by the
// public resolve endpoint when no Bearer token is present. When nil, the cookie
// fallback is skipped and the visitor stays anonymous. (unified guest link)
func (h *CallHandler) SetSessionResolver(resolver SessionUserResolver) {
	h.sessionResolver = resolver
}

// sessionCookieName is the cookie carrying the server-side session ID. It MUST
// match the frontend's AUTH_COOKIES.session so the unified /join/<token> server
// page can forward it to this public resolve endpoint. (unified guest link)
const sessionCookieName = "aloqa_session"

// guestCallLinkMaxUses caps redeems on a single call guest link. It is set high
// because the authoritative expiry for "reusable until the call ends" is the
// redeem-time call-ended check, not the use count (ALK-700).
const guestCallLinkMaxUses = 100

// guestCallLinkTTL is the backstop expiry for a call guest link.
const guestCallLinkTTL = 12 * time.Hour

type guestLinkResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CreateGuestLink mints a one-time-style guest link scoped to a specific call.
// Host/co-host only. Works for ANY call type — including channel-less group /
// meeting / 1:1 calls — because the grant is scoped to the call itself, not a
// channel: a redeeming guest reaches exactly that call (and its in-call chat)
// and nothing else in the workspace. (unified guest link)
func (h *CallHandler) CreateGuestLink(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	// AuthorizeGuestLink enforces the host/co-host + active-call gate; its return
	// value is not needed since the grant is call-scoped, not channel-scoped.
	if _, err := h.svc.AuthorizeGuestLink(r.Context(), workspaceID, callID, userID); err != nil {
		writeErr(w, err)
		return
	}

	invite, err := h.guests.CreateInvite(r.Context(), guest.CreateInviteInput{
		WorkspaceID: workspaceID,
		CreatedBy:   userID,
		CallID:      &callID,
		ChannelIDs:  nil, // call-scoped: no channel/workspace access
		MaxUses:     guestCallLinkMaxUses,
		TTL:         guestCallLinkTTL,
	})
	if err != nil {
		writeErr(w, err)
		return
	}

	writeCreated(w, guestLinkResponse{Token: invite.Token, ExpiresAt: invite.ExpiresAt})
}

// resolveInviteResponse is the public, read-only projection returned by the
// unified-link resolve endpoint. valid is false (HTTP 200) for an invalid /
// expired / used / revoked token so the FE renders a friendly state rather than
// handling a 4xx/5xx. is_workspace_member is only true when a valid member
// session is attached. (unified guest link)
type resolveInviteResponse struct {
	Valid             bool       `json:"valid"`
	WorkspaceID       *uuid.UUID `json:"workspace_id,omitempty"`
	CallID            *uuid.UUID `json:"call_id,omitempty"`
	CallTitle         string     `json:"call_title,omitempty"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	IsWorkspaceMember bool       `json:"is_workspace_member"`
}

// ResolveGuestLink resolves a guest invite token for the unified /join/<token>
// link. PUBLIC (no auth middleware): it reads an OPTIONAL bearer session
// best-effort to report is_workspace_member, but a missing or invalid token is
// fine (anonymous). An invalid / expired / used / revoked invite token returns
// 200 with {"valid": false}, never an error. (unified guest link)
func (h *CallHandler) ResolveGuestLink(w http.ResponseWriter, r *http.Request) {
	// The public route is mounted on deps.Calls != nil, but this handler needs the
	// guest service. CallHandler is legitimately built with a nil guest service in
	// several callers (NewCallHandler(svc, nil)); fail closed with valid:false
	// rather than risk a nil-deref panic on this public route. (unified guest link)
	if h.guests == nil {
		writeOK(w, resolveInviteResponse{Valid: false})
		return
	}

	token := chi.URLParam(r, "token")

	// Best-effort optional auth: a Bearer header is honoured first, and any parse
	// or validation failure is silently ignored (the visitor is anonymous).
	optionalUserID := uuid.Nil
	if h.validator != nil {
		if header := r.Header.Get("Authorization"); header != "" {
			if raw, ok := strings.CutPrefix(header, "Bearer "); ok && raw != "" {
				if userID, _, err := h.validator.ValidateToken(raw); err == nil {
					optionalUserID = userID
				}
			}
		}
	}

	// Session-cookie fallback: a Next.js server component forwards the visitor's
	// cookies (including `aloqa_session`) but holds no Authorization header, so
	// without this a logged-in member is wrongly treated as anonymous. The lookup
	// is strictly read-only (no refresh / rotation); any error stays anonymous.
	// (unified guest link)
	if optionalUserID == uuid.Nil && h.sessionResolver != nil {
		if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
			if userID, err := h.sessionResolver.UserIDForSession(r.Context(), cookie.Value); err == nil {
				optionalUserID = userID
			}
		}
	}

	result, err := h.guests.ResolveInvite(r.Context(), token, optionalUserID)
	if err != nil {
		writeErr(w, err)
		return
	}

	resp := resolveInviteResponse{
		Valid:             result.Valid,
		CallTitle:         result.CallTitle,
		IsWorkspaceMember: result.IsWorkspaceMember,
	}
	if result.Valid {
		workspaceID := result.WorkspaceID
		resp.WorkspaceID = &workspaceID
		resp.CallID = result.CallID
		expiresAt := result.ExpiresAt
		resp.ExpiresAt = &expiresAt
	}
	writeOK(w, resp)
}

// startCallSettings is the request projection of call settings: the persisted
// entity.CallSettings (feature toggles + entry_mode) plus the write-only join
// Password (plaintext, hashed server-side, never persisted in this struct or
// returned by the API). #4.
type startCallSettings struct {
	entity.CallSettings
	Password string `json:"password,omitempty"`
}

type startCallRequest struct {
	Type      entity.CallType   `json:"type"`
	Title     string            `json:"title"`
	ChannelID *string           `json:"channel_id,omitempty"`
	Settings  startCallSettings `json:"settings"`
}

type joinCallRequest struct {
	// Password is supplied only for password entry-mode calls; it is optional so
	// joins to manual_admit/open calls send no body. #4.
	Password string `json:"password,omitempty"`
}

func (h *CallHandler) Start(w http.ResponseWriter, r *http.Request) {
	var req startCallRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	var channelID *uuid.UUID
	if req.ChannelID != nil {
		parsed, err := id.Parse(*req.ChannelID)
		if err != nil {
			writeErr(w, err)
			return
		}
		channelID = &parsed
	}

	c, err := h.svc.StartCall(r.Context(), wsID, userID, req.Type, req.Title, channelID, req.Settings.CallSettings, req.Settings.Password)
	if err != nil {
		writeErr(w, err)
		return
	}

	resp := StartCallResponse{Call: c}
	if h.svc.LiveKitConfigured() {
		info, tokenErr := h.svc.IssueLiveKitJoinInfo(r.Context(), c, userID, "")
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
	writeCreated(w, resp)
}

func (h *CallHandler) Get(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	c, err := h.svc.GetCall(r.Context(), workspaceID, callID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, c)
}

func (h *CallHandler) ListActive(w http.ResponseWriter, r *http.Request) {
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	calls, err := h.svc.ListActiveCalls(r.Context(), wsID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, calls)
}

// ListActiveSummaries returns the enriched Live Now projection used by the
// Calls Home page (GET /calls/active). Response shape matches the FE Zod
// schema ActiveCallSummarySchema and is wrapped as {"calls": [...]} per
// the same envelope as /calls/recents.
func (h *CallHandler) ListActiveSummaries(w http.ResponseWriter, r *http.Request) {
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	summaries, err := h.svc.ListActiveSummaries(r.Context(), wsID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, map[string]any{"calls": summaries})
}

func (h *CallHandler) Recents(w http.ResponseWriter, r *http.Request) {
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeErr(w, cerrors.InvalidInput("invalid limit"))
			return
		}
		limit = parsed
	}
	result, err := h.svc.ListRecentCalls(r.Context(), wsID, userID, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, result)
}

func (h *CallHandler) Join(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	var body joinCallRequest
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}

	participant, err := h.svc.JoinCall(r.Context(), workspaceID, callID, userID, body.Password)
	if err != nil {
		writeErr(w, err)
		return
	}

	resp := JoinCallResponse{Participant: participant}
	if h.svc.LiveKitConfigured() && participant.Status == entity.ParticipantStatusConnected {
		c, callErr := h.svc.GetCall(r.Context(), workspaceID, callID, userID)
		if callErr != nil {
			writeErr(w, callErr)
			return
		} else if c != nil {
			info, tokenErr := h.svc.IssueLiveKitJoinInfo(r.Context(), c, userID, "")
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
	}
	writeOK(w, resp)
}

func (h *CallHandler) Leave(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	result, err := h.svc.LeaveCall(r.Context(), workspaceID, callID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, result)
}

func (h *CallHandler) End(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	if err := h.svc.EndCall(r.Context(), workspaceID, callID, userID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *CallHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	result, err := h.svc.CancelCall(r.Context(), workspaceID, callID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, result)
}

func (h *CallHandler) Participants(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	participants, err := h.svc.GetParticipants(r.Context(), workspaceID, callID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, participants)
}

// --- Waiting room ---

type admitRequest struct {
	UserID string `json:"user_id"`
}

func (h *CallHandler) Admit(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req admitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	targetUserID, err := id.Parse(req.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	if err := h.svc.AdmitParticipant(r.Context(), workspaceID, callID, userID, targetUserID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *CallHandler) AdmitAll(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	if err := h.svc.AdmitAll(r.Context(), workspaceID, callID, userID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *CallHandler) Reject(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req admitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	targetUserID, err := id.Parse(req.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	if err := h.svc.RejectParticipant(r.Context(), workspaceID, callID, userID, targetUserID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *CallHandler) ListWaiting(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	waiting, err := h.svc.ListWaiting(r.Context(), workspaceID, callID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, waiting)
}

type setQualityRequest struct {
	StreamID string `json:"stream_id"`
	Quality  string `json:"quality"` // "f" (high), "h" (medium), "q" (low)
}

func (h *CallHandler) SetQuality(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req setQualityRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	if err := h.svc.SetQuality(r.Context(), workspaceID, callID, userID, req.StreamID, req.Quality); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *CallHandler) ReportNetworkQuality(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req call.MediaQualityReportInput
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	decision, err := h.svc.ReportNetworkQuality(r.Context(), workspaceID, callID, userID, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, decision)
}

// updateMediaRequest uses pointers so the client can patch a single field
// (mic toggle, screen-share toggle, …) without zero-filling the others.
// When this struct held plain bools, sending `{"audio_muted": true}` decoded
// as `{AudioMuted: true, VideoMuted: false, ScreenSharing: false}` and the
// service overwrote all three columns — so muting your mic also unmuted your
// video and stopped any screen share. Missing fields now mean "leave as-is".
type updateMediaRequest struct {
	AudioMuted    *bool `json:"audio_muted,omitempty"`
	VideoMuted    *bool `json:"video_muted,omitempty"`
	ScreenSharing *bool `json:"screen_sharing,omitempty"`
}

type updateSettingsRequest struct {
	EntryMode              *string `json:"entry_mode,omitempty"`
	MuteOnJoin             *bool   `json:"mute_on_join,omitempty"`
	BreakoutRooms          *bool   `json:"breakout_rooms,omitempty"`
	BreakoutCreation       *string `json:"breakout_creation,omitempty"`
	MaxBreakoutRooms       *int    `json:"max_breakout_rooms,omitempty"`
	ScreenSharing          *bool   `json:"screen_sharing,omitempty"`
	Chat                   *bool   `json:"chat,omitempty"`
	MembersCanUnmuteMic    *bool   `json:"members_can_unmute_mic,omitempty"`
	MembersCanEnableCamera *bool   `json:"members_can_enable_camera,omitempty"`
}

func (h *CallHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req updateSettingsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	if err := h.svc.CanAccessCall(r.Context(), workspaceID, callID, userID); err != nil {
		writeErr(w, err)
		return
	}

	patch := call.CallSettingsPatch{
		MuteOnJoin:             req.MuteOnJoin,
		BreakoutRooms:          req.BreakoutRooms,
		MaxBreakoutRooms:       req.MaxBreakoutRooms,
		ScreenSharing:          req.ScreenSharing,
		Chat:                   req.Chat,
		MembersCanUnmuteMic:    req.MembersCanUnmuteMic,
		MembersCanEnableCamera: req.MembersCanEnableCamera,
	}
	if req.EntryMode != nil {
		mode := entity.EntryMode(*req.EntryMode)
		patch.EntryMode = &mode
	}
	if req.BreakoutCreation != nil {
		policy := entity.BreakoutCreationPolicy(*req.BreakoutCreation)
		patch.BreakoutCreation = &policy
	}

	updated, err := h.svc.UpdateCallSettings(r.Context(), callID, userID, patch)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, updated)
}

func (h *CallHandler) UpdateMedia(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req updateMediaRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	if err := h.svc.UpdateMedia(r.Context(), workspaceID, callID, userID, req.AudioMuted, req.VideoMuted, req.ScreenSharing); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

type updateParticipantRoleRequest struct {
	Role entity.CallRole `json:"role"`
}

type addParticipantsRequest struct {
	UserIDs []uuid.UUID `json:"user_ids"`
}

const maxInviteParticipants = 100

func (h *CallHandler) AddParticipants(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req addParticipantsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if len(req.UserIDs) == 0 {
		writeErr(w, cerrors.InvalidInput("user_ids is required"))
		return
	}
	if len(req.UserIDs) > maxInviteParticipants {
		writeErr(w, cerrors.InvalidInput("too many user_ids"))
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	result, err := h.svc.InviteParticipants(r.Context(), workspaceID, callID, userID, req.UserIDs)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, result)
}

func (h *CallHandler) UpdateParticipantRole(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	targetUserID, err := id.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req updateParticipantRoleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	if err := h.svc.UpdateParticipantRole(r.Context(), workspaceID, callID, userID, targetUserID, req.Role); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

type transferHostRequest struct {
	UserID string `json:"user_id"`
}

func (h *CallHandler) TransferHost(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req transferHostRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	targetUserID, err := id.Parse(req.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	if err := h.svc.TransferHost(r.Context(), workspaceID, callID, userID, targetUserID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *CallHandler) DeclineCall(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	if err := h.svc.DeclineCall(r.Context(), workspaceID, callID, userID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

// RemoveParticipant lets the host evict a participant from the call.
// DELETE /calls/{callID}/participants/{userID}
func (h *CallHandler) RemoveParticipant(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	targetID, err := id.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	if err := h.svc.RemoveParticipant(r.Context(), workspaceID, callID, userID, targetID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

// --- Screen-share controls (ALK-697) ---

// RequestScreenShare lets a connected non-viewer ask the host for permission.
// POST /calls/{callID}/share-requests
func (h *CallHandler) RequestScreenShare(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	if err := h.svc.RequestScreenShare(r.Context(), workspaceID, callID, userID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

type resolveShareRequest struct {
	Approved *bool `json:"approved"`
}

// ResolveShareRequest lets a host approve or deny a screen-share request.
// POST /calls/{callID}/share-requests/{requesterUserID}/resolve
func (h *CallHandler) ResolveShareRequest(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	requesterID, err := id.Parse(chi.URLParam(r, "requesterUserID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req resolveShareRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.Approved == nil {
		writeErr(w, cerrors.InvalidInput("approved is required"))
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	if err := h.svc.ResolveShareRequest(r.Context(), workspaceID, callID, userID, requesterID, *req.Approved); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

// RevokeScreenShare lets a host revoke a participant's screen-share grant.
// POST /calls/{callID}/participants/{userID}/revoke-screen-share
func (h *CallHandler) RevokeScreenShare(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	targetID, err := id.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	if err := h.svc.RevokeScreenShare(r.Context(), workspaceID, callID, userID, targetID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

type setFeaturedShareRequest struct {
	UserID *string `json:"user_id"`
}

// SetFeaturedShare lets a host feature one participant's screen-share for
// everyone (a null user_id clears the featured share).
// PUT /calls/{callID}/featured-share
func (h *CallHandler) SetFeaturedShare(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req setFeaturedShareRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	var target *uuid.UUID
	if req.UserID != nil {
		parsed, perr := id.Parse(*req.UserID)
		if perr != nil {
			writeErr(w, perr)
			return
		}
		target = &parsed
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	if err := h.svc.SetFeaturedShare(r.Context(), workspaceID, callID, userID, target); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *CallHandler) TurnCredentials(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	credentials, err := h.svc.IssueTurnCredentials(r.Context(), workspaceID, callID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, credentials)
}
