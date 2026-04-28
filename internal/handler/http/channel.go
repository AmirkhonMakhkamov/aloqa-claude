package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/id"
	"aloqa/internal/service/chat"
)

type ChannelHandler struct {
	svc *chat.Service
}

func NewChannelHandler(svc *chat.Service) *ChannelHandler {
	return &ChannelHandler{svc: svc}
}

type createChannelRequest struct {
	Name  string             `json:"name"`
	Topic string             `json:"topic"`
	Type  entity.ChannelType `json:"type"`
}

type updateChannelRequest struct {
	Name  string `json:"name"`
	Topic string `json:"topic"`
}

func (h *ChannelHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createChannelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	ch, err := h.svc.CreateChannel(r.Context(), wsID, userID, req.Name, req.Topic, req.Type)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeCreated(w, ch)
}

func (h *ChannelHandler) List(w http.ResponseWriter, r *http.Request) {
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	channels, err := h.svc.ListChannels(r.Context(), wsID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, channels)
}

func (h *ChannelHandler) Get(w http.ResponseWriter, r *http.Request) {
	channelID, err := id.Parse(chi.URLParam(r, "channelID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())

	ch, err := h.svc.GetChannel(r.Context(), channelID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, ch)
}

func (h *ChannelHandler) Update(w http.ResponseWriter, r *http.Request) {
	channelID, err := id.Parse(chi.URLParam(r, "channelID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req updateChannelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	ch, err := h.svc.UpdateChannel(r.Context(), channelID, userID, req.Name, req.Topic)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, ch)
}

func (h *ChannelHandler) Join(w http.ResponseWriter, r *http.Request) {
	channelID, err := id.Parse(chi.URLParam(r, "channelID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.JoinChannel(r.Context(), channelID, userID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *ChannelHandler) Leave(w http.ResponseWriter, r *http.Request) {
	channelID, err := id.Parse(chi.URLParam(r, "channelID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.LeaveChannel(r.Context(), channelID, userID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *ChannelHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	channelID, err := id.Parse(chi.URLParam(r, "channelID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.MarkRead(r.Context(), channelID, userID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *ChannelHandler) UnreadCounts(w http.ResponseWriter, r *http.Request) {
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	counts, err := h.svc.GetUnreadCounts(r.Context(), wsID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, counts)
}

type createDMRequest struct {
	UserID            string  `json:"user_id"`
	TargetWorkspaceID *string `json:"target_workspace_id,omitempty"`
}

func (h *ChannelHandler) CreateDM(w http.ResponseWriter, r *http.Request) {
	var req createDMRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	targetID, err := id.Parse(req.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}

	var targetWorkspaceID *uuid.UUID
	if req.TargetWorkspaceID != nil {
		parsed, err := id.Parse(*req.TargetWorkspaceID)
		if err != nil {
			writeErr(w, err)
			return
		}
		targetWorkspaceID = &parsed
	}

	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	ch, err := h.svc.GetOrCreateDM(r.Context(), wsID, userID, targetID, targetWorkspaceID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, ch)
}
