package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	wshandler "aloqa/internal/handler/ws"
)

func TestNotificationTokenRoutes(t *testing.T) {
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

	for _, tc := range []struct {
		name   string
		path   string
		body   string
		status int
	}{
		{
			name:   "register web push",
			path:   "/register-token",
			body:   `{"platform":"web_push","web_subscription":{"endpoint":"https://push.example.test/subscription","expirationTime":null,"keys":{"p256dh":"key","auth":"auth"}}}`,
			status: http.StatusNoContent,
		},
		{
			name:   "register fcm",
			path:   "/register-token",
			body:   `{"platform":"fcm","token":"fcm-token"}`,
			status: http.StatusNoContent,
		},
		{
			name:   "unregister apns",
			path:   "/unregister-token",
			body:   `{"platform":"apns","token":"apns-token"}`,
			status: http.StatusNoContent,
		},
		{
			name:   "missing native token",
			path:   "/register-token",
			body:   `{"platform":"fcm"}`,
			status: http.StatusBadRequest,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+workspaceID.String()+"/notifications"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer test-token")
			req.Header.Set("Content-Type", "application/json")
			res := httptest.NewRecorder()

			router.ServeHTTP(res, req)

			if res.Code != tc.status {
				t.Fatalf("status = %d, want %d, body=%s", res.Code, tc.status, res.Body.String())
			}
		})
	}
}
