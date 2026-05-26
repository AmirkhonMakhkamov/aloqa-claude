package http

import (
	"log/slog"
	"net/http"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/webhook"

	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/service/call"
)

// LiveKitWebhookHandler verifies LiveKit-signed webhook payloads and bridges
// them into the call service.
type LiveKitWebhookHandler struct {
	svc      *call.Service
	provider auth.KeyProvider
	enabled  bool
}

// NewLiveKitWebhookHandler wires the call service together with the LiveKit
// key/secret pair used to verify webhook signatures. If apiKey or apiSecret is
// empty the handler responds with 503 (the LiveKit bridge is disabled).
func NewLiveKitWebhookHandler(svc *call.Service, apiKey, apiSecret string) *LiveKitWebhookHandler {
	enabled := apiKey != "" && apiSecret != "" && svc != nil
	var provider auth.KeyProvider
	if enabled {
		provider = auth.NewSimpleKeyProvider(apiKey, apiSecret)
	}
	return &LiveKitWebhookHandler{svc: svc, provider: provider, enabled: enabled}
}

// Webhook is the HTTP entry point invoked by LiveKit Server.
func (h *LiveKitWebhookHandler) Webhook(w http.ResponseWriter, r *http.Request) {
	if !h.enabled {
		http.Error(w, "livekit webhook is not configured", http.StatusServiceUnavailable)
		return
	}

	ev, err := webhook.ReceiveWebhookEvent(r, h.provider)
	if err != nil {
		slog.WarnContext(r.Context(), "livekit webhook signature/parse failed", "error", err)
		http.Error(w, "invalid livekit webhook", http.StatusUnauthorized)
		return
	}

	if err := h.svc.HandleLiveKitWebhook(r.Context(), ev); err != nil {
		slog.ErrorContext(r.Context(), "failed to process livekit webhook", "event", ev.GetEvent(), "id", ev.GetId(), "error", err)
		if appErr, ok := cerrors.AsAppError(err); ok {
			http.Error(w, "failed to process webhook", appErr.HTTPStatus())
			return
		}
		http.Error(w, "failed to process webhook", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
