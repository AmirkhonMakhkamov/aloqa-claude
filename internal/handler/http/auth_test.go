package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"aloqa/internal/domain/entity"
	wshandler "aloqa/internal/handler/ws"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/service/auth"
)

const testSafariUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"

func TestLoginStatusCodesAfterPasswordVerification(t *testing.T) {
	env := newAuthHandlerTestEnv(t)
	password := "Password1!"
	activeID := env.addCredentialedUser(t, "active@example.com", password, entity.UserStatusActive)
	deactivatedID := env.addCredentialedUser(t, "deactivated@example.com", password, entity.UserStatusDeactivated)
	suspendedID := env.addCredentialedUser(t, "suspended@example.com", password, entity.UserStatusSuspended)

	tests := []struct {
		name     string
		email    string
		password string
		status   int
		code     cerrors.Code
	}{
		{
			name:     "deactivated wrong password masks status",
			email:    "deactivated@example.com",
			password: "wrong",
			status:   http.StatusUnauthorized,
			code:     cerrors.CodeUnauthorized,
		},
		{
			name:     "suspended wrong password masks status",
			email:    "suspended@example.com",
			password: "wrong",
			status:   http.StatusUnauthorized,
			code:     cerrors.CodeUnauthorized,
		},
		{
			name:     "deactivated correct password is typed",
			email:    "deactivated@example.com",
			password: password,
			status:   http.StatusForbidden,
			code:     cerrors.CodeAccountDeactivated,
		},
		{
			name:     "suspended correct password is typed",
			email:    "suspended@example.com",
			password: password,
			status:   http.StatusForbidden,
			code:     cerrors.CodeAccountSuspended,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(loginBody(tt.email, tt.password)))
			res := httptest.NewRecorder()

			env.handler.Login(res, req)

			if res.Code != tt.status {
				t.Fatalf("Login status = %d, want %d, body=%s", res.Code, tt.status, res.Body.String())
			}
			assertErrorCode(t, res, tt.code)
		})
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(loginBody("active@example.com", password)))
	res := httptest.NewRecorder()
	env.handler.Login(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("active Login status = %d, want 200, body=%s", res.Code, res.Body.String())
	}
	if env.users.users[activeID].Status != entity.UserStatusActive {
		t.Fatalf("active user status changed")
	}
	if env.users.users[deactivatedID].Status != entity.UserStatusDeactivated {
		t.Fatalf("deactivated user status changed")
	}
	if env.users.users[suspendedID].Status != entity.UserStatusSuspended {
		t.Fatalf("suspended user status changed")
	}
}

func TestReactivateAndLoginStatusCodes(t *testing.T) {
	env := newAuthHandlerTestEnv(t)
	password := "Password1!"
	deactivatedID := env.addCredentialedUser(t, "deactivated@example.com", password, entity.UserStatusDeactivated)
	now := time.Now().UTC()
	env.users.users[deactivatedID].DeactivatedAt = &now
	env.addCredentialedUser(t, "active@example.com", password, entity.UserStatusActive)
	env.addCredentialedUser(t, "suspended@example.com", password, entity.UserStatusSuspended)

	tests := []struct {
		name     string
		email    string
		password string
		status   int
		code     cerrors.Code
	}{
		{
			name:     "wrong password masks deactivated status",
			email:    "deactivated@example.com",
			password: "wrong",
			status:   http.StatusUnauthorized,
			code:     cerrors.CodeUnauthorized,
		},
		{
			name:     "active correct password is not deactivated",
			email:    "active@example.com",
			password: password,
			status:   http.StatusForbidden,
			code:     cerrors.CodeNotDeactivated,
		},
		{
			name:     "suspended correct password is typed",
			email:    "suspended@example.com",
			password: password,
			status:   http.StatusForbidden,
			code:     cerrors.CodeAccountSuspended,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/reactivate-and-login", strings.NewReader(loginBody(tt.email, tt.password)))
			res := httptest.NewRecorder()

			env.handler.ReactivateAndLogin(res, req)

			if res.Code != tt.status {
				t.Fatalf("ReactivateAndLogin status = %d, want %d, body=%s", res.Code, tt.status, res.Body.String())
			}
			assertErrorCode(t, res, tt.code)
		})
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/reactivate-and-login", strings.NewReader(loginBody("deactivated@example.com", password)))
	res := httptest.NewRecorder()
	env.handler.ReactivateAndLogin(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("ReactivateAndLogin status = %d, want 200, body=%s", res.Code, res.Body.String())
	}
	var tokens tokenResponse
	if err := json.Unmarshal(res.Body.Bytes(), &tokens); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.SessionID == "" || tokens.ExpiresIn == 0 {
		t.Fatalf("token response missing fields: %+v", tokens)
	}
	user := env.users.users[deactivatedID]
	if user.Status != entity.UserStatusActive {
		t.Fatalf("Status = %q, want active", user.Status)
	}
	if user.DeactivatedAt != nil {
		t.Fatalf("DeactivatedAt = %v, want nil", user.DeactivatedAt)
	}
}

func TestLogoutRevokesOwnSession(t *testing.T) {
	env := newAuthHandlerTestEnv(t)
	userID := env.addUser(t)
	currentID := env.createSession(t, userID, testSafariUA, "127.0.0.1")
	targetID := env.createSession(t, userID, testSafariUA, "127.0.0.2")

	req := newAuthRequest(t, http.MethodPost, "/api/v1/auth/logout", `{"session_id":"`+targetID+`"}`, userID, currentID)
	res := httptest.NewRecorder()
	env.handler.Logout(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("Logout status = %d, want 204, body=%s", res.Code, res.Body.String())
	}
	assertSessionListed(t, env.svc, userID, currentID, true)
	assertSessionListed(t, env.svc, userID, targetID, false)
}

func TestLogoutRejectsCrossUserSession(t *testing.T) {
	env := newAuthHandlerTestEnv(t)
	userA := env.addUser(t)
	userB := env.addUser(t)
	currentID := env.createSession(t, userA, testSafariUA, "127.0.0.1")
	targetID := env.createSession(t, userB, testSafariUA, "127.0.0.2")

	req := newAuthRequest(t, http.MethodPost, "/api/v1/auth/logout", `{"session_id":"`+targetID+`"}`, userA, currentID)
	res := httptest.NewRecorder()
	env.handler.Logout(res, req)

	if res.Code != http.StatusNotFound {
		t.Fatalf("Logout status = %d, want 404, body=%s", res.Code, res.Body.String())
	}
	assertSessionListed(t, env.svc, userB, targetID, true)
}

func TestLogoutRejectsUnknownSession(t *testing.T) {
	env := newAuthHandlerTestEnv(t)
	userID := env.addUser(t)
	currentID := env.createSession(t, userID, testSafariUA, "127.0.0.1")

	req := newAuthRequest(t, http.MethodPost, "/api/v1/auth/logout", `{"session_id":"missing"}`, userID, currentID)
	res := httptest.NewRecorder()
	env.handler.Logout(res, req)

	if res.Code != http.StatusNotFound {
		t.Fatalf("Logout status = %d, want 404, body=%s", res.Code, res.Body.String())
	}
	assertSessionListed(t, env.svc, userID, currentID, true)
}

func TestLogoutOwnershipLookupFailure(t *testing.T) {
	env := newAuthHandlerTestEnv(t)
	env.svc.SetSessionOperationTimeout(20 * time.Millisecond)
	userID := env.addUser(t)
	currentID := env.createSession(t, userID, testSafariUA, "127.0.0.1")
	env.redis.Close()

	req := newAuthRequest(t, http.MethodPost, "/api/v1/auth/logout", `{"session_id":"`+currentID+`"}`, userID, currentID)
	res := httptest.NewRecorder()
	env.handler.Logout(res, req)

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("Logout status = %d, want 500, body=%s", res.Code, res.Body.String())
	}
}

func TestLogoutMalformedBodyReturnsBadRequest(t *testing.T) {
	env := newAuthHandlerTestEnv(t)
	userID := env.addUser(t)
	currentID := env.createSession(t, userID, testSafariUA, "127.0.0.1")

	req := newAuthRequest(t, http.MethodPost, "/api/v1/auth/logout", "{", userID, currentID)
	res := httptest.NewRecorder()
	env.handler.Logout(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("Logout status = %d, want 400, body=%s", res.Code, res.Body.String())
	}
	assertSessionListed(t, env.svc, userID, currentID, true)
}

func TestLogoutEmptyBodyRevokesCurrentSession(t *testing.T) {
	env := newAuthHandlerTestEnv(t)
	userID := env.addUser(t)
	currentID := env.createSession(t, userID, testSafariUA, "127.0.0.1")

	req := newAuthRequest(t, http.MethodPost, "/api/v1/auth/logout", "", userID, currentID)
	res := httptest.NewRecorder()
	env.handler.Logout(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("Logout status = %d, want 204, body=%s", res.Code, res.Body.String())
	}
	assertSessionListed(t, env.svc, userID, currentID, false)
}

func TestLogoutOthersRequiresAuth(t *testing.T) {
	env := newAuthHandlerTestEnv(t)
	router := newAuthTestRouter(env.handler)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout-others", nil)
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("LogoutOthers status = %d, want 401, body=%s", res.Code, res.Body.String())
	}
}

func TestLogoutOthersMissingSessionContext(t *testing.T) {
	env := newAuthHandlerTestEnv(t)
	userID := env.addUser(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout-others", nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserIDKey, userID))
	res := httptest.NewRecorder()

	env.handler.LogoutOthers(res, req)

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("LogoutOthers status = %d, want 500, body=%s", res.Code, res.Body.String())
	}
	assertErrorCode(t, res, cerrors.CodeInternal)
}

func TestLogoutOthersRevokesEveryOtherSession(t *testing.T) {
	env := newAuthHandlerTestEnv(t)
	userID := env.addUser(t)
	currentID := env.createSession(t, userID, testSafariUA, "127.0.0.1")
	env.createSession(t, userID, testSafariUA, "127.0.0.2")
	env.createSession(t, userID, testSafariUA, "127.0.0.3")

	req := newAuthRequest(t, http.MethodPost, "/api/v1/auth/logout-others", "", userID, currentID)
	res := httptest.NewRecorder()
	env.handler.LogoutOthers(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("LogoutOthers status = %d, want 204, body=%s", res.Code, res.Body.String())
	}

	listReq := newAuthRequest(t, http.MethodGet, "/api/v1/auth/sessions", "", userID, currentID)
	listRes := httptest.NewRecorder()
	env.handler.ListSessions(listRes, listReq)
	if listRes.Code != http.StatusOK {
		t.Fatalf("ListSessions status = %d, want 200, body=%s", listRes.Code, listRes.Body.String())
	}
	var entries []sessionListEntry
	if err := json.NewDecoder(listRes.Body).Decode(&entries); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("ListSessions returned %d entries, want 1", len(entries))
	}
	if entries[0].ID != currentID || !entries[0].IsCurrent {
		t.Fatalf("remaining entry = %+v, want current %s", entries[0], currentID)
	}
}

func TestLogoutOthersPartialFailureReturnsInternal(t *testing.T) {
	env := newAuthHandlerTestEnv(t)
	userID := env.addUser(t)
	currentID := env.createSession(t, userID, testSafariUA, "127.0.0.1")
	goodID := env.createSession(t, userID, testSafariUA, "127.0.0.2")
	badID := env.createSession(t, userID, testSafariUA, "127.0.0.3")
	if err := env.rdb.Set(context.Background(), "session:"+badID, "not-json", time.Hour).Err(); err != nil {
		t.Fatalf("corrupt session returned error: %v", err)
	}

	req := newAuthRequest(t, http.MethodPost, "/api/v1/auth/logout-others", "", userID, currentID)
	res := httptest.NewRecorder()
	env.handler.LogoutOthers(res, req)

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("LogoutOthers status = %d, want 500, body=%s", res.Code, res.Body.String())
	}
	assertSessionListed(t, env.svc, userID, currentID, true)
	assertSessionListed(t, env.svc, userID, goodID, false)
}

func TestListSessionsMarksCurrentAndAddsParsedLabels(t *testing.T) {
	env := newAuthHandlerTestEnv(t)
	userID := env.addUser(t)
	currentID := env.createSession(t, userID, testSafariUA, "127.0.0.1")
	otherID := env.createSession(t, userID, "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1", "127.0.0.2")

	req := newAuthRequest(t, http.MethodGet, "/api/v1/auth/sessions", "", userID, currentID)
	res := httptest.NewRecorder()
	env.handler.ListSessions(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("ListSessions status = %d, want 200, body=%s", res.Code, res.Body.String())
	}

	var raw []map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw list response: %v", err)
	}
	if len(raw) != 2 {
		t.Fatalf("ListSessions returned %d entries, want 2", len(raw))
	}

	seen := map[string]map[string]any{}
	for _, entry := range raw {
		id, _ := entry["id"].(string)
		seen[id] = entry
		for _, key := range []string{"user_agent", "device_label", "browser_label", "os_label"} {
			if _, ok := entry[key]; !ok {
				t.Fatalf("entry %s missing key %q: %#v", id, key, entry)
			}
		}
	}
	if seen[currentID]["is_current"] != true {
		t.Fatalf("current session is_current = %#v, want true", seen[currentID]["is_current"])
	}
	if seen[otherID]["is_current"] != false {
		t.Fatalf("other session is_current = %#v, want false", seen[otherID]["is_current"])
	}
	if seen[currentID]["device_label"] != "Desktop" || seen[currentID]["browser_label"] != "Safari 17" || seen[currentID]["os_label"] != "macOS 10.15" {
		t.Fatalf("current parsed labels = device:%#v browser:%#v os:%#v", seen[currentID]["device_label"], seen[currentID]["browser_label"], seen[currentID]["os_label"])
	}
}

type authHandlerTestEnv struct {
	handler *AuthHandler
	svc     *auth.Service
	users   *authHandlerFakeUsers
	redis   *miniredis.Miniredis
	rdb     *redis.Client
}

func newAuthHandlerTestEnv(t *testing.T) *authHandlerTestEnv {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	users := &authHandlerFakeUsers{users: map[uuid.UUID]*entity.User{}}
	svc := auth.NewService(users, nil, rdb, []byte("01234567890123456789012345678901"), time.Minute, time.Hour, nil)
	svc.SetSessionOperationTimeout(500 * time.Millisecond)
	t.Cleanup(func() {
		_ = rdb.Close()
		mr.Close()
	})
	return &authHandlerTestEnv{
		handler: NewAuthHandler(svc),
		svc:     svc,
		users:   users,
		redis:   mr,
		rdb:     rdb,
	}
}

func (e *authHandlerTestEnv) addUser(t *testing.T) uuid.UUID {
	t.Helper()
	userID := uuid.New()
	e.users.users[userID] = &entity.User{
		ID:          userID,
		Email:       userID.String() + "@example.com",
		DisplayName: "Test User",
		Status:      entity.UserStatusActive,
		Locale:      "en",
	}
	return userID
}

func (e *authHandlerTestEnv) addCredentialedUser(t *testing.T, email, password string, status entity.UserStatus) uuid.UUID {
	t.Helper()
	user, err := e.svc.Register(context.Background(), email, password, "Test User")
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	user.Status = status
	if status == entity.UserStatusDeactivated {
		now := time.Now().UTC()
		user.DeactivatedAt = &now
	}
	if err := e.users.Update(context.Background(), user); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	return user.ID
}

func loginBody(email, password string) string {
	return `{"email":"` + email + `","password":"` + password + `","device_info":"test ua"}`
}

func (e *authHandlerTestEnv) createSession(t *testing.T, userID uuid.UUID, userAgent, ip string) string {
	t.Helper()
	result, err := e.svc.CreateSessionForUser(context.Background(), userID, userAgent, ip)
	if err != nil {
		t.Fatalf("CreateSessionForUser returned error: %v", err)
	}
	return result.SessionID
}

func newAuthRequest(t *testing.T, method, path, body string, userID uuid.UUID, sessionID string) *http.Request {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	ctx := context.WithValue(req.Context(), middleware.UserIDKey, userID)
	ctx = context.WithValue(ctx, middleware.SessionIDKey, sessionID)
	return req.WithContext(ctx)
}

func newAuthTestRouter(handler *AuthHandler) http.Handler {
	return NewRouter(RouterDeps{
		Auth:             handler,
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
		Validator:        fakeTokenValidator{userID: uuid.New()},
		PersonalResolver: fakePersonalResolver{workspaceID: uuid.New()},
	})
}

func assertSessionListed(t *testing.T, svc *auth.Service, userID uuid.UUID, sessionID string, want bool) {
	t.Helper()
	sessions, err := svc.ListSessions(context.Background(), userID.String())
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	got := false
	for _, session := range sessions {
		if session.ID == sessionID {
			got = true
			break
		}
	}
	if got != want {
		t.Fatalf("session %s listed = %v, want %v", sessionID, got, want)
	}
}

func assertErrorCode(t *testing.T, res *httptest.ResponseRecorder, want cerrors.Code) {
	t.Helper()
	var body errorBody
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error.Code != string(want) {
		t.Fatalf("error code = %q, want %q", body.Error.Code, want)
	}
}

type authHandlerFakeUsers struct {
	users map[uuid.UUID]*entity.User
}

func (r *authHandlerFakeUsers) Create(_ context.Context, user *entity.User) error {
	if r.users == nil {
		r.users = map[uuid.UUID]*entity.User{}
	}
	r.users[user.ID] = user
	return nil
}

func (r *authHandlerFakeUsers) GetByID(_ context.Context, id uuid.UUID) (*entity.User, error) {
	if user := r.users[id]; user != nil {
		return user, nil
	}
	return nil, cerrors.NotFound("user not found")
}

func (r *authHandlerFakeUsers) GetByEmail(_ context.Context, email string) (*entity.User, error) {
	for _, user := range r.users {
		if user.Email == email {
			return user, nil
		}
	}
	return nil, cerrors.NotFound("user not found")
}

func (r *authHandlerFakeUsers) Update(_ context.Context, user *entity.User) error {
	if r.users == nil {
		r.users = map[uuid.UUID]*entity.User{}
	}
	r.users[user.ID] = user
	return nil
}
