package http

import (
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/service/search"
)

// SearchHandler handles search HTTP endpoints.
type SearchHandler struct {
	svc *search.Service
}

// NewSearchHandler creates a new SearchHandler.
func NewSearchHandler(svc *search.Service) *SearchHandler {
	return &SearchHandler{svc: svc}
}

func (h *SearchHandler) Search(w http.ResponseWriter, r *http.Request) {
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())

	q := r.URL.Query().Get("q")
	if q == "" {
		writeErr(w, cerrors.InvalidInput("query parameter 'q' is required"))
		return
	}

	params := search.Params{
		Query:       q,
		WorkspaceID: workspaceID,
		Type:        r.URL.Query().Get("type"),
		Limit:       20,
		Offset:      0,
	}
	userID := middleware.UserIDFromContext(r.Context())
	params.UserID = &userID

	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			params.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			params.Offset = n
		}
	}
	if v := r.URL.Query().Get("channel_id"); v != "" {
		if chID, err := uuid.Parse(v); err == nil {
			params.ChannelID = &chID
		}
	}
	if v := r.URL.Query().Get("date_from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			params.DateFrom = &t
		}
	}
	if v := r.URL.Query().Get("date_to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			params.DateTo = &t
		}
	}

	results, err := h.svc.Search(r.Context(), params)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, results)
}
