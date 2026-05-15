# BE-PR2 — raise_hand + emoji reactions ephemeral broadcast (ALOQA-243)

> Pure WebSocket broadcast surface for in-call hand-raise toggle and floating-emoji reactions. **No DB persistence.** Powers FE PR7 ALOQA-214.

## Goal

Two ephemeral interactions during a live call:

1. **Raise / lower hand** — a participant signals they want to speak. UI shows a hand icon on their tile and a host-side "raised hands" list.
2. **Emoji reaction** — a participant fires off a 👏 / 🎉 / ❤️ / 😂 / 👍 / 👎 (6 emoji, fixed allow-list). Floating animation client-side, ~3s lifetime.

Server does the minimum: validate the caller is `connected` in the call, fan out to other in-call participants. **No state retained on the server.**

Out of scope: persisting raised-hand history, custom emoji, reaction tallies, message reactions (those exist already for chat).

## Why ephemeral

- Hand-raise is a real-time state — a refresh shouldn't show stale raised hands. Late joiners do not see existing raised hands; the host announces "Alice's hand is up" via voice or chat if needed. ALOQA-77 SFU work may revisit this later.
- Emoji reactions are 3-second visual events with no recall value. Storing them would be needless write amplification.

## Surface

### Service methods (`internal/service/call/interactions.go`, new file)

```go
func (s *Service) RaiseHand(ctx context.Context, workspaceID, callID, userID uuid.UUID) error
func (s *Service) LowerHand(ctx context.Context, workspaceID, callID, userID uuid.UUID) error
func (s *Service) BroadcastReaction(
    ctx context.Context,
    workspaceID, callID, userID uuid.UUID,
    emoji string,
) error
```

Reaction `emoji` is validated against a fixed allow-list at the handler boundary (`"+1" "-1" "tada" "clap" "heart" "joy"` — short codes, not unicode, to keep FE-side glyph swapping trivial). 6 codes only; any other value returns `CodeInvalidInput`.

Access check for all three: caller must be `connected` in CallParticipant for that call. Reuse `requireCallAccess` + `GetParticipant` status check. If the call has ended (`Status=ended`) return `CodeConflict`.

No rate-limit enforcement at the service layer for the spec — the HTTP handler applies a coarse limit (see below).

### WS events (`internal/domain/event/events.go`)

```go
const (
    TypeCallHandRaised   Type = "call.participant.hand_raised"
    TypeCallHandLowered  Type = "call.participant.hand_lowered"
    TypeCallReaction     Type = "call.reaction.broadcast"
)
```

Payloads:

```go
type CallHandPayload struct {
    CallID uuid.UUID `json:"call_id"`
    UserID uuid.UUID `json:"user_id"`
}

type CallReactionPayload struct {
    CallID uuid.UUID `json:"call_id"`
    UserID uuid.UUID `json:"user_id"`
    Emoji  string    `json:"emoji"` // short code
}
```

All three are `Ephemeral` delivery semantic (`Definition.DeliverySemantic = EphemeralDelivery`). They are **not** queued in the outbox, **not** replayable. The server skips `enqueueRealtimeTx` and calls `publishEvent` (NATS inline broadcast at `service.go:1267 doPublish`).

Broadcast subject: `aloqa.ws.<workspaceID>`. Matches the existing call-event audience.

### HTTP handlers (`internal/handler/http/call.go` — add three handlers next to existing call ops)

```
POST   /workspaces/{wsID}/calls/{callID}/hand          → RaiseHand
DELETE /workspaces/{wsID}/calls/{callID}/hand          → LowerHand
POST   /workspaces/{wsID}/calls/{callID}/reactions     → BroadcastReaction
```

Request body for reactions: `{ "emoji": "tada" }`.

Responses: `204 No Content` for all three. No echo of state — the FE renders optimistically and trusts the broadcast for the final visual.

Rate limit at handler level using existing `httprate` middleware if available: 5 reactions/sec/user per call, 10 hand toggles/min/user per call. Tunable via config in a follow-up; defaults must prevent abuse.

### Hand-raise state (in-memory transient, optional)

The server does **not** persist raised-hand state. Two consequences:

1. The Get-call response does not include a `raised_hands` array. The FE tracks it from the live WS stream.
2. On reconnect, hand state is lost — accepted trade-off for ephemeral simplicity.

If a future host-side "list raised hands right now" requirement appears, we'll add an in-memory map keyed by `callID -> set<userID>` inside `service.Hub` (no DB). For PR2 this is explicitly deferred.

## Test plan

### Service (`internal/service/call/interactions_test.go`)

- RaiseHand when caller is `connected` → publishes `call.participant.hand_raised`.
- RaiseHand when caller is `disconnected` → `CodeForbidden`.
- RaiseHand when caller is not a participant → `CodeForbidden` (NotFound on GetParticipant).
- RaiseHand on `Status=ended` call → `CodeConflict`.
- LowerHand mirrors RaiseHand (same matrix).
- BroadcastReaction valid emoji `"tada"` → publishes `call.reaction.broadcast`.
- BroadcastReaction invalid emoji `"💩"` → `CodeInvalidInput`.
- BroadcastReaction with empty emoji → `CodeInvalidInput`.

### HTTP (`internal/handler/http/call_interactions_test.go`)

- 204 on each happy-path POST/DELETE.
- 401 unauthenticated, 403 not-in-call.
- 400 on bad emoji.
- 409 on ended call.
- Two rapid reactions in <200 ms → first 204, second 429 (rate limited).

### Integration

Verify the existing WS test harness picks up the new event types — payload deserialization round-trips through `event.Prepare`.

## Risks

1. **Rate-limit middleware absence** — if `httprate` is not already on this router group, we add it and validate against existing chat send. Defer config knobs to a follow-up Jira.
2. **Allow-list churn** — adding a 7th emoji requires a backend release. Acceptable: the 6 chosen cover ~95% of demo-day reactions; longer-term plan is to widen via config.
3. **Race with LeaveCall** — caller can be removed from CallParticipant between request arrival and `GetParticipant`. We return `CodeForbidden` and the FE retries silently. No data loss.

## Out of scope

- Persisting hand-raise / reaction history
- Reaction tallies (lobby-wide counters)
- Custom emoji upload
- Rate-limit config UI
- Hand-raise auto-clear after N minutes
- Server-side reactions feed (e.g., "5 people clapped" digest)
