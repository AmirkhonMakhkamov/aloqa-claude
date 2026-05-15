# BE-PR2 — raise_hand + emoji reactions Implementation Plan (ALOQA-243)

> Implementation plan for [PR2 spec](2026-05-15-be-pr2-raise-hand-reactions-design.md).

## Branch + base

- Branch: `feature/ALOQA-243-raise-hand-reactions` (carries spec + plan).
- Base: `origin/develop` at `434b752`.

## Step-by-step (commit after each)

### Step 1 — Event types + DefinitionForType case

**Modify:** `internal/domain/event/events.go`:

- Append after `TypeCallQualityAdapted` (line 48):

```go
TypeCallHandRaised  Type = "call.participant.hand_raised"
TypeCallHandLowered Type = "call.participant.hand_lowered"
TypeCallReaction    Type = "call.reaction.added"
```

- Add payload types in the payload section:

```go
type CallHandPayload struct {
    CallID uuid.UUID `json:"call_id"`
    UserID uuid.UUID `json:"user_id"`
}

type CallReactionPayload struct {
    CallID uuid.UUID `json:"call_id"`
    UserID uuid.UUID `json:"user_id"`
    Emoji  string    `json:"emoji"`
}
```

- Extend `DefinitionForType` (`events.go:107-127`) ephemeral case to include the three new types:

```go
case TypeTypingStarted, TypeSignalOffer, TypeSignalAnswer, TypeSignalCandidate,
     TypeCallHandRaised, TypeCallHandLowered, TypeCallReaction:
    return Definition{
        Version:          CurrentVersion,
        DeliverySemantic: DeliveryEphemeral,
        Replayable:       false,
    }
```

**Commit:** `feat(calls): event types for hand raise + reactions`

### Step 2 — Service methods + publishCallInteraction helper

**Create:** `internal/service/call/interactions.go`:

```go
package call

import (
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "time"
    "unicode/utf8"

    "github.com/google/uuid"

    "aloqa/internal/domain/entity"
    "aloqa/internal/domain/event"
    "aloqa/internal/pkg/cerrors"
)

// callReactionMaxBytes is the same UTF-8 byte cap chat enforces
// (internal/service/chat/service.go:904-917).
const callReactionMaxBytes = 32

// RaiseHand publishes call.participant.hand_raised to current connected
// participants.
func (s *Service) RaiseHand(ctx context.Context, workspaceID, callID, userID uuid.UUID) error {
    call, err := s.requireActiveInCallParticipant(ctx, workspaceID, callID, userID)
    if err != nil {
        return err
    }
    return s.publishCallInteraction(ctx, call, event.TypeCallHandRaised, event.CallHandPayload{
        CallID: call.ID,
        UserID: userID,
    })
}

// LowerHand is the mirror of RaiseHand.
func (s *Service) LowerHand(ctx context.Context, workspaceID, callID, userID uuid.UUID) error {
    call, err := s.requireActiveInCallParticipant(ctx, workspaceID, callID, userID)
    if err != nil {
        return err
    }
    return s.publishCallInteraction(ctx, call, event.TypeCallHandLowered, event.CallHandPayload{
        CallID: call.ID,
        UserID: userID,
    })
}

// SendCallReaction broadcasts a single emoji reaction to in-call participants.
func (s *Service) SendCallReaction(ctx context.Context, workspaceID, callID, userID uuid.UUID, emoji string) error {
    if emoji == "" || len(emoji) > callReactionMaxBytes || !utf8.ValidString(emoji) {
        return cerrors.InvalidInput("invalid emoji")
    }
    call, err := s.requireActiveInCallParticipant(ctx, workspaceID, callID, userID)
    if err != nil {
        return err
    }
    return s.publishCallInteraction(ctx, call, event.TypeCallReaction, event.CallReactionPayload{
        CallID: call.ID,
        UserID: userID,
        Emoji:  emoji,
    })
}

// requireActiveInCallParticipant resolves the call, ensures it is active,
// and ensures the caller is connected. Matches breakout precedent at
// internal/service/call/breakout.go:123-152 — `Status != CallStatusActive`
// (rejects both `ringing` and `ended`) and distinguishes NotFound from
// Internal errors.
func (s *Service) requireActiveInCallParticipant(ctx context.Context, workspaceID, callID, userID uuid.UUID) (*entity.Call, error) {
    call, err := s.requireCallAccess(ctx, workspaceID, callID, userID)
    if err != nil {
        return nil, err
    }
    if call.Status != entity.CallStatusActive {
        return nil, cerrors.Forbidden("call is not active")
    }
    participant, err := s.calls.GetParticipant(ctx, callID, userID)
    if err != nil {
        if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
            return nil, cerrors.Forbidden("not in call")
        }
        return nil, cerrors.Internal("failed to load participant", err)
    }
    if participant.Status != entity.ParticipantStatusConnected {
        return nil, cerrors.Forbidden("not in call")
    }
    return call, nil
}

// publishCallInteraction fans out an ephemeral event to each connected
// participant's aloqa.signal.<userID> subject. event.Prepare is called
// per recipient so the wire envelope carries the ephemeral DeliverySemantic.
func (s *Service) publishCallInteraction(
    ctx context.Context,
    call *entity.Call,
    evtType event.Type,
    payload any,
) error {
    participants, err := s.calls.ListParticipants(ctx, call.ID)
    if err != nil {
        return cerrors.Internal("failed to list participants", err)
    }
    for _, p := range participants {
        if p.Status != entity.ParticipantStatusConnected {
            continue
        }
        subject := fmt.Sprintf("aloqa.signal.%s", p.UserID)
        _, body, _, err := event.Prepare(subject, event.Event{
            Type:        evtType,
            WorkspaceID: call.WorkspaceID,
            UserID:      p.UserID,
            Timestamp:   time.Now(),
            Payload:     payload,
        })
        if err != nil {
            slog.ErrorContext(ctx, "failed to prepare call interaction",
                "type", evtType, "error", err)
            continue
        }
        if err := s.pubsub.Publish(ctx, subject, body); err != nil {
            slog.ErrorContext(ctx, "failed to publish call interaction",
                "type", evtType, "subject", subject, "error", err)
        }
    }
    return nil
}
```

No JSON-marshal calls outside `event.Prepare` — that helper already does the JSON encoding (`events.go:146`).

**Commit:** `feat(calls): hand raise + reaction service methods`

### Step 3 — HTTP handlers + router

**Create:** `internal/handler/http/call_interactions.go`. Important: `reactionRequest` already exists at `internal/handler/http/message.go:187` (same `package http`) — use **`callReactionRequest`** to avoid redeclaration. The error helper is `writeErr`, not `writeError` (see `response.go:42`). UUID parsing uses `id.Parse(chi.URLParam(...))`, the same pattern as `call.go:62`:

```go
type callReactionRequest struct {
    Emoji string `json:"emoji"`
}

func (h *CallHandler) RaiseHand(w http.ResponseWriter, r *http.Request) {
    ws := middleware.WorkspaceIDFromContext(r.Context())
    callID, err := id.Parse(chi.URLParam(r, "callID"))
    if err != nil {
        writeErr(w, cerrors.InvalidInput("invalid call id"))
        return
    }
    userID := middleware.UserIDFromContext(r.Context())
    if err := h.svc.RaiseHand(r.Context(), ws, callID, userID); err != nil {
        writeErr(w, err)
        return
    }
    w.WriteHeader(http.StatusNoContent)
}
// LowerHand mirrors RaiseHand.
// SendCallReaction decodes callReactionRequest, calls h.svc.SendCallReaction, returns 204.
```

**Modify:** `internal/handler/http/router.go` — inside `r.Route("/{callID}", ...)` (around line 275-282):

```go
r.Post("/hand", deps.Calls.RaiseHand)
r.Delete("/hand", deps.Calls.LowerHand)
r.Post("/reactions", deps.Calls.SendCallReaction)
```

The existing global `httprate.LimitByIP` (line 70) applies automatically. No per-route middleware added.

**Commit:** `feat(calls): HTTP routes for hand raise + reactions`

### Step 4 — Service tests

**Create:** `internal/service/call/interactions_test.go`. The existing **`capturingPublisher`** at `internal/service/call/service_test.go:572` is already in the call package — reuse it directly. Its current `Publish(_, subject, _)` ignores the body, so extend it (or add a sibling `capturingPublisherWithBody`) that retains `subject + body` per call:

```go
// Either extend service_test.go:572 in-place (add a Body capture field) or
// add a new type next to it that records both fields. Keep both call-package
// helpers in service_test.go to share state across this test file.
type capturedPublish struct {
    subject string
    body    []byte
}
```

Cases per spec §"Test plan / Service":

- RaiseHand connected → publishes one event per connected participant; subject is `aloqa.signal.<userID>`; unmarshal body asserts `delivery_semantic: "ephemeral"`, `replayable: false`, `type: "call.participant.hand_raised"`, payload has correct `call_id` + `user_id`.
- RaiseHand disconnected → `Forbidden("not in call")`, no publishes.
- RaiseHand not participant → `Forbidden`, no publishes.
- RaiseHand on ended call → `Forbidden("call is not active")`.
- LowerHand same matrix.
- SendCallReaction valid emoji → publishes `call.reaction.added` with `emoji` field.
- SendCallReaction empty/33-byte/invalid-UTF-8 → `InvalidInput`.

Run: `go test ./internal/service/call/... -run "Interaction|Hand|Reaction"`

**Commit:** `test(calls): hand raise + reaction service tests`

### Step 5 — HTTP tests

**Create:** `internal/handler/http/call_interactions_test.go`:

- 204 on each happy-path POST/DELETE.
- 401 unauthenticated, 403 not-in-call, 403 on ended call.
- 400 on bad emoji (empty / oversize / invalid UTF-8 / malformed JSON).
- `workspaceID` reads from middleware context.

No rate-limit timing assertions.

Run: `go test ./internal/handler/http/... -run "Hand|Reaction"`

**Commit:** `test(calls): hand raise + reaction HTTP tests`

### Step 6 — Full verify

```bash
go vet ./...
go test ./... -timeout 60s
go build ./cmd/server
```

Fix any failures: `chore(calls): PR2 verify gate fixes`

### Step 7 — Stop and report

```
READY FOR PR
Last commit: <sha>
Tests added: <count>
Files changed: <list>
```

## Verification gates

| Gate | When | Command |
|---|---|---|
| Service | after Step 4 | `go test ./internal/service/call/... -run "Hand\|Reaction"` |
| HTTP | after Step 5 | `go test ./internal/handler/http/... -run "Hand\|Reaction"` |
| Full | after Step 6 | `go vet ./... && go test ./... && go build ./cmd/server` |

## Out of scope (re-stated)

Persisting hand-raise / reaction history, hand snapshot on reconnect, reaction tallies, custom emoji upload, per-user-per-call rate limiting (separate Jira).

## Risks

1. **N × N publishes** — 50-participant call × 1 reaction = 50 NATS publishes + 50 `event.Prepare` calls. Acceptable for demo-day window; revisit with fan-out subject if telemetry shows pressure.
2. **`ListParticipants` per interaction** — 1 DB read per request. Acceptable until per-user rate-limit lands.
3. **`pubsub` `Publish` errors are logged not propagated** — partial fan-out (some recipients succeed, some fail) is acceptable for ephemeral events. The function still returns nil if the loop completes; if `ListParticipants` itself fails, the whole call returns 500.
