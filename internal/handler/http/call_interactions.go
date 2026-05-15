package http

import (
	"bytes"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
)

type callReactionRequest struct {
	Emoji string `json:"emoji"`
}

func (h *CallHandler) RaiseHand(w http.ResponseWriter, r *http.Request) {
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, cerrors.InvalidInput("invalid call id"))
		return
	}
	userID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.RaiseHand(r.Context(), workspaceID, callID, userID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CallHandler) LowerHand(w http.ResponseWriter, r *http.Request) {
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, cerrors.InvalidInput("invalid call id"))
		return
	}
	userID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.LowerHand(r.Context(), workspaceID, callID, userID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CallHandler) SendCallReaction(w http.ResponseWriter, r *http.Request) {
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, cerrors.InvalidInput("invalid call id"))
		return
	}

	var req callReactionRequest
	if err := decodeCallReactionRequest(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	if err := h.svc.SendCallReaction(r.Context(), workspaceID, callID, userID, req.Emoji); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeCallReactionRequest(r *http.Request, req *callReactionRequest) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return cerrors.InvalidInput("invalid request body")
	}
	if !utf8.Valid(body) {
		return cerrors.InvalidInput("invalid request body")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return decodeJSON(r, req)
}
