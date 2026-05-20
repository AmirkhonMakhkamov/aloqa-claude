package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	wshandler "aloqa/internal/handler/ws"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
)

func TestAccountDeactivateRequiresAuth(t *testing.T) {
	env := newAuthHandlerTestEnv(t)
	router := NewRouter(RouterDeps{
		Auth:             env.handler,
		Account:          NewAccountHandler(env.svc),
		Channels:         &ChannelHandler{},
		Messages:         &MessageHandler{},
		Calls:            &CallHandler{},
		Breakout:         &BreakoutHandler{},
		Files:            &FileHandler{},
		Presence:         &PresenceHandler{},
		Recordings:       &RecordingHandler{},
		Notifications:    &NotificationHandler{},
		Search:           &SearchHandler{},
		Admin:            &AdminHandler{},
		Guests:           &GuestHandler{},
		WS:               &wshandler.Handler{},
		Validator:        fakeTokenValidator{userID: uuid.New()},
		PersonalResolver: fakePersonalResolver{workspaceID: uuid.New()},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/deactivate", nil)
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("Deactivate status = %d, want 401, body=%s", res.Code, res.Body.String())
	}
}

func TestAccountDeactivateRevokesSessionsAndSetsStatus(t *testing.T) {
	env := newAuthHandlerTestEnv(t)
	handler := NewAccountHandler(env.svc)
	userID := env.addUser(t)
	sessionID := env.createSession(t, userID, testSafariUA, "127.0.0.1")
	req := newAccountRequest(http.MethodPost, "/api/v1/users/me/deactivate", userID)
	res := httptest.NewRecorder()

	handler.Deactivate(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("Deactivate status = %d, want 204, body=%s", res.Code, res.Body.String())
	}
	user := env.users.users[userID]
	if user.Status != entity.UserStatusDeactivated {
		t.Fatalf("Status = %q, want deactivated", user.Status)
	}
	if user.DeactivatedAt == nil {
		t.Fatalf("DeactivatedAt = nil, want timestamp")
	}
	assertSessionListed(t, env.svc, userID, sessionID, false)
}

func TestAccountDeactivateIdempotent(t *testing.T) {
	env := newAuthHandlerTestEnv(t)
	handler := NewAccountHandler(env.svc)
	userID := env.addUser(t)

	for i := 0; i < 2; i++ {
		req := newAccountRequest(http.MethodPost, "/api/v1/users/me/deactivate", userID)
		res := httptest.NewRecorder()
		handler.Deactivate(res, req)
		if res.Code != http.StatusNoContent {
			t.Fatalf("Deactivate attempt %d status = %d, want 204, body=%s", i+1, res.Code, res.Body.String())
		}
	}
	if env.users.users[userID].Status != entity.UserStatusDeactivated {
		t.Fatalf("Status = %q, want deactivated", env.users.users[userID].Status)
	}
}

func TestAccountDeactivateRejectsSuspended(t *testing.T) {
	env := newAuthHandlerTestEnv(t)
	handler := NewAccountHandler(env.svc)
	userID := env.addUser(t)
	env.users.users[userID].Status = entity.UserStatusSuspended
	req := newAccountRequest(http.MethodPost, "/api/v1/users/me/deactivate", userID)
	res := httptest.NewRecorder()

	handler.Deactivate(res, req)

	if res.Code != http.StatusForbidden {
		t.Fatalf("Deactivate status = %d, want 403, body=%s", res.Code, res.Body.String())
	}
	assertErrorCode(t, res, cerrors.CodeForbidden)
	if env.users.users[userID].Status != entity.UserStatusSuspended {
		t.Fatalf("Status = %q, want suspended", env.users.users[userID].Status)
	}
}

func TestAccountDeactivateRevokeFailureDoesNotFlipStatus(t *testing.T) {
	env := newAuthHandlerTestEnv(t)
	handler := NewAccountHandler(env.svc)
	userID := env.addUser(t)
	env.svc.SetSessionOperationTimeout(20 * time.Millisecond)
	env.redis.Close()
	req := newAccountRequest(http.MethodPost, "/api/v1/users/me/deactivate", userID)
	res := httptest.NewRecorder()

	handler.Deactivate(res, req)

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("Deactivate status = %d, want 500, body=%s", res.Code, res.Body.String())
	}
	user := env.users.users[userID]
	if user.Status != entity.UserStatusActive {
		t.Fatalf("Status = %q, want active after revoke failure", user.Status)
	}
	if user.DeactivatedAt != nil {
		t.Fatalf("DeactivatedAt = %v, want nil after revoke failure", user.DeactivatedAt)
	}
}

func newAccountRequest(method, path string, userID uuid.UUID) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	ctx := context.WithValue(req.Context(), middleware.UserIDKey, userID)
	return req.WithContext(ctx)
}
