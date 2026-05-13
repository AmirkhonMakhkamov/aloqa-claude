package http

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/service/call"
)

type CallHandler struct {
	svc *call.Service
}

func NewCallHandler(svc *call.Service) *CallHandler {
	return &CallHandler{svc: svc}
}

type startCallRequest struct {
	Type      entity.CallType     `json:"type"`
	Title     string              `json:"title"`
	ChannelID *string             `json:"channel_id,omitempty"`
	Settings  entity.CallSettings `json:"settings"`
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

	c, err := h.svc.StartCall(r.Context(), wsID, userID, req.Type, req.Title, channelID, req.Settings)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeCreated(w, c)
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

	participant, err := h.svc.JoinCall(r.Context(), workspaceID, callID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, participant)
}

func (h *CallHandler) Leave(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	if err := h.svc.LeaveCall(r.Context(), workspaceID, callID, userID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
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

func (h *CallHandler) MediaToken(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	token, err := h.svc.IssueMediaJoinToken(r.Context(), workspaceID, callID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, token)
}

func (h *CallHandler) MediaOffer(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var req call.MediaOfferInput
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	req.CallID = callID

	answer, err := h.svc.HandleMediaOffer(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, answer)
}

func (h *CallHandler) MediaICECandidate(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var req call.MediaICECandidateInput
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	req.CallID = callID

	if err := h.svc.AddMediaICECandidate(r.Context(), req); err != nil {
		writeErr(w, err)
		return
	}
	writeNoContent(w)
}

func (h *CallHandler) MediaICERestart(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var req call.MediaOfferInput
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	req.CallID = callID

	answer, err := h.svc.RestartMediaICE(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, answer)
}
