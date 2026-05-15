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

### Repository

**Interface** (`internal/domain/repository/interfaces.go`, append next to `CallRepository` at line 207):

```go
type CallMessageRepository interface {
    Create(ctx context.Context, msg *entity.CallMessage) error
    ListByCall(ctx context.Context, callID uuid.UUID, p pagination.Params) ([]entity.CallMessage, error)
    SoftDelete(ctx context.Context, id, callID uuid.UUID) error
    GetByID(ctx context.Context, id uuid.UUID) (*entity.CallMessage, error)
}
```

The interface lives in `internal/domain/repository/interfaces.go` to match `CallRepository`/`MessageRepository`/etc. and to avoid an import cycle with `txscope` (which imports `domain/repository`).

**Postgres implementation** (`internal/repository/postgres/call_message.go`, new) follows the existing `CallRepo` pattern at `internal/repository/postgres/call.go:30`:

```go
type CallMessageRepo struct {
    pool *pgxpool.Pool
    db   queryable
}

func NewCallMessageRepo(pool *pgxpool.Pool) *CallMessageRepo { ... }

// withTx is unexported; called only from txScope.WithinTx (tx.go).
func (r *CallMessageRepo) withTx(tx pgx.Tx) *CallMessageRepo { ... }
```

The exported interface has no `WithTx` method — tx binding is wired in `internal/repository/postgres/tx.go` exactly like `scope.calls = m.calls.withTx(tx)` at line 139.

`ListByCall` returns messages ordered by `id DESC` (UUIDv7 makes this chronological), filters `deleted_at IS NULL`, and uses `m.id < cursor` for pagination — exactly mirroring `MessageRepo.ListByChannel` at `internal/repository/postgres/message.go:141-175`. The repo fetches `limit+1` rows so the handler can detect `has_more`.

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

Required for atomic "persist + enqueue WS event" semantics. The whole flow mirrors chat at `internal/service/chat/service.go:674-707` and reuses the existing `enqueueRealtimeTx` helper at `internal/service/call/service.go:1249-1265` (the `enqueueParticipantEventTx` etc. already use it). Real signatures from the repo, not invented:

- `txscope.Manager.WithinTx(ctx, fn func(ctx, scope) error) error` (`internal/platform/txscope/interfaces.go:28-30`)
- `txscope.Scope.EnqueueRealtime(ctx, evt event.Event, body []byte) error` (`interfaces.go:25`)
- `event.Prepare(subject string, evt event.Event) (event.Event, []byte, bool, error)` (`events.go:132`) — sets `Version`/`Subject`/`DeliverySemantic`/`Replayable` from `DefinitionForType` and marshals the body in one call.
- `event.Event.UserID` is `uuid.UUID`, not a pointer (`events.go:87`).

**`internal/platform/txscope/interfaces.go`** — add accessor to `Scope`:

```go
CallMessages() repository.CallMessageRepository
```

(Slot in alphabetical order alongside `Calls()`, `Channels()`, `Messages()`.)

**`internal/repository/postgres/tx.go`** — extend `txScope` to hold a `callMessages *CallMessageRepo` and bind it in `WithinTx` exactly like `scope.calls = m.calls.withTx(tx)` at line 139. Add the `CallMessages()` accessor on `txScope` returning `ts.callMessages`.

**Service `SendCallMessage` flow** mirrors `chat.Service.SendMessage` lines 674-707 verbatim — tx path when `s.tx != nil`, non-tx fallback otherwise:

```go
if s.tx != nil {
    if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
        if err := scope.CallMessages().Create(ctx, msg); err != nil {
            return err
        }
        return s.enqueueCallMessageEventTx(ctx, scope, event.TypeCallMessageCreated, call, msg)
    }); err != nil {
        return nil, cerrors.Internal("failed to create call message", err)
    }
} else {
    if err := s.callMessages.Create(ctx, msg); err != nil {
        return nil, cerrors.Internal("failed to create call message", err)
    }
    s.publishCallMessageEvent(ctx, event.TypeCallMessageCreated, call, msg)
}
```

The new `enqueueCallMessageEventTx` and `publishCallMessageEvent` helpers slot beside `enqueueCallEventTx`/`publishCallEvent` at `service.go:1207-1265` and reuse the same `enqueueRealtimeTx`/`doPublish` plumbing. Subject is `fmt.Sprintf("aloqa.ws.%s", call.WorkspaceID)` for consistency with the other call events.

`DefinitionForType` does **not** require a new explicit case — the default branch (`events.go:121-126`) already returns `DeliveryAtLeastOnce + Replayable: true`, which is correct for persisted call messages. Adding an explicit case would be redundant.

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

No `DefinitionForType` case needed — the default branch (`events.go:121-126`) already returns `DeliveryAtLeastOnce + Replayable: true`. See the outbox plumbing section above for the publish flow.

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

Response shapes:

- POST → 201 + `CallMessage` (entity serialized directly via JSON tags)
- GET → 200 + `pagination.Page[CallMessage]` (canonical shape `{ items, next_cursor, has_more }`, matching chat's `buildMessagePage` at `chat/service.go:1529-1546`)
- DELETE → 204

Cursor format is base64-encoded UUID via `pagination.EncodeCursor` / `DecodeCursor` (`internal/pkg/pagination/pagination.go:37-54`). The handler decodes the `?cursor=` query param to a `uuid.UUID`, builds `pagination.Params{Cursor, Limit}`, calls `s.calls.ListCallMessages`, and a small `buildCallMessagePage(items, limit)` helper assembles the response (fetches `limit+1`, trims, encodes last item's ID).

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

`requireCallAccess` returns `(*entity.Call, error)` — the call is captured for downstream `Settings.Chat` / `Status` checks (`service.go:199-220`).

- `SendCallMessage`:
  1. `call, err := s.requireCallAccess(ctx, workspaceID, callID, senderID)` — workspace + channel boundary.
  2. `GetParticipant(ctx, callID, senderID)` must exist with `Status == ParticipantStatusConnected`. If missing → `cerrors.Forbidden("not in call")`.
  3. If `call.Settings.Chat == false` → `cerrors.Forbidden("call chat is disabled")`.
  4. If `call.Status == CallStatusEnded` → `cerrors.Forbidden("call is not active")` (matches `breakout.go:124` precedent).
  5. Nil-check `s.callMessages == nil` (service-layer) → `cerrors.Unavailable("call messaging is not configured")`. The handler does not need its own guard.
- `ListCallMessages`: `requireCallAccess` only. Even disconnected workspace members can fetch the transcript via `CallEndedSummary`. Same `s.callMessages == nil` guard.
- `DeleteCallMessage`:
  1. `requireCallAccess`.
  2. Fetch message; if `msg.SenderID != requesterID`, require host/co-host via `requireHostOrCoHost(ctx, callID, requesterID)` (`breakout.go:383`).
  3. Soft-delete + emit `call.message.deleted` through the same tx-or-fallback flow as Send.

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
4. **Route auto-exposure under `/api/v1/personal/...`** — `mountSharedScopedRoutes` is shared between workspace and personal scopes; the new `/messages` routes inherit both mounts. Intentional — personal-scope calls (1:1 between two users not in a workspace) also benefit from in-call chat. Tests cover both mount points.

## Open question for FE PR4 (ALOQA-211)

The architect should confirm before implementation that excluding reactions, edit, attachments, mentions, and link unfurl from call messages matches the FE in-call chat panel design. The BE repo cannot verify this. If the FE assumes any of those, that's a follow-up Jira.

## Out of scope (re-stated)

Reactions on call messages, edit, file attachments, image upload, @mentions, threading, server-side markdown rendering, link unfurl, message search, rate-limit middleware tuning.
