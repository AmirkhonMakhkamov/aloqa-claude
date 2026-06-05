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
	savedsvc "aloqa/internal/service/saved"
)

type SavedHandler struct {
	svc *savedsvc.Service
}

func NewSavedHandler(svc *savedsvc.Service) *SavedHandler {
	return &SavedHandler{svc: svc}
}

func (h *SavedHandler) ResolveWorkspaceChannel(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	ch, err := h.svc.ResolveWorkspaceChannel(r.Context(), userID, workspaceID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, ch)
}

func (h *SavedHandler) ResolveGlobalChannel(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	ch, err := h.svc.ResolveGlobalChannel(r.Context(), userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, ch)
}

type saveMessageRequest struct {
	MessageID   string `json:"message_id"`
	WorkspaceID string `json:"workspace_id"`
}

func (h *SavedHandler) SaveMessage(w http.ResponseWriter, r *http.Request) {
	var req saveMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	messageID, err := id.Parse(req.MessageID)
	if err != nil {
		writeErr(w, err)
		return
	}
	workspaceID, err := id.Parse(req.WorkspaceID)
	if err != nil {
		writeErr(w, err)
		return
	}
	userID := middleware.UserIDFromContext(r.Context())
	result, err := h.svc.SaveMessage(r.Context(), userID, messageID, workspaceID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, result)
}

func (h *SavedHandler) UnsaveMessage(w http.ResponseWriter, r *http.Request) {
	savedMsgID, err := id.Parse(chi.URLParam(r, "savedMsgID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	userID := middleware.UserIDFromContext(r.Context())
	if err := h.svc.UnsaveMessage(r.Context(), userID, savedMsgID); err != nil {
		writeErr(w, err)
		return
	}
	writeNoContent(w)
}

func (h *SavedHandler) HardDeleteMessage(w http.ResponseWriter, r *http.Request) {
	savedMsgID, err := id.Parse(chi.URLParam(r, "savedMsgID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	userID := middleware.UserIDFromContext(r.Context())
	if err := h.svc.HardDeleteMessage(r.Context(), userID, savedMsgID); err != nil {
		writeErr(w, err)
		return
	}
	writeNoContent(w)
}

func (h *SavedHandler) ListMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mode := entity.SavedMessagesMode(q.Get("mode"))
	var workspaceID *uuid.UUID
	if raw := q.Get("workspaceId"); raw != "" {
		parsed, err := id.Parse(raw)
		if err != nil {
			writeErr(w, err)
			return
		}
		workspaceID = &parsed
	}
	limit := 100
	if raw := q.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeErr(w, cerrors.InvalidInput("invalid limit"))
			return
		}
		limit = parsed
	}
	userID := middleware.UserIDFromContext(r.Context())
	page, err := h.svc.ListMessages(r.Context(), userID, mode, workspaceID, q.Get("cursor"), limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, page)
}
