# ALOQA-257 — BE: `forwarded_from` JSONB on Message (R2)

**Status:** Spec R2 (rewritten after schema-constraint review)
**Date:** 2026-05-18
**Repo:** aloqa-claude (backend)
**Parent FE ticket:** ALOQA-175 in `aloqa-frontend`. See FE spec at `aloqa-frontend/docs/superpowers/specs/2026-05-18-aloqa-175-forward-message-design.md` §4 for the contract.
**Profile:** `pr-sync --codex-led`.
**Scope cut from R1:** `attachment_ids` removed from MVP. R1 review uncovered a `UNIQUE` constraint on `attachments.storage_path` (migration 010) that blocks the duplicate-row approach. Cross-channel attachment download lives in follow-up ticket **ALOQA-262**.

---

## 1. Goal

Add a single optional field on `POST /api/v1/workspaces/{wsId}/channels/{chId}/messages` (and on the `Message` entity returned everywhere):

- `forwarded_from` (JSONB) — full self-contained snapshot of the original message (root, after FE-side collapse). Persisted verbatim, returned verbatim. Server does NOT parse the internal shape — FE owns it per ALOQA-175 §12.5 and [[aloqa_backend_changes_allowed]].

When absent the response shape is byte-identical to today's (omitempty / NULL).

**Out of scope (filed as ALOQA-262):** attachment re-reference on forwarded messages. FE renders attachment chips visually from `forwarded_from.snapshot.attachments`; download of those chips falls back to the existing `/attachments/{id}` endpoint, which gates on source-channel membership. A forward from a private channel into a DM will show the chip but the recipient sees a 403 on download. Known UX limitation, acceptable for MVP.

**Out of scope (legacy validation):** the existing `content min=1` rule is preserved as a default but **conditionally relaxed** when `forwarded_from` is set, so a comment-less forward (`content=""`) is accepted. This is the only validator change required.

This PR is **backend-only**. No FE code touched here.

---

## 2. Context

### Existing Message entity (`internal/domain/entity/message.go`)

Existing fields: `ID`, `ChannelID`, `UserID`, `ParentID`, `Content`, `Type`, `Edited`, `EditedAt`, `Pinned`, `PinnedBy`, `PinnedAt`, `CreatedAt`, `UpdatedAt`, `DeletedAt`, plus aggregations (`ReplyCount`, `Reactions`, `Attachments`, `User`).

### Existing POST handler request (`internal/handler/http/message.go:25-28`)

```go
type sendMessageRequest struct {
    Content  string  `json:"content" validate:"required,min=1,max=40000"`
    ParentID *string `json:"parent_id,omitempty"`
}
```

The handler calls `decodeJSON(r, &req)` (line 38) and **does not call `validate.Struct` itself** — validation runs at the service layer where `validate.Struct(input)` is invoked (`internal/service/chat/service.go:632` and similar at L248, L318). Adding `validate` tags to the service input struct is sufficient to enforce the rule on every send path. **Do NOT add a handler-level `validate.Struct` call** — it would double-validate and could change error shapes.

### Service `SendMessage` signature (`internal/service/chat/service.go:625-630`)

```go
func (s *Service) SendMessage(
    ctx context.Context,
    channelID, userID uuid.UUID,
    content string,
    parentID *uuid.UUID,
) (*entity.Message, error)
```

### Service `SendMessageInput` (`internal/service/chat/service.go:236-238`)

```go
type SendMessageInput struct {
    Content string `validate:"required,min=1,max=40000"`
}
```

Currently used at L632 via `validate.Struct(input)`. Extension below.

### Repository `Create` (`internal/repository/postgres/message.go:38-64`)

INSERTs into `messages`. Migration tool: golang-migrate SQL files in `migrations/NNN_<name>.sql`. Next free migration: **`035`**.

### Existing access policy

`internal/service/chat/service.go` already exposes:
- `requireMessageAccess(ctx, messageID, userID)` — proper capability check.
- `GetAccessibleChannel(ctx, channelID, userID)` — public/private/grant-aware.

Per FE spec §12.5 we do NOT need to verify the source message of `forwarded_from.snapshot.message_id` — backend trusts FE. Only the TARGET channel access is enforced (existing behaviour, unchanged).

---

## 3. Schema change

### Migration `035_messages_forward_from.sql`

```sql
BEGIN;

-- ALOQA-257: forward message support — persist forwarded_from snapshot JSON
ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS forwarded_from jsonb DEFAULT NULL;

-- Reverse lookup index: "all forwards of message X". Cheap to maintain, useful for moderation.
CREATE INDEX IF NOT EXISTS idx_messages_forwarded_from_message_id
    ON messages ((forwarded_from->>'message_id'))
    WHERE forwarded_from IS NOT NULL;

COMMIT;
```

Down migration `migrations/down/035_messages_forward_from.down.sql`:

```sql
BEGIN;
DROP INDEX IF EXISTS idx_messages_forwarded_from_message_id;
ALTER TABLE messages DROP COLUMN IF EXISTS forwarded_from;
COMMIT;
```

Both `ADD COLUMN` and `CREATE INDEX` use `IF NOT EXISTS` so the up migration is **idempotent** (rerun safe).

---

## 4. Code changes

### 4.1 Entity (`internal/domain/entity/message.go`)

Add one field to `Message`:

```go
// JSON-typed forwarded-from envelope. Persisted verbatim in the messages.forwarded_from
// jsonb column, returned verbatim. Server does not parse the internal structure — FE owns
// the schema per ALOQA-175 spec §4.
ForwardedFrom json.RawMessage `json:"forwarded_from,omitempty" db:"forwarded_from"`
```

`omitempty` ensures the response omits the field when NULL — required for §8.5 backward compatibility.

### 4.2 HTTP handler request (`internal/handler/http/message.go:25-28`)

```go
type sendMessageRequest struct {
    Content       string          `json:"content"`
    ParentID      *string         `json:"parent_id,omitempty"`
    ForwardedFrom json.RawMessage `json:"forwarded_from,omitempty"`
}
```

**Removed `validate:"required,min=1,max=40000"` from the request struct** — validation moves to the service input (§4.3) where conditional content-emptiness is expressible. Handler still calls `decodeJSON`; service validates. The handler resolves `*string` ParentID → `*uuid.UUID` before calling service.

### 4.3 Service input (`internal/service/chat/service.go:236-238`)

```go
type SendMessageInput struct {
    Content       string          // length validated in §4.4 by SendMessage, not by tag
    ParentID      *uuid.UUID
    ForwardedFrom json.RawMessage // nil when not a forward; persisted verbatim when set
}
```

**Removed the `validate:"required,min=1,max=40000"` tag** because the rule is now conditional (see §4.4). All other Input structs in the file keep their tags — only `SendMessageInput` changes.

### 4.4 Service `SendMessage` — conditional content validation + signature

Switch to a struct-input signature (cleaner evolution for new optional fields):

```go
func (s *Service) SendMessage(
    ctx context.Context,
    channelID, userID uuid.UUID,
    input SendMessageInput,
) (*entity.Message, error) {
    // Conditional content validation:
    // - When forwarded_from is set, content may be empty (= comment-less forward).
    // - Otherwise, content must be non-empty (legacy behaviour).
    if len(input.ForwardedFrom) == 0 {
        if len(input.Content) < 1 {
            return nil, ErrValidation("content is required")
        }
    }
    if len(input.Content) > 40000 {
        return nil, ErrValidation("content must be at most 40000 characters")
    }

    // If forwarded_from is set, assert it parses as valid JSON (defense-in-depth: the
    // jsonb column would reject invalid JSON anyway, but we want a clean 400 not a 500).
    if len(input.ForwardedFrom) > 0 {
        var probe interface{}
        if err := json.Unmarshal(input.ForwardedFrom, &probe); err != nil {
            return nil, ErrValidation("forwarded_from must be valid JSON")
        }
    }

    // Existing logic (channel access check, parent validation, insert) unchanged
    // except the message entity now carries ForwardedFrom into the repository call.
    msg := &entity.Message{
        // ...existing fields...,
        ForwardedFrom: input.ForwardedFrom, // nil propagates to NULL in jsonb
    }
    return s.repo.Create(ctx, msg)
}
```

(`ErrValidation` is whatever the existing repo uses for 400-mapped errors — see existing `requireMessageAccess` error returns.)

**No change to the existing channel access enforcement** for the target channel. **No new check against the snapshot's source `message_id` / `channel_id`** per §8.4.

### 4.5 Repository `Create` (`internal/repository/postgres/message.go:38-64`)

- Add `forwarded_from` to the INSERT column list and the VALUES placeholder list (positional or named binding — match the existing style).
- Bind `entity.Message.ForwardedFrom` (pgx handles `json.RawMessage` → jsonb natively because `RawMessage` implements `[]byte` underneath).
- Make `forwarded_from` selectable in **all** SELECT queries that load Message (`GetByID`, `ListByChannel`, thread queries, search). Add `forwarded_from` to the SELECT column list. pgxscan row-mapping handles `*json.RawMessage` (or scan `[]byte` then assign — match the existing style in the file).
- `nil`-safe: empty `RawMessage` ↔ NULL column.

### 4.6 Events (`internal/domain/event/events.go`)

**No code change.** `MessagePayload.Message` is `*entity.Message`; the new `ForwardedFrom` field flows automatically through JSON marshaling. `message.created` event payload includes the field on forwarded messages.

### 4.7 No attachment changes

`attachment_ids` is NOT in the request, NOT in the service input, NOT processed. Forwarded messages have an empty `Attachments` array (no rows in `attachments` table reference the new `message_id`). FE renders the visual attachment list from `forwarded_from.snapshot.attachments`. Download remains gated by the source-channel access policy. **See ALOQA-262 for the proper cross-channel-attachment fix.**

---

## 5. Validation and error responses

| Condition | HTTP | Body |
|---|---|---|
| `content` empty AND `forwarded_from` absent | 400 | `{"error":"content is required"}` |
| `content` > 40000 chars | 400 | `{"error":"content must be at most 40000 characters"}` |
| `forwarded_from` is non-empty but not valid JSON | 400 | `{"error":"forwarded_from must be valid JSON"}` |
| Target channel send permission fails | 403 | (existing behaviour, unchanged) |
| `forwarded_from` is a JSON value of arbitrary shape (e.g., scalar, missing keys) | 201 | Persisted verbatim. **Trust FE per §8.4.** |

---

## 6. Performance + storage notes

- Single new column, mostly NULL (only forwarded messages carry it).
- Expression index on `(forwarded_from->>'message_id')` filtered `WHERE forwarded_from IS NOT NULL` — sparse, cheap.
- Realtime broadcast payload grows by ~500 bytes per forwarded message (snapshot content + attachment metadata). Acceptable.
- No N+1.

---

## 7. Test plan

### Unit (`internal/service/chat/service_test.go`)

| # | Case | Expected |
|---|---|---|
| U1 | `SendMessage` without `ForwardedFrom`, `Content="hi"` | Inserted with `forwarded_from=NULL`. Returned Message has nil/empty `ForwardedFrom`. |
| U2 | `SendMessage` with `ForwardedFrom=<valid json object>`, `Content="comment"` | Inserted with `forwarded_from=<blob>`. Returned `Message.ForwardedFrom` deep-equal to input. |
| U3 | `SendMessage` with `ForwardedFrom=<valid json>`, `Content=""` (comment-less forward) | Accepted — 201 / no validation error. Persisted with `content=""`, `forwarded_from=<blob>`. |
| U4 | `SendMessage` with `Content=""` AND `ForwardedFrom=nil` (legacy regression) | 400 `content is required`. No row inserted. |
| U5 | `SendMessage` with `Content` of length 40001 | 400 `content must be at most 40000 characters`. |
| U6 | `SendMessage` with `ForwardedFrom=[]byte("not json")` | 400 `forwarded_from must be valid JSON`. No row inserted. |
| U7 | `SendMessage` with `ForwardedFrom=[]byte("\"a string\"")` (valid JSON scalar) | Accepted; persisted verbatim. Trust contract per §8.4. |
| U8 | `SendMessage` with `ForwardedFrom=[]byte("{}")` (empty object) | Accepted; persisted verbatim. |
| U9 | Soft-delete privacy regression: insert message with `ForwardedFrom=<jsonb>`, call `MessageRepo.SoftDelete(ctx, id)`, then `GetByID(ctx, id)` | Returned Message has `Content=""` AND `ForwardedFrom == nil`. Snapshot must NOT leak through after soft-delete (§8.11). |
| U10 | Multi-byte content boundary: `SendMessage` with `Content = strings.Repeat("я", 40000)` (40k Cyrillic chars = ~80k bytes) | Accepted (40000 runes, exactly at limit). Verifies `utf8.RuneCountInString` is used, not `len()` (§8.12). |
| U11 | Multi-byte content over-limit: `SendMessage` with `Content = strings.Repeat("я", 40001)` | 400 "content must be at most 40000 characters". |
| U12 | EditMessage validation unchanged: call `EditMessage(ctx, msgID, userID, "")` | 400 — EditMessage still rejects empty content (§8.13, separate EditMessageInput struct preserves legacy tag). |

### Integration (handler-level HTTP)

| # | Case | Expected |
|---|---|---|
| I1 | `POST` body `{"content":"hi","forwarded_from":{"user_id":"u1","message_id":"m1","channel_id":"c1","created_at":"...","snapshot":{"content":"...","attachments":[]}}}` | 201; response JSON has `forwarded_from` echo (deep-equal). |
| I2 | `POST` body `{"content":""}` (no `forwarded_from`) | 400 `content is required`. |
| I3 | `POST` body `{"content":"","forwarded_from":{"user_id":"u1",...}}` | 201; comment-less forward accepted. |
| I4 | `POST` body `{"content":"hi"}` (legacy) | 201; response JSON does NOT include `forwarded_from` (omitempty preserved). |
| I5 | `GET /messages/{id}` after a forward POST | Response includes `forwarded_from`. |
| I6 | Realtime `message.created` event after a forward POST | Payload includes `forwarded_from`. |

### Migration

| # | Case | Expected |
|---|---|---|
| M1 | Run `035_messages_forward_from.sql` on a populated dev DB | Column added (NULL default), existing rows untouched, no data loss. |
| M2 | Run down migration after up | Column removed, no data loss for other columns. |
| M3 | Run up migration twice in a row | Second run is a no-op (`IF NOT EXISTS` on both column add and index create). |

---

## 8. DO-NOT-DRIFT invariants

1. **Migration uses `IF NOT EXISTS`** on both `ADD COLUMN` and `CREATE INDEX`. Test M3.
2. **`forwarded_from` is `omitempty`** on the Message JSON response — absent field when NULL. Test I4, U1.
3. **Content validation is conditional**: required (length ≥ 1) when `ForwardedFrom` is empty; allowed-empty when `ForwardedFrom` is non-empty. Test U3, U4.
4. **No source-channel membership check on `forwarded_from.snapshot.message_id` / `channel_id`** — FE-trusted per ALOQA-175 §12.5.
5. **`omitempty` preserved on the request struct** — legacy `{"content":"hi"}` body unchanged behaviour. Test I4.
6. **No attachment_ids handling** in this PR — explicitly out of scope per §1, follow-up ALOQA-262. Reject any code that touches the `attachments` table on forward.
7. **Realtime broadcast carries `forwarded_from`** automatically via JSON marshaling — no event payload struct change. Test I6.
8. **No external dependencies** added.
9. **Service-level `validate.Struct` is NOT removed for SendMessageInput** — but its `Content` tag is replaced by the manual conditional check in `SendMessage`. Other input structs keep validators. (If the file uses one shared `validate.New()` instance — confirm no cross-impact.)
10. **`forwarded_from` non-empty implies valid JSON** — service rejects 400 before INSERT; jsonb column never receives garbage. Test U6.
11. **Soft-delete clears `forwarded_from`** — `MessageRepo.SoftDelete` UPDATE statement extends the existing field reset (`content = ''`, `pinned = false`, etc.) with `forwarded_from = NULL`. Without this, deleted forwarded messages still expose the embedded snapshot content. Test U9.
12. **Content length is rune-counted, not byte-counted** — the manual `SendMessage` check uses `utf8.RuneCountInString(input.Content)` (matches go-playground/validator semantics). `len()` is forbidden because it counts bytes and would silently reject valid multi-byte content (Russian, Uzbek-cyrl). Test U10, U11.
13. **EditMessage validation unchanged** — a separate `EditMessageInput` struct preserves the legacy `validate:"required,min=1,max=40000"` tag for edit flows. Removing the tag from `SendMessageInput` does NOT affect EditMessage. Test U12.

---

## 9. Acceptance criteria

1. All §7 unit + integration + migration tests pass.
2. `make lint && make test` green.
3. Manual smoke (§9 below) executed against the new build.
4. Realtime `message.created` payload includes `forwarded_from` on a forwarded message (verified by tailing websocket on a member account).
5. Legacy `POST /messages` body returns the same response shape — backward compat regression.
6. All 10 §8 DO-NOT-DRIFT invariants pass review.

### Manual test cases (will be `state.manual_test_cases`)

| # | Title | Steps | Expected |
|---|---|---|---|
| 1 | Legacy send | `POST .../messages` body `{"content":"hi"}` | 201, response shape unchanged. No `forwarded_from` in JSON. |
| 2 | Forward with comment | `POST` body `{"content":"FYI","forwarded_from":{"user_id":"u1","message_id":"m1","channel_id":"c1","created_at":"2026-05-15T10:42:00Z","snapshot":{"content":"original body","attachments":[]}}}` | 201; response echoes `forwarded_from` deep-equal. |
| 3 | Comment-less forward | Same as #2 but `content=""` | 201; persisted with empty content. |
| 4 | Empty content, no forward | `POST` body `{"content":""}` | 400 `content is required`. |
| 5 | Invalid forwarded_from JSON | `POST` body `{"content":"x","forwarded_from":"not-json"}` (string-escaped invalid JSON) | The string is itself valid JSON; persisted. To trigger the 400, send raw bytes via curl with a malformed JSON payload — verify 400 from json decoder. |
| 6 | Realtime delivery | Subscribe to channel WS, then POST forward | `message.created` event payload includes `forwarded_from` deep-equal. |
| 7 | Idempotent migration | Apply `035` migration up, then apply up again | Second `up` is a no-op (no error). |
| 8 | Down migration | Apply `035` migration down | Column dropped, no data loss in other columns. |

---

## 10. Code-review prompt augmentation

Per [[aloqa_168_merge_codex_led]] lesson. The Codex reviewer brief MUST inline-enumerate:
1. Every §7 case (U1–U8, I1–I6, M1–M3) and verify each has a matching test.
2. Every §8 DO-NOT-DRIFT invariant (1–10) and verify the code upholds it.
3. The `IF NOT EXISTS` guard on migration (§8.1).
4. The conditional content-validation logic in §4.4 (§8.3).
5. The omitempty preservation (§8.2, §8.5).
6. Confirmation that NO attachment_ids handling exists in the PR diff (§8.6).
7. `forwarded_from` reaches the realtime event payload (§8.7).

---

## 11. Out of scope (filed)

- **ALOQA-262** Forward: cross-channel attachment access (BE join-table or blob copy) — proper fix for visible-but-not-downloadable chips in cross-channel forwards.

Related: [[aloqa_calls_backend_session_pause_2026_05_15]], [[aloqa_backend_changes_allowed]], [[vultr_staging_deploy_2026_05_14]].
