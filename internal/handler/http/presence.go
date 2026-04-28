package http

import (
	"net/http"

	"aloqa/internal/middleware"
	cerrors "aloqa/internal/pkg/cerrors"
	"aloqa/internal/service/presence"
)

// PresenceHandler handles presence-related HTTP endpoints.
type PresenceHandler struct {
	svc *presence.Service
}

// NewPresenceHandler creates a new PresenceHandler.
func NewPresenceHandler(svc *presence.Service) *PresenceHandler {
	return &PresenceHandler{svc: svc}
}

type setStatusRequest struct {
	Status       string `json:"status"`
	CustomStatus string `json:"custom_status,omitempty"`
	CustomEmoji  string `json:"custom_emoji,omitempty"`
}

// SetStatus updates the authenticated user's presence status.
func (h *PresenceHandler) SetStatus(w http.ResponseWriter, r *http.Request) {
	var req setStatusRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	sessionID := middleware.SessionIDFromContext(r.Context())

	switch presence.Status(req.Status) {
	case presence.StatusOnline:
		if err := h.svc.SetOnline(r.Context(), userID, wsID, sessionID); err != nil {
			writeErr(w, err)
			return
		}
	case presence.StatusAway:
		if err := h.svc.SetAway(r.Context(), userID, wsID, sessionID); err != nil {
			writeErr(w, err)
			return
		}
	case presence.StatusDND:
		if err := h.svc.SetDND(r.Context(), userID, wsID, sessionID); err != nil {
			writeErr(w, err)
			return
		}
	case presence.StatusOffline:
		if req.CustomStatus != "" || req.CustomEmoji != "" {
			writeErr(w, cerrors.InvalidInput("custom status cannot be set while offline"))
			return
		}
		if err := h.svc.SetOffline(r.Context(), userID, wsID, sessionID); err != nil {
			writeErr(w, err)
			return
		}
	default:
		writeErr(w, cerrors.InvalidInput("invalid status, must be one of: online, away, dnd, offline"))
		return
	}

	if req.CustomStatus != "" || req.CustomEmoji != "" {
		if err := h.svc.SetCustomStatus(r.Context(), userID, wsID, sessionID, req.CustomStatus, req.CustomEmoji); err != nil {
			writeErr(w, err)
			return
		}
	}

	writeNoContent(w)
}

// ListOnline returns all online users in the workspace.
func (h *PresenceHandler) ListOnline(w http.ResponseWriter, r *http.Request) {
	wsID := middleware.WorkspaceIDFromContext(r.Context())

	result, err := h.svc.GetWorkspacePresence(r.Context(), wsID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, result)
}
