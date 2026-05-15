# BE-PR1 — call_messages Resource (ALOQA-242)

> Backend resource powering in-call chat (FE PR4 ALOQA-211). Adds the persistence + WS broadcast required for the frontend in-call chat panel and the `CallEndedSummary` transcript.

## Goal

Add a first-class `call_messages` resource that:

1. Persists messages sent during a call (sender, body, timestamps).
2. Broadcasts `call.message.created` / `call.message.deleted` to all in-call participants in real time.
3. Survives the call so the FE `CallEndedSummary` can render the transcript.
4. Is gated by the existing `Call.Settings.Chat` flag (`internal/domain/entity/call.go:54`).

Out of scope: editing, reactions on call messages, threading, file attachments, message search.

## Surface

### Domain entity (`internal/domain/entity/call_message.go`, new)

```go
type CallMessage struct {
    ID         uuid.UUID
    CallID     uuid.UUID
    SenderID   uuid.UUID
    Body       string     // 1..2000 chars after trim
    CreatedAt  time.Time
    DeletedAt  *time.Time // soft delete
}
```

Validation: `strings.TrimSpace(Body)` must be non-empty and ≤ 2000 chars. UTF-8 must be valid. Body is stored verbatim (no markdown rewriting on the server — FE renders).

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

CREATE INDEX idx_call_messages_call_id_created_at ON call_messages (call_id, created_at DESC);
CREATE INDEX idx_call_messages_sender ON call_messages (sender_id);
```

Down migration drops indexes + table.

### Repository (`internal/repository/postgres/call_message.go`, new)

```go
type CallMessageRepo struct { db *pgxpool.Pool }

func (r *CallMessageRepo) Create(ctx context.Context, msg *entity.CallMessage) error
func (r *CallMessageRepo) ListByCall(
    ctx context.Context,
    callID uuid.UUID,
    cursor *time.Time,    // nil = newest first
    limit int,            // clamped [1, 100]
) ([]entity.CallMessage, *time.Time, error) // returns next cursor or nil
func (r *CallMessageRepo) SoftDelete(ctx context.Context, id, callID uuid.UUID) error
func (r *CallMessageRepo) GetByID(ctx context.Context, id uuid.UUID) (*entity.CallMessage, error)
```

`ListByCall` returns messages ordered by `created_at DESC`, excludes `deleted_at IS NOT NULL` rows. Cursor pagination over `created_at` (per CODE_STANDARDS).

### Service (`internal/service/call/message.go`, new)

Lives alongside `service.go`. The existing `Service` struct gains a `messages CallMessageRepo` dependency through `NewService` (option pattern is already used for `breakouts`/`media`).

```go
func (s *Service) SendCallMessage(
    ctx context.Context,
    workspaceID, callID, senderID uuid.UUID,
    body string,
) (*entity.CallMessage, error)

func (s *Service) ListCallMessages(
    ctx context.Context,
    workspaceID, callID, requesterID uuid.UUID,
    cursor *time.Time,
    limit int,
) ([]entity.CallMessage, *time.Time, error)

func (s *Service) DeleteCallMessage(
    ctx context.Context,
    workspaceID, callID, requesterID, messageID uuid.UUID,
) error
```

Access checks:

- `SendCallMessage`: caller must be `connected` in CallParticipant (use existing `GetParticipant` + status check). Reuse `requireCallAccess` for workspace/channel boundary. If `call.Settings.Chat == false`, return `cerrors.CodeForbidden` with reason `chat_disabled`.
- `ListCallMessages`: any user with workspace access via `requireCallAccess` — supports historical view from `CallEndedSummary` after leaving.
- `DeleteCallMessage`: must be sender OR participant with role host/co_host. Reuse `requireHostOrCoHost` for the host path.

WS broadcast:

- On send: publish `call.message.created` event with payload `{ message: CallMessage }` to `aloqa.ws.<workspaceID>` (consistent with existing `call.*` events at `service.go:1213`). Delivery semantic `at_least_once` (replayable — these are persisted). Uses the same outbox pattern as `publishCallEvent`.
- On delete: publish `call.message.deleted` with payload `{ message_id, call_id }`.

Add new event-type constants in `internal/domain/event/events.go` next to existing `call.*` group:

```go
const (
    TypeCallMessageCreated Type = "call.message.created"
    TypeCallMessageDeleted Type = "call.message.deleted"
)
```

Add payload types `CallMessagePayload` and `CallMessageDeletedPayload`.

### HTTP handlers (`internal/handler/http/call_message.go`, new)

Wire into existing `router.go` `/calls/{callID}/...` block (line 275–322 area):

```
POST   /workspaces/{wsID}/calls/{callID}/messages         → SendCallMessage
GET    /workspaces/{wsID}/calls/{callID}/messages         → ListCallMessages [?limit, ?before]
DELETE /workspaces/{wsID}/calls/{callID}/messages/{msgID} → DeleteCallMessage
```

Request bodies:

```json
POST  { "body": "<1..2000 chars>" }
```

Response shapes (snake_case JSON to match repo convention):

```json
{
  "id": "...",
  "call_id": "...",
  "sender_id": "...",
  "body": "...",
  "created_at": "RFC3339",
  "deleted_at": null
}
```

List response: `{ "messages": [...], "next_cursor": "RFC3339" | null }`.

Errors map to existing `cerrors` codes: `CodeForbidden` for non-participant / chat-disabled / wrong sender on delete, `CodeNotFound` for unknown call/message, `CodeInvalidInput` for empty/oversize body, `CodeConflict` for "call already ended" (we still allow send while connected; only block after `call.Status = ended`).

### Lifecycle interaction

- `EndCall` does **not** delete messages — the FE needs them for `CallEndedSummary`. Soft-delete on call deletion is handled by the FK `ON DELETE CASCADE` (calls are themselves hard-deleted only via admin tooling, never in normal flow).
- A participant who has LEFT can still call `ListCallMessages` while the call is active (workspace-scope access). This is intentional for late joiners to backfill.

## Test plan

### Repo (`internal/repository/postgres/call_message_test.go`)

- Round-trip Create → GetByID matches.
- ListByCall returns newest-first, excludes soft-deleted.
- Cursor pagination: 2-page walk over 5 messages with `limit=3`.
- SoftDelete is idempotent (calling twice does not error).
- ON DELETE CASCADE from `calls` removes messages (use a tx that deletes the parent call).
- Body length boundaries (1, 2000, 2001 — last must fail at insert).

### Service (`internal/service/call/message_test.go`)

Use the existing fake-repo pattern (in-memory map keyed by UUID).

- Send happy path → returns persisted message + publishes `call.message.created`.
- Send when caller not in CallParticipant → `CodeForbidden`.
- Send when caller in CallParticipant status=disconnected → `CodeForbidden`.
- Send when `Call.Settings.Chat == false` → `CodeForbidden` with reason `chat_disabled`.
- Send when call.Status=ended → `CodeConflict`.
- Send empty body → `CodeInvalidInput`.
- Send 2001-char body → `CodeInvalidInput`.
- List returns messages even after caller leaves.
- Delete by sender → soft-deletes + publishes `call.message.deleted`.
- Delete by host (not sender) → succeeds.
- Delete by random workspace member (not sender, not host) → `CodeForbidden`.
- Delete unknown message → `CodeNotFound`.

### HTTP (`internal/handler/http/call_message_test.go`)

- 201 on POST, body present, broadcast fired.
- 400 on malformed JSON / empty body / oversize body.
- 401 unauthenticated, 403 not-in-call.
- 200 on GET with cursor pagination.
- 204 on DELETE.

## Open questions (defer to plan stage)

- Pagination cursor format — RFC3339 timestamp string vs base64-encoded `(created_at, id)` to handle ties at the same nanosecond. Recommend the latter to avoid duplicates when multiple messages share `created_at`.
- WS payload size — entire CallMessage (~200 bytes) is fine; we don't need a slim variant.
- Rate limit — defer to operations layer (existing chat rate limit middleware may apply if hung off the same route group).

## Risks

1. **Outbox replay duplication** — `at_least_once` semantics means FE may receive duplicate `call.message.created` for a single send. FE dedupes by message ID. Out of scope here; FE PR4 owns this.
2. **Soft-delete + cascade interaction** — if a host hard-deletes the underlying call (admin), messages vanish. Acceptable.
3. **Migration ordering** — must apply 034 after 033 cleanly. No schema dependencies beyond `calls(id)` and `users(id)`.

## Out of scope

- Reactions on call messages (chat has reactions, but call messages do not — keeps the WS surface flat for now)
- Edit
- File attachments / images
- @mentions
- Threading
- Server-side markdown rendering / link unfurl
- Message search
