# BE-PR1 — call_messages Resource (ALOQA-242)

> Backend resource powering in-call chat (FE PR4 ALOQA-211). Adds persistence + WS broadcast required for the frontend in-call chat panel and the `CallEndedSummary` transcript.
>
> Spec v2 — addresses Codex review (1 blocking, 3 high, 2 medium).

## Goal

Add a `call_messages` resource that:

1. Persists messages sent during a call (sender, body, timestamps) — UUIDv7 keys, soft delete.
2. Broadcasts `call.message.created` / `call.message.deleted` to call participants in real time via the existing outbox path.
3. Survives the call so the FE `CallEndedSummary` can render the transcript.
4. Is gated by the existing `Call.Settings.Chat` flag (`internal/domain/entity/call.go:54`).

Out of scope: editing, reactions on call messages, threading, file attachments, message search, mentions, link unfurl. Reactions on call messages may follow in a separate ticket once the FE establishes a need.

## Surface

### Domain entity (`internal/domain/entity/call_message.go`, new)

```go
type CallMessage struct {
    ID        uuid.UUID  `json:"id"`
    CallID    uuid.UUID  `json:"call_id"`
    SenderID  uuid.UUID  `json:"sender_id"`
    Body      string     `json:"body"`
    CreatedAt time.Time  `json:"created_at"`
    DeletedAt *time.Time `json:"deleted_at,omitempty"`
}
```

JSON tags match other domain response structs (`entity.Call` carries tags at `call.go:59-71`). The handler may serialize the entity directly; no separate DTO is needed.

Validation: `strings.TrimSpace(Body)` must be non-empty and ≤ 2000 chars after trim. UTF-8 must be valid (rely on `utf8.ValidString`). Body is stored verbatim — FE renders markdown.

### Migration `034_call_messages.sql` (new)

```sql
CREATE TABLE call_messages (
    id          uuid        PRIMARY KEY,
    call_id     uuid        NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
    sender_id   uuid        NOT NULL REFERENCES users(id),
    body        text        NOT NULL CHECK (length(btrim(body)) BETWEEN 1 AND 2000),
    created_at  timestamptz NOT NULL DEFAULT NOW(),
    deleted_at  timestamptz
);

CREATE INDEX idx_call_messages_call_id_id ON call_messages (call_id, id DESC);
CREATE INDEX idx_call_messages_sender ON call_messages (sender_id);
```

The composite `(call_id, id DESC)` index covers the dominant query (`WHERE call_id = $1 AND id < $cursor ORDER BY id DESC LIMIT $n`) and avoids same-timestamp tie-breaking issues because IDs are UUIDv7 and naturally time-ordered (`internal/pkg/id/id.go:5-10`).

Down migration drops indexes + table. Last migration is `033`; `034` is the next number — confirmed in `migrations/` listing.

### Repository (`internal/repository/postgres/call_message.go`, new)

The repo follows the **postgres tx pattern used by `MessageRepo`** (`internal/repository/postgres/message.go`) and `CallRepo` (`internal/repository/postgres/call.go`): a `pool *pgxpool.Pool` plus a `db queryable` receiver method `withTx` so a service-provided `pgx.Tx` shadows the pool during a transaction.

```go
type CallMessageRepo struct {
    pool *pgxpool.Pool
    db   queryable // shadowed inside withTx
}

func NewCallMessageRepo(pool *pgxpool.Pool) *CallMessageRepo { ... }

// withTx returns a copy of the repo bound to the given tx.
func (r *CallMessageRepo) withTx(tx pgx.Tx) *CallMessageRepo { ... }

func (r *CallMessageRepo) Create(ctx context.Context, msg *entity.CallMessage) error
func (r *CallMessageRepo) ListByCall(
    ctx context.Context,
    callID uuid.UUID,
    p pagination.Params, // uses internal/pkg/pagination — base64 UUID cursor
) ([]entity.CallMessage, error)
func (r *CallMessageRepo) SoftDelete(ctx context.Context, id, callID uuid.UUID) error
func (r *CallMessageRepo) GetByID(ctx context.Context, id uuid.UUID) (*entity.CallMessage, error)
```

`ListByCall` returns messages ordered by `id DESC` (UUIDv7 makes this chronological), filters `deleted_at IS NULL`, and uses `m.id < cursor` for pagination — exactly mirroring `MessageRepo.ListByChannel` at `internal/repository/postgres/message.go:141-175`. `pagination.Params` (`internal/pkg/pagination/pagination.go:36-53`) carries the opaque base64 UUID cursor and the limit, clamped at 100.

### Service wiring (`internal/service/call/service.go`)

The existing `NewService(...)` constructor has a fixed positional signature (`service.go:79-89`) with setter-based optional dependencies (`SetMediaControlPlane` / `SetTransactionManager` at `service.go:109-114`). The call-message repo follows the **setter pattern** to avoid touching every call site:

```go
func (s *Service) SetCallMessageRepo(repo CallMessageRepository) {
    s.callMessages = repo
}
```

The `CallMessageRepository` interface lives in the call service package and declares the four repo methods above plus a `WithTx(pgx.Tx) CallMessageRepository` accessor so the service can run create/delete inside a tx for outbox atomicity.

`cmd/server/main.go` adds one line after `NewCallMessageRepo(pool)`:

```go
callSvc.SetCallMessageRepo(callMessageRepo)
```

If `s.callMessages == nil` at request time, the handler returns `cerrors.Unavailable("call messaging not configured")` — same pattern as media plane absence.

### Transaction outbox plumbing

Required for atomic "persist + enqueue WS event" semantics. Mirrors chat exactly (`internal/service/chat/service.go:674-688`).

**`internal/platform/txscope/interfaces.go`** — add accessor:

```go
type Scope interface {
    Messages() chat.MessageRepository
    Calls() call.CallRepository
    CallMessages() call.CallMessageRepository // NEW
    EnqueueRealtime(ctx, *realtime.Event) error
    // ... existing methods
}
```

**`internal/repository/postgres/tx.go`** — extend `txScope` (lines 60-75) to hold the call-message repo and bind it via `withTx` at lines 177-182:

```go
type txScope struct {
    messages      *MessageRepo
    calls         *CallRepo
    callMessages  *CallMessageRepo // NEW
    // ...
}

// Inside withTx:
ts.callMessages = ts.callMessages.withTx(tx)
```

**`internal/platform/txscope/config.go`** (`TxManagerConfig`) — register the repo so `txManager.Begin(ctx)` injects it.

**Service Send call** (mirrors `chat/service.SendMessage:674-688`):

```go
err := s.tx.Run(ctx, func(scope txscope.Scope) error {
    if err := scope.CallMessages().Create(ctx, msg); err != nil {
        return err
    }
    rtEvent := event.Prepare(event.Event{
        Type: event.TypeCallMessageCreated,
        ... payload ...
    })
    return scope.EnqueueRealtime(ctx, rtEvent)
})
```

`event.Prepare` (referenced at `internal/domain/event/events.go`) populates `Version`, `Subject`, `DeliverySemantic`, and `Replayable` from `DefinitionForType` before the event hits the outbox.

### Event types (`internal/domain/event/events.go`)

Add the new types alongside the existing `call.*` group (lines 42-48):

```go
const (
    TypeCallMessageCreated Type = "call.message.created"
    TypeCallMessageDeleted Type = "call.message.deleted"
)
```

Add payload types:

```go
type CallMessagePayload struct {
    Message entity.CallMessage `json:"message"`
}

type CallMessageDeletedPayload struct {
    MessageID uuid.UUID `json:"message_id"`
    CallID    uuid.UUID `json:"call_id"`
}
```

Extend `DefinitionForType` (`events.go:107-127`) with an explicit case for the new types:

```go
case TypeCallMessageCreated, TypeCallMessageDeleted:
    return Definition{
        DeliverySemantic: DeliveryAtLeastOnce,
        Replayable:       true,
        Subject:          subjectForWorkspace(workspaceID),
    }
```

Subject is `aloqa.ws.<workspaceID>` to match the existing `call.*` events at `service.go:1213`. This is the same audience as `call.participant.joined` — workspace members can subscribe, but the events only carry call metadata they already see in `GetCall`. Tight call-scoping is unnecessary for persisted messages because `ListCallMessages` is the source of truth and is itself access-gated.

### HTTP handlers (`internal/handler/http/call_message.go`, new)

The workspace prefix is **already mounted** at `/api/v1/workspaces/{workspaceID}` in `router.go:135-143`. Calls are mounted at `/calls` then `r.Route("/{callID}", ...)` at `router.go:275-282`. The new routes nest inside that block, **without** restating the workspace prefix:

```go
r.Route("/{callID}", func(r chi.Router) {
    // ... existing routes ...
    r.Route("/messages", func(r chi.Router) {
        r.Post("/", deps.Calls.SendCallMessage)
        r.Get("/", deps.Calls.ListCallMessages)
        r.Delete("/{messageID}", deps.Calls.DeleteCallMessage)
    })
})
```

External URLs:

- `POST   /api/v1/workspaces/{workspaceID}/calls/{callID}/messages`
- `GET    /api/v1/workspaces/{workspaceID}/calls/{callID}/messages?cursor=&limit=`
- `DELETE /api/v1/workspaces/{workspaceID}/calls/{callID}/messages/{messageID}`

`workspaceID` is read via `middleware.WorkspaceIDFromContext(r.Context())` per existing handler convention.

Request body for POST:

```json
{ "body": "<1..2000 chars after trim>" }
```

Response shapes serialize the entity directly (JSON tags above):

- POST → 201 + `CallMessage`
- GET → 200 + `{ "messages": [...], "next_cursor": "<base64 UUID>" | null }`
- DELETE → 204

### Service surface (`internal/service/call/message.go`, new)

```go
func (s *Service) SendCallMessage(
    ctx context.Context,
    workspaceID, callID, senderID uuid.UUID,
    body string,
) (*entity.CallMessage, error)

func (s *Service) ListCallMessages(
    ctx context.Context,
    workspaceID, callID, requesterID uuid.UUID,
    params pagination.Params,
) ([]entity.CallMessage, error)

func (s *Service) DeleteCallMessage(
    ctx context.Context,
    workspaceID, callID, requesterID, messageID uuid.UUID,
) error
```

Access checks reuse existing helpers:

- `SendCallMessage`:
  1. `requireCallAccess(ctx, workspaceID, callID, senderID)` (`service.go:199-220`) — workspace + channel boundary.
  2. `GetParticipant(ctx, callID, senderID)` must exist with `Status == ParticipantStatusConnected`. If missing → `cerrors.Forbidden("not in call")`.
  3. If `call.Settings.Chat == false` → `cerrors.Forbidden("call chat is disabled")`.
  4. If `call.Status == CallStatusEnded` → `cerrors.Forbidden("call is not active")` (matches `breakout.go:124` precedent).
- `ListCallMessages`: `requireCallAccess` only. Even disconnected workspace members can fetch the transcript via `CallEndedSummary`.
- `DeleteCallMessage`:
  1. `requireCallAccess`.
  2. Fetch message; if `msg.SenderID != requesterID`, require host/co-host via `requireHostOrCoHost(ctx, callID, requesterID)` (`breakout.go:383`).
  3. Soft-delete + emit `call.message.deleted`.

Errors use stable English messages (`cerrors.Forbidden(msg)` takes a single message; `internal/pkg/cerrors/errors.go:80-82`). Tests assert on `cerrors.AsAppError(err)` code + message text — no structured `reason` field is added.

### Lifecycle interaction

- `EndCall` does **not** delete messages — the FE needs them for `CallEndedSummary`. Soft-delete on parent call deletion is handled by the FK `ON DELETE CASCADE`.
- A participant who has LEFT (status=disconnected) can still call `ListCallMessages` while the call is active. Sending requires connected status.

## Test plan

### Repo (`internal/repository/postgres/call_message_test.go`)

Tests use the standard postgres test harness (per existing `message_test.go` / `call_test.go`). They run against the test DB, not the in-memory fakes used in service tests.

- Round-trip `Create` → `GetByID` matches all fields.
- `ListByCall` returns newest-first, excludes soft-deleted.
- Cursor pagination: 2-page walk over 5 messages with `limit=3` → second-page first row equals fourth-newest.
- `SoftDelete` is idempotent (twice → no error, `deleted_at` stable).
- ON DELETE CASCADE from `calls` removes messages (assert via row count after deleting parent).
- Body-length CHECK rejects 0-char and 2001-char inputs at insert.

### Service (`internal/service/call/message_test.go`)

Uses the existing fake-repo pattern (`service_test.go` hand-rolled fakes — in-memory maps). Adds `fakeCallMessageRepo` mirroring the interface. Mocks `txManager` to assert the create+enqueue happens in one logical scope.

- Send happy path → returns persisted message + outbox enqueue carries `call.message.created` with `DeliveryAtLeastOnce`.
- Send when caller not a participant → `CodeForbidden` ("not in call").
- Send when participant status=disconnected → `CodeForbidden` ("not in call").
- Send when `Call.Settings.Chat == false` → `CodeForbidden` ("call chat is disabled").
- Send when `Call.Status == ended` → `CodeForbidden` ("call is not active").
- Send empty / whitespace-only body → `CodeInvalidInput`.
- Send 2001-char body → `CodeInvalidInput`.
- List returns messages even after caller leaves.
- Delete by sender → soft-deletes + enqueues `call.message.deleted`.
- Delete by host (not sender) → succeeds.
- Delete by random workspace member (not sender, not host) → `CodeForbidden`.
- Delete unknown message → `CodeNotFound`.

### HTTP (`internal/handler/http/call_message_test.go`)

Standard handler tests using `httptest`:

- 201 on POST, body present, outbox enqueue triggered (assert via injected fake tx scope).
- 400 on malformed JSON / empty body / oversize body.
- 401 unauthenticated, 403 not-in-call.
- 200 on GET with cursor pagination; opaque cursor round-trips.
- 204 on DELETE.

## Risks

1. **Outbox replay duplication** — `at_least_once` semantics means FE may receive duplicate `call.message.created` for a single send. FE PR4 dedupes by `message.id`. Documented but not enforced here.
2. **Same-timestamp ordering** — handled by UUIDv7 + `id DESC` ordering; the composite index `(call_id, id DESC)` covers the query.
3. **Migration ordering** — must apply 034 after 033 cleanly. No schema dependencies beyond `calls(id)` and `users(id)`.

## Open question for FE PR4 (ALOQA-211)

The architect should confirm before implementation that excluding reactions, edit, attachments, mentions, and link unfurl from call messages matches the FE in-call chat panel design. The BE repo cannot verify this. If the FE assumes any of those, that's a follow-up Jira.

## Out of scope (re-stated)

Reactions on call messages, edit, file attachments, image upload, @mentions, threading, server-side markdown rendering, link unfurl, message search, rate-limit middleware tuning.
