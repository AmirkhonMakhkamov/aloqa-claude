# ALOQA-274 — BE Implementation plan

**Spec:** [2026-05-18-aloqa-274-message-quote-fields-design.md](./2026-05-18-aloqa-274-message-quote-fields-design.md)
**Profile:** `pr-sync --codex-led`
**Branch:** `pr-sync/ALOQA-274` (worktree `aloqa-claude-prsync-ALOQA-274`)

4 phases × ~6 commits. Each phase ends `make lint && make test` green.

---

## Phase A — Migration (1 commit)

1. Create `migrations/036_messages_quote_fields.sql` per spec §3 (ADD COLUMN IF NOT EXISTS quoted_message_id uuid NULL + quoted_snapshot jsonb NULL; CREATE INDEX IF NOT EXISTS idx_messages_quoted_message_id ... WHERE quoted_message_id IS NOT NULL). BEGIN/COMMIT wrapper.
2. Create `migrations/down/036_messages_quote_fields.down.sql` (DROP INDEX + DROP COLUMNs).
3. Apply locally via explicit psql (no Makefile recipe — per [[ALOQA-257 plan A]]):
   ```bash
   PGPASSWORD=... psql -h localhost -U aloqa -d aloqa_dev -f migrations/036_messages_quote_fields.sql
   ```
4. Smoke: `psql ... -c '\d messages'` shows both new columns + index.

**Verify:** apply up → down → up again (idempotent).

Commit: `feat(db): migration 036 messages.quoted_message_id + quoted_snapshot columns (ALOQA-274)`

---

## Phase B — Entity + repository (2 commits)

### B1 — Entity + INSERT/SELECT projections + own-row pairing-preserving SoftDelete

1. **Entity** (`internal/domain/entity/message.go`): add `QuotedSnapshot` typed struct + `QuotedMessageID *uuid.UUID` + `QuotedSnapshot *QuotedSnapshot` on `Message`. All `omitempty` json tags. `parent_message_id *uuid.UUID` field on QuotedSnapshot for thread deeplink support.
2. **Repository INSERT** (`internal/repository/postgres/message.go:38-64`): add `quoted_message_id` + `quoted_snapshot` to INSERT column list + VALUES. pgx handles `*uuid.UUID` natively; `*entity.QuotedSnapshot` marshals via `json.Marshal` then bind as jsonb `[]byte`.
3. **Repository SELECT** projections: every SELECT path that loads Message includes both new columns:
   - `GetByID`, `ListByChannel`, thread queries, FTS, pinned message queries.
   - Use `git grep "SELECT.*FROM messages"` to enumerate. (After ALOQA-257 also added `forwarded_from` so the projection-touching set should mirror that change.)
4. **Existing `SoftDelete(ctx, id) error` extended** with own-row quote-field clearing for pairing preservation:
   ```sql
   UPDATE messages
   SET content = '', edited = false, edited_at = NULL,
       pinned = false, pinned_by = NULL, pinned_at = NULL,
       forwarded_from = NULL,
       quoted_message_id = NULL, quoted_snapshot = NULL,  -- NEW
       updated_at = $2, deleted_at = $2
   WHERE id = $1 AND deleted_at IS NULL
   ```
   The signature stays `error` (backward compat with all existing callers). Add a regression test asserting both quote fields become NULL after SoftDelete on a message that itself had a quote.

**Verify:** `make lint && go test ./internal/repository/postgres/...` green.

Commit: `feat(repo): persist + project messages quote fields + clear OWN quote fields in SoftDelete (ALOQA-274)`

### B2 — SoftDeleteWithCascade new method

1. **Extend repository interface** (`internal/domain/repository/interfaces.go`):
   ```go
   type MessageRepository interface {
       // ...existing methods including SoftDelete(ctx, id) error...
       SoftDeleteWithCascade(ctx context.Context, id uuid.UUID) (affectedQuoteRowIDs []uuid.UUID, err error)
   }
   ```
2. **Implement** in `internal/repository/postgres/message.go` per spec §4.6. Both primary UPDATE (with all field clears including quote fields) AND cascade UPDATE on `quoted_message_id = $deletedId` run inside the same `r.beginTx`. Returns affected row IDs via `RETURNING id` from the cascade UPDATE.
3. **Mocks**: update repo mocks (if any exist) to satisfy the extended interface — `git grep mocks.MessageRepository` to find.
4. Tests `internal/repository/postgres/message_test.go` for the new method:
   - U9 (own-row pairing) — verifies SoftDelete (the old method) clears both quote fields.
   - U10 (cascade fanout) — verifies SoftDeleteWithCascade returns N affected IDs and each row has `quoted_snapshot.deleted=true`.
   - U12 (atomicity) — simulate cascade failure → primary rolled back; `deleted_at` still NULL on the target.

**Verify:** `make lint && make test` green.

Commit: `feat(repo): SoftDeleteWithCascade returns affected quote-row IDs for service event emission (ALOQA-274)`

---

## Phase C — Handler + service (2 commits)

### C1 — Handler input types + service input

1. **Define `QuotedSnapshotInput`** EXPORTED in `internal/service/chat/inputs.go` (or in the handler package; choose whichever already exports send-message input types — `git grep type SendMessageInput`). The handler imports it, decodes JSON directly into it. **No `Deleted` field** — `DisallowUnknownFields()` rejects `deleted: true` at decode time.
2. **Handler `sendMessageRequest`** (`internal/handler/http/message.go:25-28`): add `QuotedMessageID *string` (`validate:"omitempty,uuid"`) and `QuotedSnapshot *QuotedSnapshotInput` fields.
3. **Define `ParsedQuotedSnapshotInput`** in service package (UUIDs + time, no Deleted). Handler converts `QuotedSnapshotInput` → `ParsedQuotedSnapshotInput` via `uuid.Parse` + `time.Parse(time.RFC3339, ...)`. Parse errors → `cerrors.InvalidInput("invalid created_at")` etc → 400.
4. **`SendMessageInput`** (`internal/service/chat/service.go:236-238`): add `QuotedMessageID *uuid.UUID` + `QuotedSnapshot *ParsedQuotedSnapshotInput`.

**Verify:** `make lint && make typecheck` (Go has no separate typecheck — `go build ./...` covers it).

Commit: `feat(api): QuotedSnapshotInput handler + ParsedQuotedSnapshotInput service input (ALOQA-274)`

### C2 — Service `SendMessage` body + cascade integration in `DeleteMessage`

1. **`SendMessage` body** extension per spec §4.4:
   - Pairing invariant check (returns `cerrors.InvalidInput("quoted_message_id and quoted_snapshot must be set together")` on partial state).
   - Excerpt cap via `utf8.RuneCountInString` ≤ 200 (returns `cerrors.InvalidInput("quoted_snapshot.content_excerpt must be at most 200 characters")`).
   - Build `entity.QuotedSnapshot` with `Deleted = nil` explicitly (drops any client-supplied value; defense in depth on top of decode rejection).
2. **`DeleteMessage`** (`internal/service/chat/service.go:857`) extension per spec §4.6:
   - Replace `scope.Messages().SoftDelete(ctx, messageID)` with `scope.Messages().SoftDeleteWithCascade(ctx, messageID)`. Capture `affectedQuoteRows`.
   - Existing `s.enqueueEventTx(ctx, scope, event.TypeMessageDeleted, ...)` call stays unchanged.
   - For each `rowID` in `affectedQuoteRows`: `scope.Messages().GetByID(ctx, rowID)` → `s.enqueueEventTx(ctx, scope, event.TypeMessageUpdated, fmt.Sprintf("aloqa.chat.%s", updated.ChannelID), updated.WorkspaceID, updated.ChannelID, userID, event.MessagePayload{Message: updated})`.
   - **Non-tx fallback path** (the `else` branch using `publishEvent` directly): replicate the cascade event emission via `s.publishEvent(...)` for each affected row.
3. Service tests `internal/service/chat/service_test.go`:
   - U1-U8, U11 per spec §7.
   - Mock the messages repo to return predetermined `affectedQuoteRows` and assert events enqueued correctly.

**Verify:** `make lint && make test` green.

Commit: `feat(chat): SendMessage accepts quote fields + DeleteMessage cascades message.updated events (ALOQA-274)`

---

## Phase D — Tests + manual verify (2 commits)

### D1 — Integration + handler tests + decode-boundary regression

1. Extend handler tests (`internal/handler/http/message_test.go` or sibling): I1-I6 per spec §7.
2. **Decode-boundary regression**: POST body with `quoted_snapshot.deleted: true` → assert 400 `{"error":{"code":"INVALID_INPUT","message":"...unknown field..."}}`. Confirms `DisallowUnknownFields` is the security gate.
3. **Pairing 400 cases**: I2 + manual #3.
4. **Multi-byte boundary**: U8 (200 runes pass, 201 fail).
5. Realtime cascade I6 — simulate WS subscribe + DELETE → asserts `TypeMessageUpdated` events arrive per affected row.

**Verify:** `make lint && make test` green.

Commit: `test(chat): quote fields validation + cascade events + decode-boundary regression (ALOQA-274)`

### D2 — Migration tests (optional inline note)

1. M1/M2/M3 (idempotency, up+down cycle) — manual psql cycle on dev DB documented in PR body if no automated migration test exists.

---

## Pre-PR checklist

- [ ] `git rebase origin/develop` clean.
- [ ] `make lint && make test` green.
- [ ] Migration applied + reverted + reapplied on dev DB (M1-M3).
- [ ] Manual smoke (spec §9 #1-#8) executed.
- [ ] `state.manual_test_cases` populated.
- [ ] All 13 §8 DO-NOT-DRIFT invariants self-verified.

---

## Commit log shape (target)

```
docs(ALOQA-274): forward fields spec + plan                          (already landed)
feat(db): migration 036 messages quote fields                        (Phase A)
feat(repo): persist + project quote fields + own-row pairing         (Phase B1)
feat(repo): SoftDeleteWithCascade affected-rows                      (Phase B2)
feat(api): QuotedSnapshotInput + ParsedQuotedSnapshotInput           (Phase C1)
feat(chat): SendMessage quote validation + DeleteMessage cascade events (Phase C2)
test(chat): quote fields validation + cascade + decode regression    (Phase D1)
```

Squash on merge.
