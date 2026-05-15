package http

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"aloqa/internal/domain/entity"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/pkg/pagination"
)

type sendCallMessageRequest struct {
	Body string `json:"body"`
}

func (h *CallHandler) SendCallMessage(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	var req sendCallMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	req.Body = strings.TrimSpace(req.Body)

	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	msg, err := h.svc.SendCallMessage(r.Context(), workspaceID, callID, userID, req.Body)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeCreated(w, msg)
}

func (h *CallHandler) ListCallMessages(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	params, err := parseCallMessagePagination(r)
	if err != nil {
		writeErr(w, err)
		return
	}

	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	items, err := h.svc.ListCallMessages(r.Context(), workspaceID, callID, userID, params)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, buildCallMessagePage(items, params.Limit))
}

func (h *CallHandler) DeleteCallMessage(w http.ResponseWriter, r *http.Request) {
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	messageID, err := id.Parse(chi.URLParam(r, "messageID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.DeleteCallMessage(r.Context(), workspaceID, callID, userID, messageID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func parseCallMessagePagination(r *http.Request) (pagination.Params, error) {
	params := pagination.Params{}

	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		parsed, err := pagination.DecodeCursor(cursor)
		if err != nil {
			return pagination.Params{}, cerrors.InvalidInput("invalid cursor")
		}
		params.Cursor = parsed
	}

	if limit := r.URL.Query().Get("limit"); limit != "" {
		parsed, err := strconv.Atoi(limit)
		if err != nil {
			return pagination.Params{}, cerrors.InvalidInput("invalid limit")
		}
		params.Limit = parsed
	}

	params.Normalize()
	return params, nil
}

func buildCallMessagePage(items []entity.CallMessage, limit int) pagination.Page[entity.CallMessage] {
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}

	var nextCursor string
	if hasMore && len(items) > 0 {
		nextCursor = pagination.EncodeCursor(items[len(items)-1].ID)
	}

	return pagination.Page[entity.CallMessage]{
		Items:      items,
		NextCursor: nextCursor,
		HasMore:    hasMore,
	}
}
