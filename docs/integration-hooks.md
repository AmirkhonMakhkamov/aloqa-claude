# Integration Hooks

The backend keeps telephony, AI, app marketplace, and white-labeling behind provider interfaces so core chat and media services do not depend on vendor SDKs.

## Registry

- `extension.Registry` stores optional providers for AI, telephony, marketplace, and white-label services.
- Provider lookups return `(provider, ok)` so callers can fail closed when a feature is disabled or not installed.
- The registry owns a `HookDispatcher` for cross-module events.

## Hook Dispatcher

- Hook events include an ID, type, workspace, actor, resource, idempotency key, payload, and timestamp.
- Handlers can subscribe to specific event types such as `call.started`, `recording.ready`, or the wildcard `*`.
- Dispatch aggregates handler errors with `errors.Join`, so caller code can observe partial failures without losing individual causes.

## Production Rules

- Persist outbound marketplace/webhook work through an outbox before delivering external calls.
- Use per-app permissions and per-workspace feature flags before invoking providers.
- Keep AI and transcription jobs asynchronous unless a user explicitly requests an interactive operation.
- Use idempotency keys for webhook and telephony provider retries.
- Never let optional provider failures break core chat/call state transitions unless the provider is required for that feature.
