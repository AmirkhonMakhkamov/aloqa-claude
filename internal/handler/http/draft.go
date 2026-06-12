package http

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"aloqa/internal/middleware"
	"aloqa/internal/pkg/id"
	"aloqa/internal/service/draft"
)

// DraftHandler handles server-backed message draft endpoints.
type DraftHandler struct {
	svc *draft.Service
}

// NewDraftHandler creates a new DraftHandler.
func NewDraftHandler(svc *draft.Service) *DraftHandler {
	return &DraftHandler{svc: svc}
}

type upsertDraftRequest struct {
	ParentMessageID *string         `json:"parent_message_id,omitempty"`
	Content         json.RawMessage `json:"content"`
}

type draftListResponse struct {
	Drafts []draft.Draft `json:"drafts"`
}

// List returns all of the caller's drafts in a workspace (hydration).
func (h *DraftHandler) List(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	workspaceID, err := id.Parse(chi.URLParam(r, "workspaceID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	drafts, err := h.svc.List(r.Context(), workspaceID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, draftListResponse{Drafts: drafts})
}

// Upsert stores (or replaces) the caller's draft for a channel/thread.
func (h *DraftHandler) Upsert(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	workspaceID, err := id.Parse(chi.URLParam(r, "workspaceID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	channelID, err := id.Parse(chi.URLParam(r, "channelID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req upsertDraftRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	parentID, err := parseOptionalParentMessageID(req.ParentMessageID)
	if err != nil {
		writeErr(w, err)
		return
	}

	result, err := h.svc.Upsert(r.Context(), workspaceID, channelID, userID, parentID, req.Content)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, result)
}

// Delete removes the caller's draft for a channel/thread.
func (h *DraftHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	channelID, err := id.Parse(chi.URLParam(r, "channelID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var parentID *uuid.UUID
	if raw := r.URL.Query().Get("parent_message_id"); raw != "" {
		parsed, parseErr := id.Parse(raw)
		if parseErr != nil {
			writeErr(w, parseErr)
			return
		}
		parentID = &parsed
	}

	if err := h.svc.Delete(r.Context(), channelID, userID, parentID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func parseOptionalParentMessageID(raw *string) (*uuid.UUID, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	parsed, err := id.Parse(*raw)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
