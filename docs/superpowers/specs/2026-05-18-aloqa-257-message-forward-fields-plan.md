# ALOQA-257 — BE Implementation plan

**Spec:** [2026-05-18-aloqa-257-message-forward-fields-design.md](./2026-05-18-aloqa-257-message-forward-fields-design.md)
**Profile:** `pr-sync --codex-led`
**Branch:** `pr-sync/ALOQA-257` (worktree `aloqa-claude-prsync-ALOQA-257`)

Single small PR. 4 phases × 5 commits. Each phase ends `make lint && make test` green.

---

## Phase A — Migration (1 commit)

**Goal:** persist column live before any code references it.

1. Create `migrations/035_messages_forward_from.sql` (per spec §3) with `ALTER TABLE messages ADD COLUMN IF NOT EXISTS forwarded_from jsonb DEFAULT NULL;` + the expression index `CREATE INDEX IF NOT EXISTS idx_messages_forwarded_from_message_id ON messages ((forwarded_from->>'message_id')) WHERE forwarded_from IS NOT NULL;`. Wrap in BEGIN/COMMIT.
2. Create `migrations/down/035_messages_forward_from.down.sql` per spec §3.
3. **Repo has NO Makefile migration recipe** (Makefile declares `migrate-up`/`migrate-down` in `.PHONY` but provides no recipe — verified). Apply locally with explicit psql:
   ```bash
   PGPASSWORD=... psql -h localhost -U aloqa -d aloqa_dev -f migrations/035_messages_forward_from.sql
   ```
   Per [[vultr_staging_deploy_2026_05_14]] staging deploys via `deploy.sh` which applies SQL files. The migration filename follows the `NNN_<name>.sql` convention used by the deploy script's globbing.
4. Smoke check: `psql ... -c '\d messages'` shows new `forwarded_from` column.

**Verify:** apply up → apply down → apply up again (idempotent — `IF NOT EXISTS` on both column and index guarantees this).

Commit: `feat(db): migration 035 messages.forwarded_from jsonb column (ALOQA-257)`.

---

## Phase B — Entity + repository (1 commit)

1. **Entity** (`internal/domain/entity/message.go`): add `ForwardedFrom json.RawMessage \`json:"forwarded_from,omitempty" db:"forwarded_from"\`` field to `Message` struct. Add `encoding/json` import if not already present.
2. **Repository INSERT** (`internal/repository/postgres/message.go:38-64`):
   - Add `forwarded_from` to the INSERT column list and the VALUES placeholder list.
   - Bind `entity.Message.ForwardedFrom` (pgx handles `json.RawMessage` → jsonb natively since `RawMessage` is `[]byte` underneath).
   - `nil` bind → SQL NULL.
3. **Repository SELECT projections**: every SELECT that returns Message rows must include `forwarded_from` in the column list AND the row-scan must populate the new field. Touch all of:
   - `GetByID(ctx, messageID)`
   - `ListByChannel(ctx, channelID, opts)`
   - Thread queries (`ListByParent`, etc.)
   - Any FTS / search queries that return Message
   - Pinned message queries
   - Use `git grep` for `SELECT.*FROM messages` inside `internal/repository/postgres` to enumerate.
4. **pgxscan struct tag**: confirm `db:"forwarded_from"` tag is respected. If the repo uses positional scans, append to the scan target list in the same column order.
5. **Soft-delete privacy fix** (`internal/repository/postgres/message.go:370-393` `SoftDelete`): extend the UPDATE statement to also `SET forwarded_from = NULL`. Without this, a soft-deleted forwarded message still exposes the embedded snapshot content via subsequent reads (privacy leak — Codex R1 B2). After the change:
   ```sql
   UPDATE messages
   SET content = '',
       edited = false,
       edited_at = NULL,
       pinned = false,
       pinned_by = NULL,
       pinned_at = NULL,
       forwarded_from = NULL,
       updated_at = $2,
       deleted_at = $2
   WHERE id = $1 AND deleted_at IS NULL
   ```
   Add a `MessageRepo.SoftDelete` regression test: insert a message with `forwarded_from = jsonb`, call SoftDelete, GetByID, assert `ForwardedFrom == nil`.

**Verify:** `make lint && go test ./internal/repository/postgres/...` green.

Commit: `feat(repo): persist + project messages.forwarded_from (ALOQA-257)`.

---

## Phase C — Service + handler (1 commit)

1. **Service input** (`internal/service/chat/service.go:236-238`): extend `SendMessageInput` to include `ParentID *uuid.UUID` (if not already a field — verify; current state per recon has only `Content`) and `ForwardedFrom json.RawMessage`. **Drop only the `min=1` part** of the `validate:"required,min=1,max=40000"` tag from `Content` — keep `required,max=40000` so the empty-content gate moves to the manual check. Actually per spec §8.9 the cleanest approach is: remove the entire tag from `Content` (so `validate.Struct(input)` doesn't reject empty content) AND add a manual conditional check in `SendMessage` body. Other input structs in the file keep their validators untouched.
2. **Service signature** (`internal/service/chat/service.go:625-630`): change to struct input

   ```go
   func (s *Service) SendMessage(
       ctx context.Context,
       channelID, userID uuid.UUID,
       input SendMessageInput,
   ) (*entity.Message, error)
   ```

   Update the body **preserving the existing `validate.Struct(input)` call** (per spec §8.9 — the call is NOT removed, only the `Content` tag inside the struct is dropped):
   - Keep `if err := validate.Struct(input); err != nil { return nil, err }` — validates ParentID UUID-ness, any future tags, etc.
   - Add the conditional content validation per spec §4.4: if `len(input.ForwardedFrom) == 0 && len(input.Content) < 1` → 400 "content is required". If `len(input.Content) > 40000` → 400 "content must be at most 40000 characters".
   - Add JSON-validity probe on `input.ForwardedFrom` when non-empty: `var probe interface{}; if err := json.Unmarshal(input.ForwardedFrom, &probe); err != nil` → 400 "forwarded_from must be valid JSON".
   - Build the `entity.Message` with `ForwardedFrom: input.ForwardedFrom` (nil propagates to NULL).
   - All other existing logic (channel access via `GetAccessibleChannel`, parent validation, broadcast) unchanged.
3. **HTTP handler** (`internal/handler/http/message.go:25-28`): extend `sendMessageRequest` to include `ForwardedFrom json.RawMessage \`json:"forwarded_from,omitempty"\``. Remove the `validate:"required,min=1,max=40000"` tag from the handler-struct `Content` field (since validation lives in service now and the request-tag would short-circuit before the service runs — verify that the handler does NOT call validate.Struct on `req`; existing pattern is `decodeJSON` only). Map `*string ParentID` to `*uuid.UUID` and pass the new struct input to `s.svc.SendMessage`.
4. Update all other callers of the old `SendMessage(ctx, channelID, userID, content, parentID)` signature to use the new struct form (find via `git grep s.SendMessage\|svc.SendMessage\|Service.SendMessage`). The call sites are likely few (HTTP handler + tests).

**Verify:** `make lint && go test ./internal/service/chat/... ./internal/handler/http/...` green.

Commit: `feat(chat): SendMessage accepts forwarded_from + conditional content rule (ALOQA-257)`.

---

## Phase D — Tests + manual verify (2 commits)

### D1 — Unit + integration tests

1. Extend `internal/service/chat/service_test.go` per spec §7 U1–U8.
2. Extend the existing HTTP handler test file (find via `git grep -l "TestSendMessage\|sendMessage"` — likely `internal/handler/http/message_test.go` or a sibling) per spec §7 I1–I6. If no test file exists, create one mirroring `call_message_test.go` pattern.
3. **Soft-delete privacy regression** (`internal/repository/postgres/message_test.go` if exists, else service-level integration test): insert message with `forwarded_from = jsonb`, call `SoftDelete`, fetch via `GetByID`, assert returned `ForwardedFrom == nil` AND `Content == ""`.
4. Migration tests M1–M3 — manual cycle on a dev DB via the explicit `psql -f` commands (no Makefile recipe per Phase A note); document the verification command run in commit message.

**Verify:** `make lint && make test` green end-to-end.

Commit: `test(chat): forward_from validation + persistence + omitempty regression (ALOQA-257)`.

### D2 — README / CHANGELOG (optional, only if repo convention requires)

1. If the repo has a CHANGELOG.md / docs/migration-notes.md / README CHANGES section, add a one-line entry noting the new `forwarded_from` field.

Commit: `chore(docs): note forwarded_from addition in changelog (ALOQA-257)` — skip if no changelog convention.

---

## Pre-PR checklist

- [ ] `git rebase origin/develop` clean.
- [ ] `make lint && make test` green.
- [ ] Migration applied + reverted + reapplied on dev DB (M1–M3).
- [ ] Manual smoke: §9 cases 1–8 from spec executed against local dev.
- [ ] `state.manual_test_cases` populated.
- [ ] All 10 §8 DO-NOT-DRIFT invariants self-verified.

---

## Commit log shape (target)

```
docs(ALOQA-257): forward fields spec + plan                  (already landed)
feat(db): migration 035 messages.forwarded_from jsonb        (Phase A)
feat(repo): persist + project messages.forwarded_from        (Phase B)
feat(chat): SendMessage accepts forwarded_from               (Phase C)
test(chat): forward_from validation + persistence            (Phase D1)
```

Squash on merge into a single commit.
