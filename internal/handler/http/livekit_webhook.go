package http

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/webhook"

	"aloqa/internal/config"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/service/call"
)

// LiveKitWebhookHandler verifies LiveKit-signed webhook payloads and bridges
// them into the call service.
type LiveKitWebhookHandler struct {
	svc         *call.Service
	providers   []auth.KeyProvider
	enabled     bool
	webhookPath string
}

// LiveKitWebhookOption customizes the public LiveKit webhook route and verifier.
type LiveKitWebhookOption func(*liveKitWebhookOptions)

type liveKitWebhookOptions struct {
	webhookPath              string
	webhookPreviousAPIKey    string
	webhookPreviousAPISecret string
}

// NewLiveKitWebhookHandler wires the call service together with the LiveKit
// key/secret pair used to verify webhook signatures. If apiKey or apiSecret is
// empty the handler responds with 503 (the LiveKit bridge is disabled). A
// previous key/secret pair can be accepted during planned webhook key rotation.
func NewLiveKitWebhookHandler(svc *call.Service, apiKey, apiSecret string, options ...LiveKitWebhookOption) *LiveKitWebhookHandler {
	opts := liveKitWebhookOptions{webhookPath: config.DefaultLiveKitWebhookPath}
	for _, option := range options {
		if option != nil {
			option(&opts)
		}
	}
	webhookPath, err := config.NormalizeLiveKitWebhookPath(opts.webhookPath)
	if err != nil {
		webhookPath = config.DefaultLiveKitWebhookPath
	}

	enabled := apiKey != "" && apiSecret != "" && svc != nil
	providers := []auth.KeyProvider{}
	if enabled {
		providers = append(providers, auth.NewSimpleKeyProvider(apiKey, apiSecret))
		if opts.webhookPreviousAPIKey != "" && opts.webhookPreviousAPISecret != "" {
			providers = append(providers, auth.NewSimpleKeyProvider(opts.webhookPreviousAPIKey, opts.webhookPreviousAPISecret))
		}
	}
	return &LiveKitWebhookHandler{svc: svc, providers: providers, enabled: enabled, webhookPath: webhookPath}
}

// WithLiveKitWebhookPath sets the public POST route for LiveKit webhook events.
func WithLiveKitWebhookPath(webhookPath string) LiveKitWebhookOption {
	return func(options *liveKitWebhookOptions) {
		if webhookPath != "" {
			options.webhookPath = webhookPath
		}
	}
}

// WithPreviousLiveKitWebhookKey accepts the prior signing key during rotation.
func WithPreviousLiveKitWebhookKey(apiKey, apiSecret string) LiveKitWebhookOption {
	return func(options *liveKitWebhookOptions) {
		options.webhookPreviousAPIKey = apiKey
		options.webhookPreviousAPISecret = apiSecret
	}
}

func (h *LiveKitWebhookHandler) WebhookPath() string {
	if h.webhookPath == "" {
		return config.DefaultLiveKitWebhookPath
	}
	return h.webhookPath
}

// Webhook is the HTTP entry point invoked by LiveKit Server.
func (h *LiveKitWebhookHandler) Webhook(w http.ResponseWriter, r *http.Request) {
	if !h.enabled {
		http.Error(w, "livekit webhook is not configured", http.StatusServiceUnavailable)
		return
	}

	ev, err := receiveLiveKitWebhookEvent(r, h.providers)
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

func receiveLiveKitWebhookEvent(r *http.Request, providers []auth.KeyProvider) (event *livekit.WebhookEvent, err error) {
	defer func() {
		closeErr := r.Body.Close()
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for _, provider := range providers {
		candidate := r.Clone(r.Context())
		candidate.Body = io.NopCloser(bytes.NewReader(body))
		event, err := webhook.ReceiveWebhookEvent(candidate, provider)
		if err == nil {
			return event, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, webhook.ErrSecretNotFound
}
