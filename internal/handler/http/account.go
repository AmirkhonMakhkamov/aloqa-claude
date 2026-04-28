package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"aloqa/internal/middleware"
	"aloqa/internal/pkg/id"
	"aloqa/internal/service/auth"
)

type AccountHandler struct {
	svc *auth.Service
}

func NewAccountHandler(svc *auth.Service) *AccountHandler {
	return &AccountHandler{svc: svc}
}

type updateProfileRequest struct {
	DisplayName *string `json:"display_name"`
	AvatarURL   *string `json:"avatar_url"`
	Locale      *string `json:"locale"`
}

type createWorkspaceRequest struct {
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	AvatarURL string `json:"avatar_url"`
}

func (h *AccountHandler) Me(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())

	user, err := h.svc.GetUser(r.Context(), userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, user)
}

func (h *AccountHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	var req updateProfileRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	user, err := h.svc.UpdateProfile(r.Context(), userID, auth.UpdateProfileInput{
		DisplayName: req.DisplayName,
		AvatarURL:   req.AvatarURL,
		Locale:      req.Locale,
	})
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, user)
}

func (h *AccountHandler) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())

	workspaces, err := h.svc.ListWorkspaces(r.Context(), userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, workspaces)
}

func (h *AccountHandler) CreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var req createWorkspaceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspace, err := h.svc.CreateWorkspace(r.Context(), userID, auth.CreateWorkspaceInput{
		Name:      req.Name,
		Slug:      req.Slug,
		AvatarURL: req.AvatarURL,
	})
	if err != nil {
		writeErr(w, err)
		return
	}

	writeCreated(w, workspace)
}

func (h *AccountHandler) GetWorkspace(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := id.Parse(chi.URLParam(r, "workspaceID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	workspace, err := h.svc.GetWorkspace(r.Context(), workspaceID, userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, workspace)
}

func (h *AccountHandler) GetPersonalWorkspace(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())

	workspace, err := h.svc.GetOrCreatePersonalWorkspace(r.Context(), userID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, workspace)
}
