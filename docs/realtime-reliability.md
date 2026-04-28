# Realtime Reliability

This backend now treats realtime delivery as a set of explicit contracts instead of one generic "best effort" channel.

## Delivery Semantics

Event delivery is defined in [events.go](/Users/makhkamov/Desktop/Work/Wibe/Go%202/internal/domain/event/events.go).

- `ephemeral`
  - Used for short-lived UX hints and direct peer signaling.
  - Examples: `typing`, `signal.offer`, `signal.answer`, `signal.candidate`.
  - Published directly to NATS.
  - Not replayed.
  - Loss is acceptable.

- `best_effort`
  - Used for state that is continuously refreshed or can be recomputed from current state.
  - Examples: `presence.changed`, `call.quality.adapted`.
  - Published directly to NATS.
  - Not replayed.
  - If a client misses one update, the next status refresh supersedes it.

- `at_least_once`
  - Used for durable collaboration state changes.
  - Examples: message lifecycle, channel lifecycle, call membership/control events, file lifecycle, recording lifecycle, guest/collaboration access changes.
  - Enqueued into the Postgres realtime outbox first.
  - Published asynchronously by the outbox worker.
  - Replayable.
  - Consumers must be idempotent because duplicate delivery is allowed.

## Event Envelope

All normalized realtime events now carry:

- `id`
- `version`
- `sequence`
- `subject`
- `delivery_semantic`
- `replayable`
- `timestamp`

This gives us stable identifiers for de-duplication, version-aware consumers, and replay cursors.

## Transport And Durable Queue

The transport remains NATS JetStream, but durable events are first written to Postgres in `realtime_events`.

Flow for durable events:

1. Service emits an event through the realtime publisher.
2. Publisher normalizes the envelope and stores it in `realtime_events`.
3. Background worker claims pending rows with `FOR UPDATE SKIP LOCKED`.
4. Worker publishes to NATS using the event UUID as the NATS message ID.
5. On success the row becomes `published`.
6. On failure the row is retried with backoff or moved to dead-letter state.

This gives us:

- durable retry state
- replay source of truth
- consumer lag visibility
- dead-letter visibility
- publish dedupe on the transport side

## Idempotency

Authenticated write endpoints now support request idempotency through the `Idempotency-Key` header.

Behavior:

- Scoped by authenticated user, HTTP method, and path.
- The request fingerprint includes query, content type, and body hash.
- Repeating the same request with the same key replays the stored response.
- Reusing the same key with different request content returns `409 Conflict`.
- `5xx` responses are not cached as successful idempotent outcomes.

This is implemented in [idempotency.go](/Users/makhkamov/Desktop/Work/Wibe/Go%202/internal/middleware/idempotency.go).

## Retry And Dead-Letter

Durable events move through these states:

- `pending`
- `processing`
- `published`
- `failed`
- `dead`

Retry behavior:

- exponential backoff
- capped attempt count
- abandoned processing rows are reclaimable after lock expiry
- exhausted rows remain queryable as dead-letter items

The consumer cursor table tracks:

- last delivered sequence
- deliveries
- failures
- lag
- status
- last error

This is the current operational visibility surface for event consumers.

## Replay And Recovery

Replay is room-based and sequence-based.

- Durable, replayable events are read from `realtime_events`.
- WebSocket clients persist room subscriptions in Redis.
- Redis also stores the last delivered sequence per user and room.
- On reconnect:
  - the server reloads saved rooms
  - re-authorizes each room subscription
  - drops unauthorized rooms from saved state
  - replays missed durable events after the stored sequence

Signal rooms are intentionally excluded from replay.

## Presence Recovery

Presence is soft state, not durable event history.

- Presence is stored in Redis with TTL.
- Clients recover presence by reconnecting and resuming heartbeat or explicit status updates.
- `presence.changed` is treated as `best_effort` because the authoritative state is the current Redis presence record, not an event log.

This keeps presence fresh without incorrectly treating every transient presence blip as audit-grade durable history.

## Recovery Expectations

- If NATS is briefly unavailable, durable events stay in Postgres and retry later.
- If a WebSocket server restarts, subscriptions and replay cursors are restored from Redis.
- If a client reconnects after missing durable room events, replay fills the gap.
- If a client misses ephemeral or best-effort events, it must resync from the authoritative HTTP state if needed.

## Important Caveat

This is a strong durability upgrade, but it is not yet a fully transactional outbox across every domain write.

Right now the domain write usually commits first, and the realtime publisher then writes the outbox record. That means:

- downstream publish failures are recoverable
- duplicate delivery is controlled
- replay is available

But there is still a narrow gap between the domain write and outbox enqueue.

The next step to reach full transactional outbox semantics is to move the affected write paths onto shared SQL transactions that write both domain rows and outbox rows atomically.
