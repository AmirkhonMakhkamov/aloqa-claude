package http

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/service/notification"
)

// NotificationHandler handles notification HTTP endpoints.
type NotificationHandler struct {
	svc *notification.Service
}

// NewNotificationHandler creates a new NotificationHandler.
func NewNotificationHandler(svc *notification.Service) *NotificationHandler {
	return &NotificationHandler{svc: svc}
}

type notificationTokenRequest struct {
	Platform        string                   `json:"platform"`
	Token           string                   `json:"token,omitempty"`
	WebSubscription *webPushSubscriptionBody `json:"web_subscription,omitempty"`
}

type webPushSubscriptionBody struct {
	Endpoint       string            `json:"endpoint"`
	ExpirationTime *int64            `json:"expirationTime,omitempty"`
	Keys           map[string]string `json:"keys,omitempty"`
}

func (h *NotificationHandler) List(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	workspaceID, err := id.Parse(chi.URLParam(r, "workspaceID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	notifications, err := h.svc.ListNotifications(r.Context(), userID, workspaceID, limit)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, notifications)
}

func (h *NotificationHandler) RegisterToken(w http.ResponseWriter, r *http.Request) {
	if err := h.handleNotificationToken(r); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *NotificationHandler) UnregisterToken(w http.ResponseWriter, r *http.Request) {
	if err := h.handleNotificationToken(r); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *NotificationHandler) handleNotificationToken(r *http.Request) error {
	_ = middleware.UserIDFromContext(r.Context())
	if _, err := id.Parse(chi.URLParam(r, "workspaceID")); err != nil {
		return err
	}

	var req notificationTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	switch req.Platform {
	case "web_push":
		if req.WebSubscription == nil || strings.TrimSpace(req.WebSubscription.Endpoint) == "" {
			return cerrors.InvalidInput("web_subscription.endpoint is required")
		}
	case "apns", "fcm":
		if strings.TrimSpace(req.Token) == "" {
			return cerrors.InvalidInput("token is required")
		}
	default:
		return cerrors.InvalidInput("unsupported notification token platform")
	}

	return nil
}

func (h *NotificationHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	notifID, err := id.Parse(chi.URLParam(r, "notificationID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	if err := h.svc.MarkRead(r.Context(), notifID, userID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *NotificationHandler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	workspaceID, err := id.Parse(chi.URLParam(r, "workspaceID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	if err := h.svc.MarkAllRead(r.Context(), userID, workspaceID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *NotificationHandler) CountUnread(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	workspaceID, err := id.Parse(chi.URLParam(r, "workspaceID"))
	if err != nil {
		writeErr(w, err)
		return
	}

	count, err := h.svc.CountUnread(r.Context(), userID, workspaceID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, map[string]int{"unread_count": count})
}
