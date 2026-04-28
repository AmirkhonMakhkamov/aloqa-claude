package http

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/pkg/validate"
	"aloqa/internal/service/guest"
)

const (
	inviteDefaultTTL = 7 * 24 * time.Hour
	inviteMinTTL     = 1 * time.Hour
	inviteMaxTTL     = 365 * 24 * time.Hour
	inviteMaxUses    = 10_000
)

// GuestHandler handles guest access HTTP endpoints.
type GuestHandler struct {
	svc *guest.Service
}

// NewGuestHandler creates a new GuestHandler.
func NewGuestHandler(svc *guest.Service) *GuestHandler {
	return &GuestHandler{svc: svc}
}

func (h *GuestHandler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := id.Parse(chi.URLParam(r, "workspaceID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	var req struct {
		Email      string   `json:"email"      validate:"omitempty,email,max=320"`
		ChannelIDs []string `json:"channel_ids" validate:"omitempty,max=64,dive,uuid"`
		MaxUses    int      `json:"max_uses"   validate:"gte=0"`
		TTLHours   int      `json:"ttl_hours"  validate:"gte=0"` // Expiry in hours, default 168 (7 days)
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := validate.Struct(&req); err != nil {
		writeErr(w, err)
		return
	}
	if req.MaxUses > inviteMaxUses {
		writeErr(w, cerrors.InvalidInput("max_uses exceeds limit"))
		return
	}

	var channelIDs []uuid.UUID
	for _, s := range req.ChannelIDs {
		chID, err := uuid.Parse(s)
		if err != nil {
			writeErr(w, cerrors.InvalidInput("invalid channel ID: "+s))
			return
		}
		channelIDs = append(channelIDs, chID)
	}

	ttl := inviteDefaultTTL
	if req.TTLHours > 0 {
		ttl = time.Duration(req.TTLHours) * time.Hour
	}
	if ttl < inviteMinTTL {
		ttl = inviteMinTTL
	}
	if ttl > inviteMaxTTL {
		ttl = inviteMaxTTL
	}

	invite, err := h.svc.CreateInvite(r.Context(), guest.CreateInviteInput{
		WorkspaceID: workspaceID,
		CreatedBy:   actorID,
		Email:       req.Email,
		ChannelIDs:  channelIDs,
		MaxUses:     req.MaxUses,
		TTL:         ttl,
	})
	if err != nil {
		writeErr(w, err)
		return
	}

	writeCreated(w, invite)
}

func (h *GuestHandler) RedeemInvite(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token == "" {
		writeErr(w, cerrors.InvalidInput("invite token is required"))
		return
	}

	var req struct {
		Email       string `json:"email"        validate:"omitempty,email,max=320"`
		DisplayName string `json:"display_name" validate:"omitempty,max=128"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if err := validate.Struct(&req); err != nil {
		writeErr(w, err)
		return
	}

	result, err := h.svc.RedeemInvite(r.Context(), guest.RedeemInviteInput{
		Token:       token,
		Email:       req.Email,
		DisplayName: req.DisplayName,
		DeviceInfo:  r.UserAgent(),
		IPAddress:   r.RemoteAddr,
	})
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, result)
}

func (h *GuestHandler) RevokeInvite(w http.ResponseWriter, r *http.Request) {
	inviteID, err := id.Parse(chi.URLParam(r, "inviteID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.RevokeInvite(r.Context(), inviteID, actorID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *GuestHandler) ListInvites(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := id.Parse(chi.URLParam(r, "workspaceID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	invites, err := h.svc.ListInvites(r.Context(), workspaceID, actorID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, invites)
}
