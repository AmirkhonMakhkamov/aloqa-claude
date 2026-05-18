# ALOQA-257 — BE: `forwarded_from` + `attachment_ids` on Message

**Status:** Spec
**Date:** 2026-05-18
**Repo:** aloqa-claude (backend)
**Parent FE ticket:** ALOQA-175 in `aloqa-frontend`. See FE spec at `aloqa-frontend/docs/superpowers/specs/2026-05-18-aloqa-175-forward-message-design.md` §4 for the canonical contract.
**Profile:** `pr-sync --codex-led`. Claude (architect) writes spec + plan; Codex gpt-5.5 high handles implement → review → fix → PR → merge.

---

## 1. Goal

Extend the backend message domain + POST handler so the FE can publish forwarded messages and re-reference existing attachments in a new message without re-uploading the blobs.

Two **optional** request fields on `POST /api/v1/workspaces/{wsId}/channels/{chId}/messages`:
- `forwarded_from` (JSONB) — full self-contained snapshot of the original message (root, after FE-side collapse). Persisted verbatim, returned verbatim. No structural validation beyond JSON shape.
- `attachment_ids` (`text[]`) — list of pre-existing `attachments.id` values to associate with the new message. Backend creates one `message_attachments` row per id (or, depending on existing schema, copies the rows with the new `message_id`). **Validated** for existence and read-permission ownership.

Both fields are also added to the `Message` entity returned by all read endpoints + realtime broadcast (`message.created` event). When absent, the response shape is byte-identical to today's (omitempty / NULL).

This PR is **backend-only**. No FE code touched here.

---

## 2. Context

### Existing Message entity (`internal/domain/entity/message.go`)

```go
type Message struct {
    ID         uuid.UUID
    ChannelID  uuid.UUID
    UserID     uuid.UUID
    ParentID   *uuid.UUID
    Content    string
    Type       string
    Edited     bool
    EditedAt   *time.Time
    Pinned     bool
    PinnedBy   *uuid.UUID
    PinnedAt   *time.Time
    CreatedAt  time.Time
    UpdatedAt  time.Time
    DeletedAt  *time.Time
    // Aggregated (loaded by repo):
    ReplyCount *int
    Reactions  []Reaction
    Attachments []Attachment
    User       *User
}
```

### Existing POST handler request (`internal/handler/http/message.go:25-28`)

```go
type sendMessageRequest struct {
    Content  string  `json:"content" validate:"required,min=1,max=40000"`
    ParentID *string `json:"parent_id,omitempty"`
}
```

### Service `SendMessage` signature (`internal/service/chat/service.go:625-630`)

```go
func (s *Service) SendMessage(
    ctx context.Context,
    channelID, userID uuid.UUID,
    content string,
    parentID *uuid.UUID,
) (*entity.Message, error)
```

### Repository `Create` (`internal/repository/postgres/message.go:38-64`)

INSERTs into `messages` with 14 columns currently. Migration tool: golang-migrate SQL files in `migrations/NNN_<name>.sql`. Next migration is `035`.

### Attachments (`internal/domain/entity/message.go:48-57`)

```go
type Attachment struct {
    ID          uuid.UUID
    MessageID   uuid.UUID  // owning message
    FileName    string
    FileSize    int64
    MimeType    string
    StoragePath string
    URL         string
    CreatedAt   time.Time
}
```

Current attachment lifecycle: client uploads → server creates row in `attachments` table linked to the *creating* message via `message_id`. There is NO pre-upload-with-orphan-attachment endpoint today.

**This means `attachment_ids` semantics must be one of:**
- **(A) Reference-only:** new message has its own row in a join table `message_attachments(message_id, attachment_id)`; original attachment row stays linked to original message; FE renders snapshot.
- **(B) Duplicate row:** copy the attachment row with a new `message_id`, keeping the same `storage_path`. The blob is shared by both rows; on storage-delete cascade only the last-referenced row deletes the blob.

The FE spec §4 says: *"backend creates `message_attachments` rows for each `attachment_id` pointing at existing blob refs"* — favouring option (A).

**However** the current schema has no `message_attachments` table — attachments have a direct `message_id` FK. Option (A) would require introducing the join table (large migration, touches all existing data). Option (B) is mechanically simpler: copy row with new `message_id`, share `storage_path`.

**Decision: option (B) — duplicate row.** Rationale: (1) no schema overhaul of the existing `attachments` table; (2) per-message attachments stay queryable via the same `WHERE message_id = ?` pattern; (3) storage cost: only metadata duplicates, blobs shared via `storage_path`; (4) on delete: storage GC must count distinct `storage_path` references before unlinking the blob (existing GC behaviour likely already does this; verify).

---

## 3. Schema changes

### Migration `035_messages_forward_attachment.sql`

```sql
BEGIN;

-- ALOQA-257: forward message support
ALTER TABLE messages
    ADD COLUMN forwarded_from jsonb DEFAULT NULL;

-- Index for "messages forwarded from <message_id>" lookup (optional but cheap)
CREATE INDEX IF NOT EXISTS idx_messages_forwarded_from_message_id
    ON messages ((forwarded_from->>'message_id'))
    WHERE forwarded_from IS NOT NULL;

COMMIT;
```

Down migration `down/035_messages_forward_attachment.down.sql`:

```sql
BEGIN;
DROP INDEX IF EXISTS idx_messages_forwarded_from_message_id;
ALTER TABLE messages DROP COLUMN IF EXISTS forwarded_from;
COMMIT;
```

**No new columns for `attachment_ids`** — the field is INPUT-only. Server processes it by `SELECT` from `attachments`, then `INSERT` duplicated rows. The persisted state is the `attachments` table; the request field is not stored beyond the side effect.

---

## 4. Code changes

### 4.1 Entity (`internal/domain/entity/message.go`)

Add to `Message`:

```go
// JSON-typed forwarded-from envelope. Stored verbatim, returned verbatim.
// Server does not parse the internal structure — FE owns the schema.
ForwardedFrom json.RawMessage `json:"forwarded_from,omitempty" db:"forwarded_from"`
```

`omitempty` ensures the response omits the field when NULL.

### 4.2 HTTP handler request (`internal/handler/http/message.go:25-28`)

```go
type sendMessageRequest struct {
    Content       string          `json:"content" validate:"required,min=1,max=40000"`
    ParentID      *string         `json:"parent_id,omitempty"`
    ForwardedFrom json.RawMessage `json:"forwarded_from,omitempty"`
    AttachmentIDs []string        `json:"attachment_ids,omitempty" validate:"omitempty,max=20,dive,uuid"`
}
```

`validate:"omitempty,max=20,dive,uuid"` — at most 20 attachment ids per message, each a valid UUID. (Existing attachment limit per message TBD; if there's an existing constant, reuse it; otherwise 20 is a reasonable upper bound for forward-of-attachments.)

The handler resolves `*string` ParentID + `[]string` AttachmentIDs into `*uuid.UUID` + `[]uuid.UUID` before calling service.

### 4.3 Service input (`internal/service/chat/service.go:236-238`)

Extend `SendMessageInput`:

```go
type SendMessageInput struct {
    Content       string          `validate:"required,min=1,max=40000"`
    ParentID      *uuid.UUID
    ForwardedFrom json.RawMessage // nil when not a forward
    AttachmentIDs []uuid.UUID     // nil or empty when none
}
```

### 4.4 Service `SendMessage` signature

Switch to a struct-input signature for evolvability (avoids growing positional args):

```go
func (s *Service) SendMessage(
    ctx context.Context,
    channelID, userID uuid.UUID,
    input SendMessageInput,
) (*entity.Message, error)
```

If signature break is too invasive, use the variant: keep the old function with old args, add a sibling `SendMessageV2(ctx, channelID, userID, input SendMessageInput) (*entity.Message, error)` and migrate the handler to call V2. **Recommend: full signature change** since this is a same-PR cohesive change and the only caller is the HTTP handler.

### 4.5 Service implementation

In `SendMessage`:

1. Existing checks (channel exists, user member, parent valid) unchanged.
2. **Attachment validation** (only when `len(input.AttachmentIDs) > 0`):
   - `SELECT id, channel_id, user_id, message_id, file_name, file_size, mime_type, storage_path, url, created_at FROM attachments WHERE id = ANY($1)`.
   - Assert: `len(returned) == len(input.AttachmentIDs)` — else 400 "one or more attachments not found".
   - **Permission check:** for each returned row, the user must be allowed to read the originating message. Cheapest correct check: user must be a member of `attachments.channel_id` (which is `messages.channel_id` via the FK). Reuse existing `repo.IsChannelMember(ctx, attachment.channel_id, userID)` per attachment, OR batch via `SELECT channel_id FROM channels WHERE id = ANY(...) AND id IN (SELECT channel_id FROM channel_members WHERE user_id = $1)`. Reject 403 "no access to attachment {id}" if any check fails.
3. Insert the new message row (now with `forwarded_from = $X`).
4. **Duplicate attachment rows** (option B): for each `(id, attachment)` in the validated set, `INSERT INTO attachments (id, message_id, file_name, file_size, mime_type, storage_path, url, created_at, channel_id) VALUES (gen_random_uuid(), $newMsgID, $attachment.FileName, $attachment.FileSize, $attachment.MimeType, $attachment.StoragePath, $attachment.URL, now(), $newMsgChannelID)`. Wrap in transaction with the message insert. The new attachment row shares `storage_path` with the original — no blob copy.
5. Load aggregations (existing logic for `Reactions`, `Attachments`, `User`) — `Attachments` returns the newly-duplicated rows automatically since they query by `message_id`.
6. Return Message.

**Forwarded_from**: pass `input.ForwardedFrom` directly to the repository as `json.RawMessage`. The repository binds to the `jsonb` column. **No internal validation of the JSON structure** — FE owns the shape per [[aloqa_backend_changes_allowed]] + ALOQA-175 spec §12.5. Server does NOT verify source-channel membership.

### 4.6 Repository (`internal/repository/postgres/message.go:38-64`)

Extend `Create`:

- Add `forwarded_from` to the INSERT column list and the VALUES placeholder list.
- Bind `entity.Message.ForwardedFrom` (`json.RawMessage` — pgx handles `[]byte` → jsonb natively).
- Make `forwarded_from` selectable in all SELECT queries that load `Message` (existing `GetByID`, `ListByChannel`, etc.) — add the column to the projection.
- `pgxscan` row-mapping must handle `*json.RawMessage` or scan `[]byte` and assign.

### 4.7 Events (`internal/domain/event/events.go`)

**No code changes** — `MessagePayload.Message` is `*entity.Message`; the new `ForwardedFrom` field flows automatically through JSON marshaling. Realtime subscribers (`message.created` event) receive the field for free.

### 4.8 No-op for attachments table

The `attachments` table schema is unchanged. The new write path inserts additional rows with new `id` values, same `storage_path`. Existing storage GC (if any) is responsible for not deleting a blob while another row references the same `storage_path`. **Verify this is true** — if GC currently deletes blob on row delete, document as a known issue and file a follow-up; for MVP it's tolerable since forwarded messages are not commonly deleted, and even then the worst case is a temporarily-broken blob ref.

---

## 5. Validation and error responses

| Condition | HTTP | Body |
|---|---|---|
| `forwarded_from` malformed JSON | 400 | `{"error": "invalid forwarded_from: <message>"}` |
| `attachment_ids` includes a non-UUID | 400 | (validator default — `validation failed: attachment_ids.[i] must be a valid UUID`) |
| `attachment_ids` length > 20 | 400 | (validator default — `attachment_ids must contain at most 20 items`) |
| Any `attachment_ids[i]` not found in `attachments` | 400 | `{"error": "attachment not found: <id>"}` |
| Any `attachment_ids[i]` references a channel the user is not a member of | 403 | `{"error": "no access to attachment: <id>"}` |
| Target channel send permission fails | 403 | (existing behaviour, unchanged) |

The first 3 are validator-level (request-time). The last 3 are service-level (after request bind). Test cases per §7.

---

## 6. Performance + storage notes

- **Attachment duplication cost**: one row per attachment in `attachments` table. Each row is ~200 bytes of metadata; blob is shared. For a 5-attachment message forwarded once, +1KB DB overhead.
- **Query plan for forwarded_from JSONB**: indexed via expression index on `(forwarded_from->>'message_id')`. Reverse-lookup "all forwards of message X" is O(log N) with this index.
- **Realtime payload size**: `forwarded_from.snapshot` includes the original content + attachment metadata. For a typical 200-byte forwarded message with one attachment, the broadcast payload grows by ~500 bytes. Acceptable.
- **No N+1**: the existing message loading already batches reactions/attachments/user. New field is on the row itself.

---

## 7. Test plan

### Unit (service_test.go)

| # | Case | Expected |
|---|---|---|
| U1 | `SendMessage` without `ForwardedFrom` or `AttachmentIDs` | Unchanged behaviour; row inserted with `forwarded_from = NULL`. |
| U2 | `SendMessage` with `ForwardedFrom = raw json blob` | Row inserted with `forwarded_from = <blob>`; returned `Message.ForwardedFrom` equals input. |
| U3 | `SendMessage` with `AttachmentIDs` of 2 valid existing attachments the user can access | 2 new rows in `attachments` table, both with new message_id, sharing storage_path. Returned `Message.Attachments` has 2 entries. |
| U4 | `SendMessage` with `AttachmentIDs` of an attachment the user cannot access (different channel, not a member) | 403; no message row inserted; no attachment rows inserted. |
| U5 | `SendMessage` with `AttachmentIDs` containing a non-existent UUID | 400; no insertion. |
| U6 | `SendMessage` with both `ForwardedFrom` AND `AttachmentIDs` (typical forward path) | All-in-one happy path; correct row state. |
| U7 | `SendMessage` with `ForwardedFrom = empty raw json` (e.g., `{}`) | Accepted; persisted as `{}`. (No internal validation.) |
| U8 | Transaction rollback test: simulate attachment INSERT failure mid-loop | Message row rolled back; no attachments inserted; error surfaced. |

### Integration (handler-level, HTTP)

| # | Case | Expected |
|---|---|---|
| I1 | `POST` with `forwarded_from: {...}` + `attachment_ids: [...]` | 201; response JSON contains `forwarded_from` echo + `attachments[]` with new ids. |
| I2 | `POST` with `attachment_ids` of length 21 | 400 validator error. |
| I3 | `POST` with `attachment_ids: ["not-a-uuid"]` | 400 validator error. |
| I4 | `POST` omitting both new fields | 201; response JSON does NOT include `forwarded_from` (omitempty). |
| I5 | `GET /messages/{id}` after a forward | Response includes `forwarded_from` echo. |
| I6 | Realtime `message.created` event after forward send | Payload includes `forwarded_from`. |
| I7 | `POST` with `forwarded_from: "not-an-object"` (string scalar) | Accept as valid JSON (it IS valid JSON). Persisted. **Trust FE per §12.5.** Document this as a known behaviour. (Optional: add minimal "must be object" guard if Codex feels strongly; not strictly required by FE spec.) |

### Migration test

| # | Case | Expected |
|---|---|---|
| M1 | Run `035_messages_forward_attachment.sql` on a populated dev DB | Column added, existing rows have `forwarded_from = NULL`, no data loss. |
| M2 | Run down migration after up | Column removed, no data loss for other columns. |
| M3 | Idempotency: run up twice | Second run no-ops (`IF NOT EXISTS` on index, `ADD COLUMN` without `IF NOT EXISTS` would error — use `ALTER TABLE messages ADD COLUMN IF NOT EXISTS forwarded_from jsonb DEFAULT NULL` for safety). **Update migration to use `IF NOT EXISTS`.** |

---

## 8. DO-NOT-DRIFT invariants

1. **Migration uses `IF NOT EXISTS`** on both column add and index create.
2. **Server does NOT validate forwarded_from structure** beyond "is valid JSON". FE owns the schema per ALOQA-175 §12.5.
3. **Attachment access check is mandatory** when `attachment_ids` is non-empty. Users must be members of `attachments.channel_id`. No access → 403.
4. **Attachment duplication is atomic with message insert** — both happen in one transaction; rollback on either failure.
5. **`forwarded_from` omitempty** on JSON response — absent field when NULL.
6. **`Attachments` field on response is the NEW message's attachments**, not the snapshot's. The snapshot is only inside `forwarded_from.snapshot.attachments` (FE-rendered).
7. **No change to existing send-message-without-forward path** — verify by running existing service_test.go without modifications.
8. **No new external dependencies** — all changes use stdlib + existing deps (pgx, validator/v10, etc.).
9. **Realtime broadcast carries `forwarded_from`** automatically via JSON marshaling — no event payload struct change.
10. **Max 20 attachment_ids per forward** enforced at validator level + service-level defensive cap.

---

## 9. Acceptance criteria

1. All unit tests U1–U8 pass.
2. All integration tests I1–I7 pass.
3. Migration M1–M3 verified manually against a dev DB.
4. `make lint && make test` green.
5. Realtime `message.created` event payload includes `forwarded_from` for forwarded messages (manually verified by triggering a forward send and inspecting websocket output).
6. POST `/api/v1/workspaces/{wsId}/channels/{chId}/messages` with the legacy body (no new fields) returns 201 + the same response shape as before — backward compatibility regression test.
7. All 10 §8 DO-NOT-DRIFT invariants pass review.

### Manual test cases (will be `state.manual_test_cases`)

| # | Title | Steps | Expected |
|---|---|---|---|
| 1 | Legacy send still works | POST with body `{"content":"hi"}` | 201, response same shape as before. No `forwarded_from` in response. |
| 2 | Forward with snapshot only | POST with body `{"content":"","forwarded_from":{"user_id":"u1","message_id":"m1","channel_id":"c1","created_at":"...","snapshot":{"content":"...","attachments":[]}}}` | 201, response echoes `forwarded_from`. No attachments duplicated. |
| 3 | Forward with attachment_ids | POST with body including `attachment_ids:[<existing>]` user can access | 201, response `attachments[]` has new rows with same storage_path. |
| 4 | Forward with foreign attachment_ids | POST with body including `attachment_ids:[<id from a channel user is not in>]` | 403 with clear error message. No message row inserted. |
| 5 | Realtime event includes forwarded_from | Subscribe to the channel websocket, then POST forward | `message.created` event payload includes `forwarded_from`. |
| 6 | Migration idempotency | Run up migration twice on same DB | Second run no-ops, no error. |

---

## 10. Code review prompt augmentation

When reviewing the PR, the Codex reviewer MUST inline-enumerate:
1. Every §7 test case (U1–U8, I1–I7, M1–M3) and verify each has a matching test.
2. Every §8 DO-NOT-DRIFT invariant (1–10) and verify the code upholds it.
3. The `IF NOT EXISTS` guard on migration (§M3).
4. The attachment access check on the service layer (§5).
5. Backward compatibility: `omitempty` on Message.ForwardedFrom in response.

Per [[aloqa_168_merge_codex_led]] lesson — "consult §X" is insufficient; the brief MUST enumerate verbatim.

---

## 11. Out of scope

- Storage blob garbage collection rework (existing GC retained).
- `message_attachments` join-table migration (option A); chosen option B per §2.
- Forwarded-message search / indexing (out of scope; existing FTS can be extended later).
- Attachment ownership transfer (current model: duplicated row owns its own metadata).
- Audit trail for forwards (not requested by FE spec).

Related: [[aloqa_calls_backend_session_pause_2026_05_15]], [[aloqa_backend_changes_allowed]], [[vultr_staging_deploy_2026_05_14]] (auto-deploy on develop merge).
