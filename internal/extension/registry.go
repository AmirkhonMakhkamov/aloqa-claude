package extension

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Registry is the central integration registry for optional platform modules.
// Providers can be attached during boot or by a future plugin loader without
// forcing core chat/media services to import vendor-specific implementations.
type Registry struct {
	mu sync.RWMutex

	ai          AIProvider
	telephony   TelephonyProvider
	marketplace MarketplaceService
	whiteLabel  WhiteLabelService
	hooks       *HookDispatcher
}

type RegistryOption func(*Registry)

func NewRegistry(opts ...RegistryOption) *Registry {
	r := &Registry{hooks: NewHookDispatcher()}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func WithAI(provider AIProvider) RegistryOption {
	return func(r *Registry) { r.ai = provider }
}

func WithTelephony(provider TelephonyProvider) RegistryOption {
	return func(r *Registry) { r.telephony = provider }
}

func WithMarketplace(service MarketplaceService) RegistryOption {
	return func(r *Registry) { r.marketplace = service }
}

func WithWhiteLabel(service WhiteLabelService) RegistryOption {
	return func(r *Registry) { r.whiteLabel = service }
}

func (r *Registry) AI() (AIProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ai, r.ai != nil
}

func (r *Registry) Telephony() (TelephonyProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.telephony, r.telephony != nil
}

func (r *Registry) Marketplace() (MarketplaceService, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.marketplace, r.marketplace != nil
}

func (r *Registry) WhiteLabel() (WhiteLabelService, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.whiteLabel, r.whiteLabel != nil
}

func (r *Registry) Hooks() *HookDispatcher {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.hooks
}

type HookEventType string

const (
	HookMessageCreated    HookEventType = "message.created"
	HookMessageUpdated    HookEventType = "message.updated"
	HookCallStarted       HookEventType = "call.started"
	HookCallEnded         HookEventType = "call.ended"
	HookRecordingReady    HookEventType = "recording.ready"
	HookWorkspaceUpdated  HookEventType = "workspace.updated"
	HookMarketplaceAction HookEventType = "marketplace.action"
)

type HookEvent struct {
	ID             uuid.UUID     `json:"id"`
	Type           HookEventType `json:"type"`
	WorkspaceID    uuid.UUID     `json:"workspace_id"`
	ActorID        uuid.UUID     `json:"actor_id,omitempty"`
	ResourceID     uuid.UUID     `json:"resource_id,omitempty"`
	IdempotencyKey string        `json:"idempotency_key"`
	Payload        any           `json:"payload,omitempty"`
	Timestamp      time.Time     `json:"timestamp"`
}

type HookHandler interface {
	HandleHook(ctx context.Context, event HookEvent) error
}

type HookHandlerFunc func(ctx context.Context, event HookEvent) error

func (f HookHandlerFunc) HandleHook(ctx context.Context, event HookEvent) error {
	return f(ctx, event)
}

type HookDispatcher struct {
	mu       sync.RWMutex
	handlers map[HookEventType][]HookHandler
}

func NewHookDispatcher() *HookDispatcher {
	return &HookDispatcher{handlers: make(map[HookEventType][]HookHandler)}
}

func (d *HookDispatcher) Register(eventType HookEventType, handler HookHandler) {
	if handler == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.handlers[eventType] = append(d.handlers[eventType], handler)
}

func (d *HookDispatcher) Dispatch(ctx context.Context, event HookEvent) error {
	if event.ID == uuid.Nil {
		event.ID = uuid.New()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if event.IdempotencyKey == "" {
		event.IdempotencyKey = event.ID.String()
	}

	d.mu.RLock()
	handlers := append([]HookHandler(nil), d.handlers[event.Type]...)
	handlers = append(handlers, d.handlers["*"]...)
	d.mu.RUnlock()

	errs := make([]error, 0, len(handlers))
	for _, handler := range handlers {
		if err := handler.HandleHook(ctx, event); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
