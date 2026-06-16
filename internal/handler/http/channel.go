package http

import (
	"net/http"
	"strconv"

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
	Name    string             `json:"name"`
	Members []uuid.UUID        `json:"members,omitempty"`
	Topic   string             `json:"topic"`
	Type    entity.ChannelType `json:"type"`
}

type updateChannelRequest struct {
	Name     string `json:"name"`
	Topic    string `json:"topic"`
	Archived *bool  `json:"archived,omitempty"`
}

type addChannelMembersRequest struct {
	UserIDs []uuid.UUID `json:"user_ids"`
}

func (h *ChannelHandler) Directory(w http.ResponseWriter, r *http.Request) {
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	directory, err := h.svc.ListDirectory(r.Context(), wsID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, directory)
}

func (h *ChannelHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createChannelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	ch, err := h.svc.CreateChannel(r.Context(), wsID, userID, req.Name, req.Topic, req.Type, req.Members)
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

// ListArchived returns the workspace's archived channels for the caller,
// enriched with member count, last-activity timestamp and archived-at
// timestamp so the Archived Channels list view (ALK-617) can render rows
// without per-row round-trips.
func (h *ChannelHandler) ListArchived(w http.ResponseWriter, r *http.Request) {
	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	infos, err := h.svc.ListArchivedChannels(r.Context(), wsID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, infos)
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

func (h *ChannelHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	channelID, err := id.Parse(chi.URLParam(r, "channelID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	members, err := h.svc.ListChannelMembers(r.Context(), channelID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, members)
}

func (h *ChannelHandler) ListMentions(w http.ResponseWriter, r *http.Request) {
	channelID, err := id.Parse(chi.URLParam(r, "channelID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	query := r.URL.Query().Get("query")
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, convErr := strconv.Atoi(raw); convErr == nil {
			limit = parsed
		}
	}

	suggestions, err := h.svc.SearchChannelMentions(r.Context(), channelID, userID, query, limit)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, suggestions)
}

func (h *ChannelHandler) AddMembers(w http.ResponseWriter, r *http.Request) {
	channelID, err := id.Parse(chi.URLParam(r, "channelID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req addChannelMembersRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	members, err := h.svc.AddChannelMembers(r.Context(), channelID, userID, req.UserIDs)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, members)
}

func (h *ChannelHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	channelID, err := id.Parse(chi.URLParam(r, "channelID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	targetUserID, err := id.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	if err := h.svc.RemoveChannelMember(r.Context(), channelID, userID, targetUserID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
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
	ch, err := h.svc.UpdateChannel(r.Context(), channelID, userID, req.Name, req.Topic, req.Archived)
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
	// UserID is the legacy single-target form used by seed.sh and the Go SDK.
	UserID string `json:"user_id"`
	// UserIDs matches the OpenAPI contract that the web client was generated
	// from. For now the backend only supports 1-on-1 DMs, so we accept the
	// array form for compatibility and use its first element. Group DMs are
	// a separate feature.
	UserIDs           []string `json:"user_ids"`
	TargetWorkspaceID *string  `json:"target_workspace_id,omitempty"`
}

func (h *ChannelHandler) CreateDM(w http.ResponseWriter, r *http.Request) {
	var req createDMRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	rawTarget := req.UserID
	if rawTarget == "" && len(req.UserIDs) > 0 {
		rawTarget = req.UserIDs[0]
	}
	targetID, err := id.Parse(rawTarget)
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

func (h *ChannelHandler) AcceptDMRequest(w http.ResponseWriter, r *http.Request) {
	channelID, err := id.Parse(chi.URLParam(r, "channelID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	ch, err := h.svc.AcceptDMRequest(r.Context(), wsID, channelID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, ch)
}

func (h *ChannelHandler) BlockDMRequest(w http.ResponseWriter, r *http.Request) {
	channelID, err := id.Parse(chi.URLParam(r, "channelID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	wsID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	ch, err := h.svc.BlockDMRequest(r.Context(), wsID, channelID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, ch)
}
