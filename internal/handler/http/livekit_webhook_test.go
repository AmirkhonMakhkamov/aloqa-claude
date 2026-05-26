package http

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils/protojson"

	"aloqa/internal/service/call"
)

func TestRouterMountsDefaultLiveKitWebhookPath(t *testing.T) {
	router := NewRouter(RouterDeps{
		LiveKit: NewLiveKitWebhookHandler(nil, "", ""),
	})

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/livekit/webhook", nil)
	router.ServeHTTP(res, req)

	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("default livekit webhook status = %d, want 503", res.Code)
	}
}

func TestRouterMountsCustomLiveKitWebhookPath(t *testing.T) {
	router := NewRouter(RouterDeps{
		LiveKit: NewLiveKitWebhookHandler(
			nil,
			"",
			"",
			WithLiveKitWebhookPath("/ops/livekit-webhook"),
		),
	})

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ops/livekit-webhook", nil)
	router.ServeHTTP(res, req)

	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("custom livekit webhook status = %d, want 503", res.Code)
	}

	defaultRes := httptest.NewRecorder()
	defaultReq := httptest.NewRequest(http.MethodPost, "/livekit/webhook", nil)
	router.ServeHTTP(defaultRes, defaultReq)
	if defaultRes.Code != http.StatusNotFound {
		t.Fatalf("default path on custom router status = %d, want 404", defaultRes.Code)
	}
}

func TestLiveKitWebhookAcceptsPreviousKeyDuringRotation(t *testing.T) {
	handler := NewLiveKitWebhookHandler(
		&call.Service{},
		"active-key",
		"active-secret",
		WithPreviousLiveKitWebhookKey("previous-key", "previous-secret"),
	)
	req := signedLiveKitWebhookRequest(t, "previous-key", "previous-secret", &livekit.WebhookEvent{
		Id:    "event-1",
		Event: "room_started",
	})
	res := httptest.NewRecorder()

	handler.Webhook(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("previous-key webhook status = %d, want 204; body=%s", res.Code, res.Body.String())
	}
}

func TestLiveKitWebhookFallsBackToPreviousSecretForSameKey(t *testing.T) {
	handler := NewLiveKitWebhookHandler(
		&call.Service{},
		"active-key",
		"new-secret",
		WithPreviousLiveKitWebhookKey("active-key", "old-secret"),
	)
	req := signedLiveKitWebhookRequest(t, "active-key", "old-secret", &livekit.WebhookEvent{
		Id:    "event-1",
		Event: "room_started",
	})
	res := httptest.NewRecorder()

	handler.Webhook(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("previous-secret webhook status = %d, want 204; body=%s", res.Code, res.Body.String())
	}
}

func TestLiveKitWebhookRejectsInvalidSignature(t *testing.T) {
	handler := NewLiveKitWebhookHandler(&call.Service{}, "active-key", "active-secret")
	req := signedLiveKitWebhookRequest(t, "active-key", "wrong-secret", &livekit.WebhookEvent{
		Id:    "event-1",
		Event: "room_started",
	})
	res := httptest.NewRecorder()

	handler.Webhook(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("invalid-signature webhook status = %d, want 401", res.Code)
	}
}

func TestLiveKitWebhookMissingConfigReturnsUnavailable(t *testing.T) {
	handler := NewLiveKitWebhookHandler(&call.Service{}, "", "")
	req := signedLiveKitWebhookRequest(t, "active-key", "active-secret", &livekit.WebhookEvent{
		Id:    "event-1",
		Event: "room_started",
	})
	res := httptest.NewRecorder()

	handler.Webhook(res, req)

	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing-config webhook status = %d, want 503", res.Code)
	}
}

func signedLiveKitWebhookRequest(t *testing.T, apiKey, apiSecret string, event *livekit.WebhookEvent) *http.Request {
	t.Helper()

	encoded, err := protojson.Marshal(event)
	if err != nil {
		t.Fatalf("marshal webhook event: %v", err)
	}
	sum := sha256.Sum256(encoded)
	checksum := base64.StdEncoding.EncodeToString(sum[:])
	token, err := auth.NewAccessToken(apiKey, apiSecret).
		SetValidFor(5 * time.Minute).
		SetSha256(checksum).
		ToJWT()
	if err != nil {
		t.Fatalf("sign webhook token: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/livekit/webhook", bytes.NewReader(encoded))
	req.Header.Set("Authorization", token)
	req.Header.Set("Content-Type", "application/webhook+json")
	return req
}
