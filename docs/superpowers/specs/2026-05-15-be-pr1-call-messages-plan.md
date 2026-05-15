# BE-PR1 — call_messages Implementation Plan (ALOQA-242)

> Implementation plan for [PR1 spec](2026-05-15-be-pr1-call-messages-design.md).

## Branch + base

- Branch: `feature/ALOQA-242-call-messages` (carries spec + plan).
- Base: `origin/develop` at `434b752` (post-PR6 merge to develop).

## Step-by-step (commit after each)

### Step 1 — Migration + domain entity + repo interface

**Create:** `migrations/034_call_messages.sql` (up) and `migrations/down/034_call_messages.sql` (down) per spec §"Migration".

**Create:** `internal/domain/entity/call_message.go`:

```go
package entity

import (
    "time"

    "github.com/google/uuid"
)

type CallMessage struct {
    ID        uuid.UUID  `json:"id"`
    CallID    uuid.UUID  `json:"call_id"`
    SenderID  uuid.UUID  `json:"sender_id"`
    Body      string     `json:"body"`
    CreatedAt time.Time  `json:"created_at"`
    DeletedAt *time.Time `json:"deleted_at,omitempty"`
}
```

**Modify:** `internal/domain/repository/interfaces.go` — append `CallMessageRepository` after `CallRepository` (line 207):

```go
type CallMessageRepository interface {
    Create(ctx context.Context, msg *entity.CallMessage) error
    ListByCall(ctx context.Context, callID uuid.UUID, p pagination.Params) ([]entity.CallMessage, error)
    SoftDelete(ctx context.Context, id, callID uuid.UUID) error
    GetByID(ctx context.Context, id uuid.UUID) (*entity.CallMessage, error)
}
```

**Commit:** `feat(call-messages): migration + domain entity + repo interface`

### Step 2 — Postgres repository

**Create:** `internal/repository/postgres/call_message.go` mirroring `CallRepo` (`internal/repository/postgres/call.go`):

- `type CallMessageRepo struct { pool *pgxpool.Pool; db queryable }`
- `NewCallMessageRepo(pool *pgxpool.Pool) *CallMessageRepo`
- Unexported `withTx(tx pgx.Tx) *CallMessageRepo`
- `Create` — INSERT, populates `CreatedAt` if zero.
- `ListByCall` — `SELECT ... WHERE call_id = $1 AND deleted_at IS NULL AND ($2 = '00000000-...' OR id < $2) ORDER BY id DESC LIMIT $3`. Fetches `limit+1` so caller detects has-more.
- `SoftDelete` — `UPDATE call_messages SET deleted_at = NOW() WHERE id = $1 AND call_id = $2 AND deleted_at IS NULL` (idempotent: 0 rows affected if already deleted).
- `GetByID` — straight select; returns `cerrors.NotFound` on no rows.

**Commit:** `feat(call-messages): postgres repository with withTx`

### Step 3 — Txscope wiring

**Modify:** `internal/platform/txscope/interfaces.go` — add `CallMessages() repository.CallMessageRepository` to `Scope` (alphabetical, after `Calls()`).

**Modify:** `internal/platform/txscope/config.go` — add `CallMessages repository.CallMessageRepository` field to `TxManagerConfig` (alphabetical) and to the `txManager` struct that holds it.

**Modify:** `internal/repository/postgres/tx.go`:

- Add `callMessages *CallMessageRepo` field to `txScope` (line 60-75 area).
- Inside `WithinTx`, after `scope.calls = m.calls.withTx(tx)` (line 139), add `scope.callMessages = m.callMessages.withTx(tx)`.
- Add `func (s *txScope) CallMessages() repository.CallMessageRepository { return s.callMessages }`.
- Bind `m.callMessages` from `TxManagerConfig.CallMessages` in `NewTxManager`.

**Modify:** `cmd/server/main.go`:

```go
callMessageRepo := postgres.NewCallMessageRepo(pool)
callSvc.SetCallMessageRepo(callMessageRepo) // method added in Step 5
txConfig.CallMessages = callMessageRepo
```

Insert near existing `messageRepo`/`callRepo` construction lines.

**Commit:** `feat(call-messages): txscope wiring`

### Step 4 — Domain event types + payloads

**Modify:** `internal/domain/event/events.go` — append after `TypeCallQualityAdapted` (line 48):

```go
TypeCallMessageCreated Type = "call.message.created"
TypeCallMessageDeleted Type = "call.message.deleted"
```

Add payload types in the payload section (after `CallPayload`):

```go
type CallMessagePayload struct {
    CallID  uuid.UUID          `json:"call_id"`
    Message entity.CallMessage `json:"message"`
}

type CallMessageDeletedPayload struct {
    CallID    uuid.UUID `json:"call_id"`
    MessageID uuid.UUID `json:"message_id"`
}
```

No `DefinitionForType` change needed — default branch returns `DeliveryAtLeastOnce + Replayable: true`, which is correct.

**Commit:** `feat(call-messages): event types + payloads`

### Step 5 — Service field + setter + helpers

**Modify:** `internal/service/call/service.go`:

- Add field `callMessages repository.CallMessageRepository` to `Service` struct.
- Add setter near `SetMediaControlPlane` / `SetTransactionManager` (lines 109-114):

```go
func (s *Service) SetCallMessageRepo(repo repository.CallMessageRepository) {
    s.callMessages = repo
}
```

- Add helpers next to `publishCallEvent` / `enqueueCallEventTx` (around line 1207-1265):

```go
func (s *Service) publishCallMessageEvent(ctx context.Context, evtType event.Type, call *entity.Call, msg *entity.CallMessage) {
    channelID := uuid.Nil
    if call.ChannelID != nil { channelID = *call.ChannelID }
    subject := fmt.Sprintf("aloqa.ws.%s", call.WorkspaceID)
    payload := callMessagePayloadFor(evtType, call, msg)
    s.doPublish(ctx, evtType, subject, call.WorkspaceID, channelID, msg.SenderID, payload)
}

func (s *Service) enqueueCallMessageEventTx(ctx context.Context, scope txscope.Scope, evtType event.Type, call *entity.Call, msg *entity.CallMessage) error {
    channelID := uuid.Nil
    if call.ChannelID != nil { channelID = *call.ChannelID }
    subject := fmt.Sprintf("aloqa.ws.%s", call.WorkspaceID)
    payload := callMessagePayloadFor(evtType, call, msg)
    return s.enqueueRealtimeTx(ctx, scope, evtType, subject, call.WorkspaceID, channelID, msg.SenderID, payload)
}

func callMessagePayloadFor(evtType event.Type, call *entity.Call, msg *entity.CallMessage) any {
    if evtType == event.TypeCallMessageDeleted {
        return event.CallMessageDeletedPayload{CallID: call.ID, MessageID: msg.ID}
    }
    return event.CallMessagePayload{CallID: call.ID, Message: *msg}
}
```

**Commit:** `feat(call-messages): service field + setter + publish/enqueue helpers`

### Step 6 — Service methods

**Create:** `internal/service/call/message.go` with three public methods per spec §"Service surface":

- `SendCallMessage` — access checks (steps 1-4 in spec), build entity, tx-or-fallback flow mirroring `chat/service.SendMessage:674-707`.
- `ListCallMessages` — access check, `s.callMessages == nil` guard, call repo with pagination params.
- `DeleteCallMessage` — access check, fetch message, sender-or-host check via `requireHostOrCoHost`, tx-or-fallback soft-delete + event.

Validation helpers:

```go
const (
    callMessageMaxBody = 2000
)

func validateCallMessageBody(body string) error {
    trimmed := strings.TrimSpace(body)
    if trimmed == "" {
        return cerrors.InvalidInput("body is required")
    }
    if len(trimmed) > callMessageMaxBody {
        return cerrors.InvalidInput("body too long")
    }
    if !utf8.ValidString(body) {
        return cerrors.InvalidInput("body must be valid UTF-8")
    }
    return nil
}
```

The handler trims at decode-time, the service stores the trimmed value.

**Commit:** `feat(call-messages): SendCallMessage / ListCallMessages / DeleteCallMessage`

### Step 7 — HTTP handlers + router

**Create:** `internal/handler/http/call_message.go` with three handlers (`SendCallMessage`, `ListCallMessages`, `DeleteCallMessage`):

- Read `workspaceID` via `middleware.WorkspaceIDFromContext`.
- Read `callID` / `messageID` via `chi.URLParam` + `uuid.Parse`.
- Decode `?cursor=` to `pagination.Params{Cursor, Limit}` via `pagination.DecodeCursor`.
- Build `pagination.Page[entity.CallMessage]` from the repo's `limit+1` slice using a `buildCallMessagePage(items, limit)` helper (mirror `chat/service.go:1529-1546`).
- Respond 201/200/204 per spec.

**Modify:** `internal/handler/http/router.go` — inside `r.Route("/{callID}", ...)` block (line 275-282 area), add:

```go
r.Route("/messages", func(r chi.Router) {
    r.Post("/", deps.Calls.SendCallMessage)
    r.Get("/", deps.Calls.ListCallMessages)
    r.Delete("/{messageID}", deps.Calls.DeleteCallMessage)
})
```

The block is mounted under both workspace and personal scope via `mountSharedScopedRoutes`; both paths get the new routes automatically (intentional per spec).

**Commit:** `feat(call-messages): HTTP handlers + router`

### Step 8 — Repo tests

**Create:** `internal/repository/postgres/call_message_test.go` using the standard postgres test harness (matches `message_test.go` / `call_test.go`):

- Round-trip Create → GetByID.
- ListByCall newest-first, excludes soft-deleted.
- Cursor pagination 2-page walk over 5 messages.
- SoftDelete idempotent.
- ON DELETE CASCADE from `calls`.
- Body-length CHECK rejects 0-char and 2001-char.

Run: `go test ./internal/repository/postgres/... -run CallMessage`

**Commit:** `test(call-messages): postgres repository tests`

### Step 9 — Service tests

**Create:** `internal/service/call/message_test.go` using hand-rolled fakes (matches `service_test.go`):

- `fakeCallMessageRepo` — in-memory map keyed by message UUID.
- `fakeTxManager` — invokes the closure with a scope that holds the fake repos.
- All cases per spec §"Test plan / Service": send/list/delete happy paths, access errors (`Forbidden`), validation errors (`InvalidInput`), missing-message error (`NotFound`), chat-disabled, ended-call.
- Assert outbox enqueue carries `TypeCallMessageCreated` / `TypeCallMessageDeleted`.

Run: `go test ./internal/service/call/... -run CallMessage`

**Commit:** `test(call-messages): service tests`

### Step 10 — HTTP tests

**Create:** `internal/handler/http/call_message_test.go`:

- 201 on POST + body present.
- 400 on malformed JSON / empty body / oversize body.
- 401 unauthenticated, 403 not-in-call.
- 200 on GET with cursor pagination; opaque cursor round-trips.
- 204 on DELETE.

Run: `go test ./internal/handler/http/... -run CallMessage`

**Commit:** `test(call-messages): HTTP handler tests`

### Step 11 — Full verify

```bash
go vet ./...
go test ./... -timeout 60s
go build ./cmd/server
```

Fix any failures and commit: `chore(call-messages): verify gate fixes`

### Step 12 — Stop and report

Print:

```
READY FOR PR
Last commit: <sha>
Files changed: <list>
Tests added: <count>
```

## Verification gates

| Gate | When | Command |
|---|---|---|
| Migration | after Step 1 | `psql` apply + revert smoke |
| Repo | after Step 8 | `go test ./internal/repository/postgres/... -run CallMessage` |
| Service | after Step 9 | `go test ./internal/service/call/... -run CallMessage` |
| HTTP | after Step 10 | `go test ./internal/handler/http/... -run CallMessage` |
| Full | after Step 11 | `go vet ./... && go test ./... && go build ./cmd/server` |

## Out of scope (re-stated)

Reactions on call messages, edit, attachments, mentions, threading, server-side markdown rendering, link unfurl, message search, per-user rate-limit middleware.

## Risks

1. **Migration ordering with concurrent feature work** — migration 034 must not collide with any other PR in flight. Verify before starting impl: latest in `migrations/` is `033_event_reminders_definition_constraint.sql`.
2. **`mountSharedScopedRoutes` double-mount** — routes appear under both `/api/v1/workspaces/{workspaceID}/...` and `/api/v1/personal/...`. Tests cover both; spec acknowledges this is intentional.
3. **FE PR4 (ALOQA-211) cross-cutting** — the architect should confirm scope exclusions (no reactions/edit/attachments on call messages) before plan execution starts.
