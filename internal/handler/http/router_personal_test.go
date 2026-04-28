package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	wshandler "aloqa/internal/handler/ws"
)

func TestPersonalRoutesExcludeAdminSurface(t *testing.T) {
	userID := uuid.New()
	workspaceID := uuid.New()
	router := NewRouter(RouterDeps{
		Auth:             &AuthHandler{},
		Account:          &AccountHandler{},
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
		Validator:        fakeTokenValidator{userID: userID},
		PersonalResolver: fakePersonalResolver{workspaceID: workspaceID},
	})

	adminReq := httptest.NewRequest(http.MethodGet, "/api/v1/personal/admin/members", nil)
	adminReq.Header.Set("Authorization", "Bearer test-token")
	adminRes := httptest.NewRecorder()
	router.ServeHTTP(adminRes, adminReq)
	if adminRes.Code != http.StatusNotFound {
		t.Fatalf("personal admin route status = %d, want 404", adminRes.Code)
	}

	notificationsReq := httptest.NewRequest(http.MethodDelete, "/api/v1/personal/notifications", nil)
	notificationsReq.Header.Set("Authorization", "Bearer test-token")
	notificationsRes := httptest.NewRecorder()
	router.ServeHTTP(notificationsRes, notificationsReq)
	if notificationsRes.Code != http.StatusMethodNotAllowed {
		t.Fatalf("personal notifications route status = %d, want 405", notificationsRes.Code)
	}
}

type fakeTokenValidator struct {
	userID uuid.UUID
}

func (v fakeTokenValidator) ValidateToken(string) (uuid.UUID, string, error) {
	return v.userID, "session-1", nil
}

type fakePersonalResolver struct {
	workspaceID uuid.UUID
}

func (r fakePersonalResolver) GetOrCreatePersonalWorkspaceID(context.Context, uuid.UUID) (uuid.UUID, error) {
	return r.workspaceID, nil
}
