package http

import (
	"net/http"

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
	if err := decodeJSON(r, &req); err != nil {
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
