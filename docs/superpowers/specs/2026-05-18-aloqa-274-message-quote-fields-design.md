# ALOQA-274 — BE: `quoted_message_id` + `quoted_snapshot` on Message

**Status:** Spec
**Date:** 2026-05-18
**Repo:** aloqa-claude (backend)
**Parent FE ticket:** ALOQA-169 in `aloqa-frontend`. See FE spec at `aloqa-frontend/docs/superpowers/specs/2026-05-18-aloqa-169-quote-reply-design.md` §4 for the contract.
**Predecessor (BE):** ALOQA-257 (forward_from JSONB column + cascade SoftDelete pattern) — same architectural template here.
**Profile:** `pr-sync --codex-led`.

---

## 1. Goal

Add two paired optional fields on `POST /api/v1/workspaces/{wsId}/channels/{chId}/messages` (and on the `Message` entity returned everywhere):

- `quoted_message_id: *uuid.UUID` — pointer to the original message being quoted.
- `quoted_snapshot: *QuotedSnapshot` — TYPED struct (not raw json) with frozen reference data:
  - `user_id: uuid.UUID` (original author)
  - `content_excerpt: string` (≤200 **Unicode codepoints**, FE-truncated; server defensive cap via `utf8.RuneCountInString`). **FE-side Zod schema enforces same codepoint count** via a `.refine((s) => Array.from(s).length <= 200)` rule, NOT `z.string().max(200)` which counts JS UTF-16 units. Codepoint-based parity ensures BE/FE agree on emoji-heavy text. (FE spec §4 / FE plan Phase B must use the codepoint-based validator; this is an architectural alignment point — flag in code-review.)
  - `created_at: time.Time` (original timestamp)
  - `deleted: *bool` (BE-owned, NEVER set by FE on POST; written only by cascade SoftDelete)
  - `parent_message_id: *uuid.UUID` (set when quoted msg lived inside a thread; lets FE build `?m=X&thread=Y` deeplinks)

**Pairing invariant**: `quoted_message_id` and `quoted_snapshot` are both set or both null. Service returns 400 on partial state. Persisted state always satisfies the invariant.

**Cascade SoftDelete**: when a message M is soft-deleted, the transaction also:
1. Clears M's OWN `quoted_message_id = NULL, quoted_snapshot = NULL` (preserves pairing — see [[aloqa_175_forward_merge]] U9 lesson; also privacy parity with `forwarded_from = NULL`).
2. UPDATEs every other message that quoted M: `SET quoted_snapshot = jsonb_set(quoted_snapshot, '{deleted}', 'true'::jsonb) WHERE quoted_message_id = $M`. Atomically in same transaction.
3. Emits one `message.updated` realtime event per affected row (uses existing event infra; no payload struct change).

This PR is **backend-only**. FE PR for ALOQA-169 depends on this landing on develop.

---

## 2. Context

### Existing Message entity (`internal/domain/entity/message.go`)

Existing fields + ALOQA-257 `ForwardedFrom json.RawMessage`. We add typed `QuotedMessageID` + `QuotedSnapshot` (note: typed struct, NOT raw json — FE spec §3 demands BE owns the shape so it can normalize `Deleted = nil` on POST without trusting raw client JSON).

### POST handler (`internal/handler/http/message.go:25-28`)

After ALOQA-257 has `Content, ParentID, ForwardedFrom`. We add `QuotedMessageID *string` + `QuotedSnapshot *QuotedSnapshotInput` (the restricted-on-input typed struct — no Deleted field, no ParentMessageID resolution into UUID).

### Service `SendMessage` (after ALOQA-257)

Now uses struct input `SendMessage(ctx, channelID, userID, input SendMessageInput)`. Extend `SendMessageInput` with `QuotedMessageID *uuid.UUID` + `QuotedSnapshot *QuotedSnapshotInput`. Service body:
- Validates pairing (both nil or both set → else 400)
- Validates `len(content_excerpt) ≤ 200` (defensive cap)
- Builds `entity.QuotedSnapshot` with `Deleted = nil` explicitly (drops any client-supplied value)
- Passes to repository

### Repository `Create` (after ALOQA-257)

INSERTs include `forwarded_from`. Extend INSERT to project `quoted_message_id` + `quoted_snapshot`. Every SELECT path that loads Message must project both new columns (already-touched files after ALOQA-257: GetByID, ListByChannel, thread queries, FTS, pinned — same set).

### SoftDelete (`internal/repository/postgres/message.go:370-393`, post-ALOQA-257)

Currently NULLs `forwarded_from`. Extend: also NULL `quoted_message_id` + `quoted_snapshot` (pairing invariant maintained) AND cascade `jsonb_set` UPDATE on rows where `quoted_message_id = $deletedId`. Both writes in same transaction.

### Realtime broadcast

`message.updated` event already broadcast by existing path. Cascade UPDATE on related rows must each emit one `message.updated` event so receivers see the deleted state without manual refresh. After the transaction commits, the service layer iterates the affected message IDs from step 2 of cascade and dispatches events.

### Migration tool + numbering

`migrations/036_messages_quote_fields.sql` (035 was ALOQA-257). Same `IF NOT EXISTS` discipline.

### Pairing-invariant enforcement layers (4)

Per FE spec §12.16:
- L1 FE Zod refine on incoming MessageSchema (rejects parse if partial)
- L2 BE service rejects 400 on POST if partial
- L3 FE sync executor drops both fields silently with warn on replay if partial
- L4 FE UI defensive guard before rendering

BE owns L2. Other layers are FE responsibility.

---

## 3. Schema change

### Migration `036_messages_quote_fields.sql`

```sql
BEGIN;

-- ALOQA-274: quote-reply support — persist quote reference + frozen snapshot.
ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS quoted_message_id uuid NULL,
    ADD COLUMN IF NOT EXISTS quoted_snapshot jsonb NULL;

-- Cascade SoftDelete lookup index: "all messages quoting X". O(log N) via the
-- partial index — most messages don't quote anyone, so the index stays tiny.
CREATE INDEX IF NOT EXISTS idx_messages_quoted_message_id
    ON messages (quoted_message_id)
    WHERE quoted_message_id IS NOT NULL;

COMMIT;
```

Down migration `migrations/down/036_messages_quote_fields.down.sql`:

```sql
BEGIN;
DROP INDEX IF EXISTS idx_messages_quoted_message_id;
ALTER TABLE messages
    DROP COLUMN IF EXISTS quoted_message_id,
    DROP COLUMN IF EXISTS quoted_snapshot;
COMMIT;
```

Both `ADD COLUMN` and `CREATE INDEX` use `IF NOT EXISTS` for idempotency.

---

## 4. Code changes

### 4.1 Entity (`internal/domain/entity/message.go`)

```go
type QuotedSnapshot struct {
    UserID          uuid.UUID  `json:"user_id" db:"user_id"`
    ContentExcerpt  string     `json:"content_excerpt" db:"content_excerpt"`
    CreatedAt       time.Time  `json:"created_at" db:"created_at"`
    Deleted         *bool      `json:"deleted,omitempty" db:"deleted,omitempty"`
    ParentMessageID *uuid.UUID `json:"parent_message_id,omitempty" db:"parent_message_id,omitempty"`
}

type Message struct {
    // ...existing fields including ForwardedFrom...
    QuotedMessageID *uuid.UUID      `json:"quoted_message_id,omitempty" db:"quoted_message_id"`
    QuotedSnapshot  *QuotedSnapshot `json:"quoted_snapshot,omitempty" db:"quoted_snapshot"`
}
```

`omitempty` ensures response shape is byte-identical to today's when both are null.

### 4.2 HTTP handler request (`internal/handler/http/message.go:25-28`)

```go
// QuotedSnapshotInput is EXPORTED so handler (different package) can construct it.
// The HTTP handler decodes this directly; service reuses the same type (no duplicate
// definition).
type QuotedSnapshotInput struct {
    UserID          string  `json:"user_id" validate:"required,uuid"`
    ContentExcerpt  string  `json:"content_excerpt" validate:"required"` // codepoint cap checked in service body
    CreatedAt       string  `json:"created_at" validate:"required"`
    ParentMessageID *string `json:"parent_message_id" validate:"omitempty,uuid"`
    // NO Deleted field. **Existing decodeJSON uses DisallowUnknownFields()**, so a
    // client sending `deleted: true` is REJECTED at decode (400) before reaching the
    // service. This is the security boundary — see U7 / I3 expecting 400, not 201.
}

type sendMessageRequest struct {
    Content         string                `json:"content"`
    ParentID        *string               `json:"parent_id,omitempty"`
    ForwardedFrom   json.RawMessage       `json:"forwarded_from,omitempty"` // ALOQA-257
    QuotedMessageID *string               `json:"quoted_message_id,omitempty" validate:"omitempty,uuid"`
    QuotedSnapshot  *QuotedSnapshotInput  `json:"quoted_snapshot,omitempty"`
}
```

Handler resolves `*string`-typed UUIDs to `*uuid.UUID`, constructs a `service.SendMessageInput` with the parsed values, passes the SAME `QuotedSnapshotInput` (re-used as the service input type).

### 4.3 Service input (`internal/service/chat/service.go:236-238`)

**Service uses the SAME exported `QuotedSnapshotInput` defined in handler package** (or in a shared `internal/api/dto` package — implementer's choice). The duplicate parsed-vs-raw distinction is between the handler-decoded form (strings) and the service-resolved form (UUIDs + time):

```go
// In the service package (or shared dto package):
type ParsedQuotedSnapshotInput struct {
    UserID          uuid.UUID
    ContentExcerpt  string
    CreatedAt       time.Time
    ParentMessageID *uuid.UUID
    // NO Deleted field — service constructs entity.QuotedSnapshot with Deleted=nil
}

type SendMessageInput struct {
    Content         string
    ParentID        *uuid.UUID
    ForwardedFrom   json.RawMessage      // ALOQA-257
    QuotedMessageID *uuid.UUID
    QuotedSnapshot  *ParsedQuotedSnapshotInput
}
```

Handler converts `QuotedSnapshotInput` (strings) → `ParsedQuotedSnapshotInput` (typed) using `uuid.Parse` + `time.Parse(time.RFC3339, ...)`. Validation errors at parse → 400.

### 4.4 Service `SendMessage` body — pairing + normalization

```go
func (s *Service) SendMessage(ctx context.Context, channelID, userID uuid.UUID, input SendMessageInput) (*entity.Message, error) {
    // ...existing validate.Struct + content/forwarded_from checks from ALOQA-257...

    // Pairing invariant: both quoted fields must be set together.
    if (input.QuotedMessageID == nil) != (input.QuotedSnapshot == nil) {
        return nil, cerrors.InvalidInput("quoted_message_id and quoted_snapshot must be set together")
    }

    // Defensive excerpt cap (FE truncates; server cap defends against bypassed clients).
    if input.QuotedSnapshot != nil && utf8.RuneCountInString(input.QuotedSnapshot.ContentExcerpt) > 200 {
        return nil, cerrors.InvalidInput("quoted_snapshot.content_excerpt must be at most 200 characters")
    }

    // Build entity.QuotedSnapshot with Deleted=nil explicitly (drops any client-supplied value).
    var quotedSnapshot *entity.QuotedSnapshot
    if input.QuotedSnapshot != nil {
        quotedSnapshot = &entity.QuotedSnapshot{
            UserID:          input.QuotedSnapshot.UserID,
            ContentExcerpt:  input.QuotedSnapshot.ContentExcerpt,
            CreatedAt:       input.QuotedSnapshot.CreatedAt,
            Deleted:         nil,                              // BE-owned; explicitly nil on POST
            ParentMessageID: input.QuotedSnapshot.ParentMessageID,
        }
    }

    msg := &entity.Message{
        // ...existing fields...,
        ForwardedFrom:   input.ForwardedFrom,
        QuotedMessageID: input.QuotedMessageID,
        QuotedSnapshot:  quotedSnapshot,
    }
    return s.repo.Create(ctx, msg)
}
```

**No source-channel/message read check** on `quoted_message_id` — FE-trusted per FE spec §12.18 (parallel to ALOQA-175 §12.5 and ALOQA-257 §8.4).

### 4.5 Repository `Create` (after ALOQA-257)

- Add `quoted_message_id` + `quoted_snapshot` to the INSERT column list and VALUES placeholder list.
- pgx handles `*uuid.UUID` directly; `*entity.QuotedSnapshot` marshals to jsonb via `pgtype.JSONB` or `json.Marshal` then bind as `[]byte`.
- Every SELECT path that loads Message must project both columns. pgxscan handles `db:"quoted_snapshot"` tag for struct unmarshaling.

### 4.6 Repository `SoftDelete` cascade — new method preserving existing signature

The existing `MessageRepository.SoftDelete(ctx, id) error` interface is unchanged for backward compatibility with non-cascading callers and the `queryable`-scoped transaction abstraction. Instead, the repository gains a NEW method `SoftDeleteWithCascade` that returns the cascade-affected messages so the SERVICE layer emits realtime events:

```go
// In domain/repository/interfaces.go (extend MessageRepository):
type MessageRepository interface {
    // ...existing methods including SoftDelete(ctx, id) error...
    
    // SoftDeleteWithCascade marks the message deleted, clears its own quote fields
    // (pairing preservation), and cascades quoted_snapshot.deleted=true to all
    // messages that quoted it. Returns the IDs of cascade-affected rows so the
    // caller can emit realtime events. Both UPDATEs run in one transaction.
    SoftDeleteWithCascade(ctx context.Context, id uuid.UUID) (affectedQuoteRowIDs []uuid.UUID, err error)
}

// In repository/postgres/message.go:
func (r *MessageRepo) SoftDeleteWithCascade(ctx context.Context, id uuid.UUID) ([]uuid.UUID, error) {
    now := time.Now().UTC()
    var affected []uuid.UUID

    err := r.beginTx(ctx, func(tx queryable) error {
        // Primary UPDATE — extends ALOQA-257 SoftDelete cleanup with quote fields
        // (pairing preservation: BOTH quoted_message_id and quoted_snapshot go NULL).
        primary := `
            UPDATE messages
            SET content = '', edited = false, edited_at = NULL,
                pinned = false, pinned_by = NULL, pinned_at = NULL,
                forwarded_from = NULL,
                quoted_message_id = NULL, quoted_snapshot = NULL,
                updated_at = $2, deleted_at = $2
            WHERE id = $1 AND deleted_at IS NULL`
        tag, err := tx.Exec(ctx, primary, id, now)
        if err != nil { return fmt.Errorf("postgres: soft delete: %w", err) }
        if tag.RowsAffected() == 0 { return cerrors.NotFound("message not found") }

        cascade := `
            UPDATE messages
            SET quoted_snapshot = jsonb_set(quoted_snapshot, '{deleted}', 'true'::jsonb),
                updated_at = $2
            WHERE quoted_message_id = $1 AND deleted_at IS NULL
            RETURNING id`
        rows, err := tx.Query(ctx, cascade, id, now)
        if err != nil { return fmt.Errorf("postgres: cascade quote-delete: %w", err) }
        defer rows.Close()
        for rows.Next() {
            var rowID uuid.UUID
            if err := rows.Scan(&rowID); err != nil { return err }
            affected = append(affected, rowID)
        }
        return rows.Err()
    })
    return affected, err
}
```

**Existing `SoftDelete(ctx, id) error` is also extended** with the OWN-row quote clearing (the primary UPDATE in the snippet above, minus the cascade) — preserves the pairing invariant for messages that themselves had a quote. Non-cascading callers continue to use the simple signature.

**Service layer wires the cascade + event emission**:

```go
// In service/chat/service.go:
func (s *Service) DeleteMessage(ctx context.Context, messageID, userID uuid.UUID) error {
    msg, ch, err := s.requireMessageAccessWithCapability(ctx, messageID, userID, accesspolicy.CapabilityDelete)
    if err != nil { return err }
    _ = ch

    affected, err := s.repo.SoftDeleteWithCascade(ctx, messageID)
    if err != nil { return err }

    // Existing MessageDeleted event for the deleted message itself
    s.eventBus.Publish(event.MessageDeleted, &event.MessagePayload{Message: msg})

    // New: one MessageUpdated event per cascade-affected row so receivers
    // see the deleted-quote chip state without manual refresh.
    for _, rowID := range affected {
        if updated, _ := s.repo.GetByID(ctx, rowID); updated != nil {
            s.eventBus.Publish(event.MessageUpdated, &event.MessagePayload{Message: updated})
        }
    }
    return nil
}
```

`eventBus.Publish` matches the existing pattern in the service file — `git grep eventBus.Publish` or `event.Bus` to verify exact API.

### 4.7 No event payload struct change

`event.MessagePayload.Message = *entity.Message` flows the new fields automatically.

---

## 5. Validation and error responses

Existing error envelope shape: `{"error":{"code":"...","message":"..."}}` per `internal/api/helpers/*` (verify via `git grep cerrors.InvalidInput` or the existing handler error path). All error bodies use this shape, not bare `{"error":"..."}`.

| Condition | HTTP | Code | Message |
|---|---|---|---|
| `quoted_message_id` set but `quoted_snapshot` null (or vice versa) | 400 | `INVALID_INPUT` | `quoted_message_id and quoted_snapshot must be set together` |
| `quoted_snapshot.content_excerpt` > 200 codepoints | 400 | `INVALID_INPUT` | `quoted_snapshot.content_excerpt must be at most 200 characters` |
| `quoted_message_id` not a valid UUID | 400 | `INVALID_INPUT` | (validator default — `quoted_message_id must be a valid UUID`) |
| `quoted_snapshot.user_id` / `parent_message_id` not valid UUID | 400 | `INVALID_INPUT` | (validator default) |
| `quoted_snapshot.created_at` not RFC3339 | 400 | `INVALID_INPUT` | (handler `time.Parse` error → wrapped as InvalidInput) |
| Target channel send permission fails | 403 | (existing behaviour) | (unchanged) |
| Client sends `quoted_snapshot.deleted: <any>` in POST body | 400 | `INVALID_INPUT` | `unknown field "deleted"` (rejected at `decodeJSON` because `QuotedSnapshotInput` omits the field AND `DisallowUnknownFields()` is enabled — strict decoding is the security boundary, NOT service-layer stripping). |

---

## 6. Performance + storage notes

- Two new mostly-NULL columns. Partial expression index on `quoted_message_id` is small (~bytes per non-null row).
- Cascade UPDATE on popular-message delete: O(N) for fanout, indexed lookup is O(log N) for the WHERE. Emitting N events is unavoidable for receivers to see deletion in real-time; acceptable for chat-polish scale.
- Realtime payload grows by `quoted_snapshot` size (~300-500 bytes) per quote-reply message.

---

## 7. Test plan

### Unit (`internal/service/chat/service_test.go`)

| # | Case | Expected |
|---|---|---|
| U1 | `SendMessage` without quote fields | Behavior unchanged from ALOQA-257 baseline; no quote columns set. |
| U2 | `SendMessage` with both quote fields valid | Persisted with both set; `QuotedSnapshot.Deleted == nil` regardless of input. |
| U3 | `SendMessage` with `QuotedMessageID` set but `QuotedSnapshot` nil | 400 `quoted_message_id and quoted_snapshot must be set together`. |
| U4 | `SendMessage` with `QuotedSnapshot` set but `QuotedMessageID` nil | 400 same error. |
| U5 | `SendMessage` with `QuotedSnapshot.ContentExcerpt` length 201 chars | 400 `content_excerpt must be at most 200 characters`. |
| U6 | `SendMessage` with `QuotedSnapshot.ContentExcerpt` exactly 200 chars | 201; persisted with full excerpt. |
| U7 | **Deleted-spoofing rejection at decode boundary**: POST handler receives `quoted_snapshot.deleted: true` → `decodeJSON` returns 400 `unknown field "deleted"` because `QuotedSnapshotInput` omits the field and `DisallowUnknownFields()` is enabled. No row inserted. (This is stricter than the spec's original "201 with ignore" — strict decoding is the security boundary, more defensive.) |
| U8 | Multi-byte excerpt boundary: `strings.Repeat("я", 200)` → accepted (200 runes ≤ cap); 201 runes → rejected. |
| U9 | SoftDelete on message that has its OWN quote: `quoted_message_id` AND `quoted_snapshot` NULLed (pairing preserved); content cleared. |
| U10 | SoftDelete cascade: insert message A; insert messages B, C, D quoting A; SoftDelete A; fetch B/C/D → each has `QuotedSnapshot.Deleted == &true`; affected row count 3. |
| U11 | SoftDelete cascade event emission: same setup as U10; assert `eventBus.Publish(MessageUpdated, ...)` called 3 times with B/C/D message payloads. |
| U12 | SoftDelete cascade transaction atomicity: simulate cascade UPDATE failure → primary UPDATE rolled back; A still has `deleted_at == nil`; no events emitted. |

### Integration (HTTP)

| # | Case | Expected |
|---|---|---|
| I1 | `POST` with both quote fields | 201; response echoes `quoted_message_id` + `quoted_snapshot` with `deleted` absent (nil). |
| I2 | `POST` with partial state | 400. |
| I3 | `POST` with `quoted_snapshot.deleted: true` (client lies) | 400 `unknown field "deleted"` rejected at decode (strict JSON decoding via `DisallowUnknownFields`). |
| I4 | `GET /messages/{id}` after quote-reply send | Response includes both quote fields. |
| I5 | Realtime `message.created` event after quote-reply send | Payload includes both quote fields. |
| I6 | DELETE message; receive `message.updated` events on subscribed channel | Each affected row's payload shows `QuotedSnapshot.Deleted == &true`. |

### Migration

| # | Case | Expected |
|---|---|---|
| M1 | Run `036` on populated DB | Both columns added (NULL default), index created, no data loss. |
| M2 | Run down migration | Both columns + index removed. |
| M3 | Up applied twice | Second run no-op (IF NOT EXISTS). |

---

## 8. DO-NOT-DRIFT invariants

1. **Migration uses `IF NOT EXISTS`** on both column adds and index. Test M3.
2. **`omitempty` on quote fields** on JSON response — absent when null. Test I1, U1.
3. **Pairing invariant enforced server-side**: 400 on partial state. Test U3, U4, I2.
4. **`Deleted` is BE-owned and POST-rejected-at-decode**: `QuotedSnapshotInput` has no `Deleted` field; existing `decodeJSON` with `DisallowUnknownFields()` returns 400 `unknown field "deleted"` before reaching service. Service additionally constructs `entity.QuotedSnapshot` with `Deleted = nil` defense-in-depth. Cascade SoftDelete is the SOLE writer of `Deleted = &true`. Test U7, I3.
5. **No source-channel/message read check**: FE trusts snapshot per FE spec §12.18.
6. **Content excerpt cap 200 codepoints**: BE defensive cap via `utf8.RuneCountInString` (codepoint count). FE must use `Array.from(s).length <= 200` (codepoint count, NOT `z.string().max(200)` which counts UTF-16 units). **Cross-stack codepoint parity** — flag in FE plan code-review. Test U5, U6, U8.
7. **SoftDelete primary clears OWN quote fields** to preserve pairing (both NULL together). Test U9.
8. **SoftDelete cascade UPDATE on `quoted_message_id = $deletedId`**: sets `quoted_snapshot.deleted = true` on all related rows. Atomic with primary. Test U10.
9. **One `message.updated` event per affected row** in cascade. Test U11.
10. **Cascade transaction is atomic**: cascade failure rolls back primary. Test U12.
11. **No new external dependencies**.
12. **All Message SELECT queries project `quoted_message_id + quoted_snapshot`** (every existing SELECT path, post-ALOQA-257 set). Verifiable via grep.
13. **`ParentMessageID` is preserved through the round-trip** (POST → DB → realtime broadcast → GET) so FE quote chip can build correct `?m=X&thread=Y` deeplinks. Test U2 + I4 verify the field is present.

---

## 9. Acceptance criteria

1. All §7 tests pass (U1-U12, I1-I6, M1-M3).
2. `make lint && make test` green.
3. All 13 §8 DO-NOT-DRIFT invariants pass review.
4. Realtime `message.updated` payload includes updated quote fields on cascade delete.
5. Legacy POST body (no quote fields) returns same shape — backward compat regression.
6. SoftDelete on a message that itself has a quote produces a row with both `quoted_message_id == nil` AND `quoted_snapshot == nil` (pairing preserved).

### Manual test cases (will be `state.manual_test_cases`)

| # | Title | Steps | Expected |
|---|---|---|---|
| 1 | Legacy POST regression | `POST .../messages` body `{"content":"hi"}` | 201; response identical to pre-ALOQA-274. |
| 2 | Happy path quote POST | `POST` body with both `quoted_message_id` + `quoted_snapshot` set | 201; response echoes both fields. |
| 3 | Partial state rejected | `POST` body with only `quoted_message_id` | 400 `must be set together`. |
| 4 | Deleted-spoofing rejected at decode | `POST` body with `quoted_snapshot.deleted: true` | 400 `unknown field "deleted"` from strict JSON decoder. No row created. |
| 5 | Multi-byte excerpt boundary | `content_excerpt = strings.Repeat("я", 200)` | 201; 201-char rejected. |
| 6 | Cascade delete | Insert A; insert B, C quoting A; DELETE A; fetch B, C via curl | Both show `quoted_snapshot.deleted: true`. |
| 7 | Cascade realtime | Subscribe to WS; trigger #6 | Two `message.updated` events arrive. |
| 8 | Self-deleted quote | Insert message A that quotes M; DELETE A; fetch A | A has `quoted_message_id == null && quoted_snapshot == null`. |

---

## 10. Code-review prompt augmentation

The Codex reviewer brief MUST inline-enumerate every §7 case (U1-U12 + I1-I6 + M1-M3) AND every §8 invariant (1-13). Per ALOQA-168 lesson, "consult §X" is insufficient.

Key focus areas (highlight in brief):
- Cascade transaction atomicity (U12 must be exercised).
- Deleted-spoofing rejection (U7 must explicitly inject malicious POST body).
- SoftDelete primary clearing OWN quote fields (U9 — paired with forwarded_from clearing from ALOQA-257).
- ParentMessageID round-trip preservation across POST → DB → realtime → GET.

---

## 11. Out of scope

- Aggregate `message.quoted_deleted` event for large fanout (only if perf becomes issue; not for MVP).
- Quoted-message search index (FTS extension).
- Bidirectional reference table (e.g., `message_quotes(quoter_id, quoted_id)` for fanout queries by quoted instead of quoter) — current index is sufficient.

Related: [[aloqa_175_forward_merge]], [[aloqa_257_forward_be]] (predecessor pattern), [[aloqa_backend_changes_allowed]].
