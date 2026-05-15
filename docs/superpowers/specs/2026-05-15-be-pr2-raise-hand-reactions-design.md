# BE-PR2 — raise_hand + emoji reactions ephemeral broadcast (ALOQA-243)

> Pure WebSocket broadcast surface for in-call hand-raise toggle and floating-emoji reactions. **No DB persistence.** Powers FE PR7 ALOQA-214.
>
> Spec v2 — addresses Codex review (3 blocking, 7 high, 1 minor).

## Goal

Two ephemeral interactions during a live call:

1. **Raise / lower hand** — a participant signals they want to speak; UI shows a hand icon on their tile and a host-side "raised hands" list.
2. **Emoji reaction** — a participant fires off a single-glyph reaction. Floating animation client-side, ~3s lifetime.

Server does the minimum: validate the caller is `connected` in the call, fan out to call participants. **No state retained on the server.**

Audience is **call-scoped via per-user signal subjects**, not workspace-wide — the existing per-user signaling pattern from `publishICECandidate` (`internal/service/call/media.go:595-597`) is the precedent. Each event is published once per current call participant to that participant's `aloqa.signal.<userID>` subject. This guarantees the audience is exactly current call participants without inventing a new subject type.

Out of scope: persisting raised-hand history, reaction tallies, custom emoji, message-scoped reactions (chat already has those).

## Surface

### Emoji format — align with chat

Reactions use the **same emoji format as chat reactions** (`internal/service/chat/service.go:904-917`, `entity.Reaction.Emoji` at `internal/domain/entity/message.go:40-45`):

- Non-empty
- Max 32 bytes
- Valid UTF-8

No fixed allow-list. FE picks a curated UI palette but the backend trusts any valid emoji string. This keeps the call-vs-chat surface uniform and avoids future migration when the FE wants to add emoji.

### Service methods (`internal/service/call/interactions.go`, new file)

```go
func (s *Service) RaiseHand(ctx context.Context, workspaceID, callID, userID uuid.UUID) error
func (s *Service) LowerHand(ctx context.Context, workspaceID, callID, userID uuid.UUID) error
func (s *Service) SendCallReaction(
    ctx context.Context,
    workspaceID, callID, userID uuid.UUID,
    emoji string,
) error
```

Access checks (all three):

1. `requireCallAccess(ctx, workspaceID, callID, userID)` (`service.go:199-220`) — workspace + channel boundary.
2. `GetParticipant(ctx, callID, userID)` must exist with `Status == ParticipantStatusConnected` (the enum value is `connected`, confirmed at `entity/call.go` for the `ParticipantStatus` type). If missing → `cerrors.Forbidden("not in call")`.
3. If `call.Status == CallStatusEnded` → `cerrors.Forbidden("call is not active")`. Matches breakout precedent at `breakout.go:123-124` and signal/media path at `service.go:1158-1159`.

Reaction emoji validation reuses the same logic as `chat.Service.AddReaction` (`chat/service.go:904-917`):

```go
emoji = strings.TrimSpace(emoji)
if emoji == "" || len(emoji) > 32 || !utf8.ValidString(emoji) {
    return cerrors.InvalidInput("invalid emoji")
}
```

No rate-limit enforcement at the service layer. Rate-limiting is deferred to a follow-up ticket because the existing router only has `httprate.LimitByIP` (`router.go:70,86`); a per-user-per-call limiter is not in the codebase and inventing one is out of scope for this PR. The handler may still apply the existing `httprate.LimitByIP` middleware to the new routes — this prevents one IP from flooding, even if it does not enforce per-user fairness.

### Event types (`internal/domain/event/events.go`)

```go
const (
    TypeCallHandRaised  Type = "call.participant.hand_raised"
    TypeCallHandLowered Type = "call.participant.hand_lowered"
    TypeCallReaction    Type = "call.reaction.added"
)
```

`call.reaction.added` follows the dominant `<aggregate>.<past_tense>` convention (`message.created`, `reaction.added`, `call.started`). The hand events keep `call.participant.<action>` to slot beside `call.participant.joined/left/updated`.

Payloads:

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

Add explicit cases to `DefinitionForType` (`events.go:107-127`):

```go
case TypeCallHandRaised, TypeCallHandLowered, TypeCallReaction:
    return Definition{
        DeliverySemantic: DeliveryEphemeral,
        Replayable:       false,
    }
```

Without this case, the default fallthrough returns `DeliveryAtLeastOnce` + `Replayable: true`, which would queue them in the outbox and defeat the no-persistence goal.

### Publish path

The service publishes directly via the existing call signaling pattern from `internal/service/call/service.go:1180-1199` (signaling events for `signal.offer` / `signal.answer` / `signal.candidate` are ephemeral and direct-published). This is the closer precedent than `typing.started` (which uses `hub.BroadcastToRoom` in the WS handler).

```go
func (s *Service) publishCallInteraction(
    ctx context.Context,
    call *entity.Call,
    eventType event.Type,
    payload any,
) error {
    // Resolve audience: current call participants.
    participants, err := s.calls.ListParticipants(ctx, call.ID)
    if err != nil { return err }

    for _, p := range participants {
        if p.Status != entity.ParticipantStatusConnected { continue }
        evt := event.Prepare(event.Event{
            Type:        eventType,
            Subject:     fmt.Sprintf("aloqa.signal.%s", p.UserID),
            WorkspaceID: call.WorkspaceID,
            UserID:      &p.UserID,
            Payload:     payload,
        })
        if err := s.doPublish(ctx, evt); err != nil { return err }
    }
    return nil
}
```

**`event.Prepare` is called explicitly** so the wire envelope carries `Version`, `Subject`, `DeliverySemantic`, and `Replayable`. The existing `doPublish` at `internal/service/call/service.go:1267-1287` marshals whatever is on `event.Event` — calling `Prepare` first fills the fields the spec relies on.

Per-user subject (`aloqa.signal.<userID>`) is the existing pattern used for signaling and quality-adapted events; it is already in the WS subscription allow-list (`internal/handler/ws/handler.go:403-414`) and is self-only — no audience leak risk. Late joiners do **not** receive a snapshot of raised hands; they hear via voice or chat. This is acceptable for ephemeral state.

### HTTP handlers (`internal/handler/http/call_interactions.go`, new)

Routes mount inside the existing `r.Route("/{callID}", ...)` block at `router.go:275-282`, **without** restating the workspace prefix (which is already mounted at `/api/v1/workspaces/{workspaceID}` per `router.go:135-143`):

```go
r.Route("/{callID}", func(r chi.Router) {
    // ... existing routes ...
    r.Post("/hand", deps.Calls.RaiseHand)
    r.Delete("/hand", deps.Calls.LowerHand)
    r.Post("/reactions", deps.Calls.SendCallReaction)
})
```

External URLs:

- `POST   /api/v1/workspaces/{workspaceID}/calls/{callID}/hand`
- `DELETE /api/v1/workspaces/{workspaceID}/calls/{callID}/hand`
- `POST   /api/v1/workspaces/{workspaceID}/calls/{callID}/reactions`

`workspaceID` is read via `middleware.WorkspaceIDFromContext(r.Context())` (per `internal/middleware/workspace.go:52-68`).

Request body for reactions:

```json
{ "emoji": "🎉" }
```

Responses: `204 No Content` for all three. No echo of state — FE renders optimistically and trusts the broadcast.

The handler applies the existing `httprate.LimitByIP` middleware (already in `router.go:70,86`). The exact per-user-per-call rate-limit story is a follow-up; this PR ships with the existing IP limiter only.

### Hand-raise state — explicitly stateless

The server does not persist or cache raised-hand state. Consequences:

1. `GetCall` does not include a `raised_hands` array.
2. On reconnect, hand state is lost — accepted trade-off.
3. A future "list currently raised hands" requirement would add an in-memory map keyed by `callID -> set<userID>` inside the call service (no DB). Out of scope here.

## Test plan

### Service (`internal/service/call/interactions_test.go`)

Hand-rolled fakes pattern (matches `service_test.go`).

- RaiseHand when caller is `connected` → publishes one `call.participant.hand_raised` per current `connected` participant (assert via fake publish capture).
- RaiseHand when caller is `disconnected` → `CodeForbidden` ("not in call").
- RaiseHand when caller is not a participant → `CodeForbidden`.
- RaiseHand on `Status=ended` call → `CodeForbidden` ("call is not active").
- LowerHand mirrors RaiseHand (same matrix).
- SendCallReaction with valid UTF-8 emoji → publishes one `call.reaction.added` per `connected` participant.
- SendCallReaction with empty emoji → `CodeInvalidInput`.
- SendCallReaction with 33-byte emoji → `CodeInvalidInput`.
- SendCallReaction with invalid UTF-8 bytes → `CodeInvalidInput`.
- All three: ensure `event.Prepare` is called by asserting the captured event has `DeliverySemantic == DeliveryEphemeral` and `Replayable == false`.

### HTTP (`internal/handler/http/call_interactions_test.go`)

- 204 on each happy-path POST/DELETE.
- 401 unauthenticated, 403 not-in-call, 403 on ended call.
- 400 on bad emoji.
- `workspaceID` reads from middleware context (no path-segment fallback).

**Rate-limit assertion deferred.** The existing IP limiter is process-global and the focused-test scenario in v1 of the spec ("two reactions in <200 ms") would be coupled to global state. v2 drops the timing assertion; the limiter is applied transparently and its behavior is covered by existing IP-limiter tests elsewhere.

## Risks

1. **N participants × N publishes** — a single reaction with 50 in-call participants does 50 NATS publishes. NATS handles this trivially; if it ever shows up in telemetry we can batch into a fan-out subject (`aloqa.call.<callID>`) in a follow-up.
2. **Race with LeaveCall** — caller can be removed between request and `GetParticipant`. Returns `CodeForbidden` and FE retries silently.
3. **Ordering across recipients** — independent NATS publishes mean two participants may see reactions in slightly different orders. Acceptable for ephemeral floating-emoji UX.

## Out of scope (re-stated)

- Persisting hand-raise / reaction history
- Hand-raise snapshot on reconnect
- Reaction tallies (lobby-wide counters)
- Custom emoji upload
- Per-user-per-call rate limiting (separate Jira)
- Hand-raise auto-clear after N minutes
- New `aloqa.call.<callID>` subject (would require WS handler authorization changes)
