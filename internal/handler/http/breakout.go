package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"aloqa/internal/middleware"
	"aloqa/internal/pkg/id"
	"aloqa/internal/service/call"
)

// BreakoutHandler handles breakout room HTTP endpoints.
type BreakoutHandler struct {
	svc *call.Service
}

// NewBreakoutHandler creates a new BreakoutHandler.
func NewBreakoutHandler(svc *call.Service) *BreakoutHandler {
	return &BreakoutHandler{svc: svc}
}

type createBreakoutRoomsRequest struct {
	Rooms []call.CreateBreakoutRoomInput `json:"rooms"`
}

func (h *BreakoutHandler) Create(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req createBreakoutRoomsRequest
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

	rooms, err := h.svc.CreateBreakoutRooms(r.Context(), callID, userID, req.Rooms)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeCreated(w, rooms)
}

func (h *BreakoutHandler) List(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	if err := h.svc.CanAccessCall(r.Context(), workspaceID, callID, userID); err != nil {
		writeErr(w, err)
		return
	}

	rooms, err := h.svc.ListBreakoutRooms(r.Context(), callID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, rooms)
}

func (h *BreakoutHandler) Join(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	roomID, err := id.Parse(chi.URLParam(r, "breakoutRoomID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	if err := h.svc.CanAccessCall(r.Context(), workspaceID, callID, userID); err != nil {
		writeErr(w, err)
		return
	}

	if err := h.svc.JoinBreakoutRoom(r.Context(), callID, userID, roomID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *BreakoutHandler) Return(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	if err := h.svc.CanAccessCall(r.Context(), workspaceID, callID, userID); err != nil {
		writeErr(w, err)
		return
	}

	if err := h.svc.ReturnToMainRoom(r.Context(), callID, userID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *BreakoutHandler) Close(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	roomID, err := id.Parse(chi.URLParam(r, "breakoutRoomID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	if err := h.svc.CanAccessCall(r.Context(), workspaceID, callID, userID); err != nil {
		writeErr(w, err)
		return
	}

	if err := h.svc.CloseBreakoutRoom(r.Context(), callID, userID, roomID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *BreakoutHandler) CloseAll(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	if err := h.svc.CanAccessCall(r.Context(), workspaceID, callID, userID); err != nil {
		writeErr(w, err)
		return
	}

	if err := h.svc.CloseAllBreakoutRooms(r.Context(), callID, userID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

type broadcastRequest struct {
	Message string `json:"message"`
}

func (h *BreakoutHandler) Broadcast(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req broadcastRequest
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

	if err := h.svc.BroadcastToBreakoutRooms(r.Context(), callID, userID, req.Message); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *BreakoutHandler) Participants(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	roomID, err := id.Parse(chi.URLParam(r, "breakoutRoomID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	if err := h.svc.CanAccessCall(r.Context(), workspaceID, callID, userID); err != nil {
		writeErr(w, err)
		return
	}

	participants, err := h.svc.ListBreakoutRoomParticipants(r.Context(), callID, roomID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, participants)
}
