package http

import (
	"net"
	"net/http"
	"strings"

	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/service/auth"
)

type AuthHandler struct {
	svc *auth.Service
}

func NewAuthHandler(svc *auth.Service) *AuthHandler {
	return &AuthHandler{svc: svc}
}

type registerRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

type loginRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	DeviceInfo string `json:"device_info"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	SessionID    string `json:"session_id"`
	ExpiresIn    int    `json:"expires_in"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type logoutRequest struct {
	SessionID string `json:"session_id"`
}

func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	user, err := h.svc.Register(r.Context(), req.Email, req.Password, req.DisplayName)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeCreated(w, user)
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	// Extract client IP from the request, preferring the first valid IP in
	// X-Forwarded-For (set by a trusted reverse proxy) over RemoteAddr.
	ipAddress := extractClientIP(r)

	result, err := h.svc.Login(r.Context(), req.Email, req.Password, req.DeviceInfo, ipAddress)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, tokenResponse{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		SessionID:    result.SessionID,
		ExpiresIn:    result.ExpiresIn,
	})
}

func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	result, err := h.svc.RefreshToken(r.Context(), req.RefreshToken)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, tokenResponse{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		SessionID:    result.SessionID,
		ExpiresIn:    result.ExpiresIn,
	})
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	// Get session ID from the JWT context (the session the user is currently using).
	sessionID := middleware.SessionIDFromContext(r.Context())

	// Allow overriding with an explicit session_id in body (to revoke a different session).
	var req logoutRequest
	if err := decodeJSON(r, &req); err == nil && req.SessionID != "" {
		sessionID = req.SessionID
	}

	if sessionID == "" {
		writeErr(w, cerrors.InvalidInput("session_id is required"))
		return
	}

	if err := h.svc.Logout(r.Context(), sessionID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *AuthHandler) LogoutAll(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.LogoutAll(r.Context(), userID.String()); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *AuthHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())

	sessions, err := h.svc.ListSessions(r.Context(), userID.String())
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, sessions)
}

// extractClientIP returns the first valid IP from X-Forwarded-For, falling back
// to RemoteAddr. This prevents log injection and header spoofing by parsing and
// validating each candidate before use.
func extractClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for _, raw := range strings.Split(xff, ",") {
			candidate := strings.TrimSpace(raw)
			if ip := net.ParseIP(candidate); ip != nil {
				return ip.String()
			}
		}
	}
	// Strip port from RemoteAddr (e.g. "192.168.1.1:12345").
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

