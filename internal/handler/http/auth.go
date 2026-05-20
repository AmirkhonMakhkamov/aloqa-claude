package http

import (
	"net"
	"net/http"
	"strings"
	"time"

	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/platform/uaparser"
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

type sessionListEntry struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id"`
	UserAgent    string    `json:"user_agent"`
	IP           string    `json:"ip,omitempty"`
	IsCurrent    bool      `json:"is_current"`
	DeviceLabel  string    `json:"device_label,omitempty"`
	BrowserLabel string    `json:"browser_label,omitempty"`
	OsLabel      string    `json:"os_label,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	LastActiveAt time.Time `json:"last_active_at"`
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

func (h *AuthHandler) ReactivateAndLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	ipAddress := extractClientIP(r)

	result, err := h.svc.ReactivateAndLogin(r.Context(), req.Email, req.Password, req.DeviceInfo, ipAddress)
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
	userID := middleware.UserIDFromContext(r.Context())
	sessionID := middleware.SessionIDFromContext(r.Context())

	// Distinguish "empty body — revoke current session" from "non-empty
	// malformed body — 400". We cannot rely on errors.Is(decodeJSON err,
	// io.EOF) because the shared decodeJSON helper catches io.EOF internally
	// and wraps it as cerrors.InvalidInput("request body is required") (see
	// internal/handler/http/response.go:62-78). Use ContentLength as the
	// discriminator: 0 means the client sent no body at all (the historical
	// "revoke current session" contract), and any non-zero body must decode
	// cleanly or 400.
	if r.ContentLength != 0 {
		var req logoutRequest
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		if req.SessionID != "" {
			sessionID = req.SessionID
		}
	}

	if sessionID == "" {
		writeErr(w, cerrors.InvalidInput("session_id is required"))
		return
	}

	if err := h.svc.LogoutSessionForUser(r.Context(), userID.String(), sessionID); err != nil {
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

func (h *AuthHandler) LogoutOthers(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	currentSessionID := middleware.SessionIDFromContext(r.Context())
	if currentSessionID == "" {
		// Server-side invariant violation — middleware always sets this for
		// authenticated requests. Surface as Internal, not InvalidInput.
		writeErr(w, cerrors.Internal("session id missing from context", nil))
		return
	}
	if err := h.svc.LogoutOthers(r.Context(), userID.String(), currentSessionID); err != nil {
		writeErr(w, err)
		return
	}
	writeNoContent(w)
}

func (h *AuthHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	currentSessionID := middleware.SessionIDFromContext(r.Context())

	sessions, err := h.svc.ListSessions(r.Context(), userID.String())
	if err != nil {
		writeErr(w, err)
		return
	}

	out := make([]sessionListEntry, 0, len(sessions))
	for _, s := range sessions {
		device, browser, os := uaparser.Parse(s.DeviceInfo)
		out = append(out, sessionListEntry{
			ID:           s.ID,
			UserID:       s.UserID,
			UserAgent:    s.DeviceInfo,
			IP:           s.IPAddress,
			IsCurrent:    s.ID == currentSessionID,
			DeviceLabel:  device,
			BrowserLabel: browser,
			OsLabel:      os,
			CreatedAt:    s.CreatedAt,
			LastActiveAt: s.LastActiveAt,
		})
	}
	writeOK(w, out)
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
