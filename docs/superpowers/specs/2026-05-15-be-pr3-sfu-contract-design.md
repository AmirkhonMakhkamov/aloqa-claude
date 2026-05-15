# BE-PR3 — SFU Reality Check + FE SFU-epic Contract Audit (ALOQA-244)

> Investigation-only deliverable. **No code, no migrations.** This is the source of truth for the contract between the FE engine and the backend SFU, and gates the FE SFU epic.
>
> Spec v2 — addresses Codex review (3 blocking, 2 major, 1 minor). v1 made factually wrong claims about renegotiation, track identity, and multi-node routing. v2 documents the real state and the work the FE epic must accommodate.

## Headline finding (corrected)

The backend runs a native Pion SFU with selective forwarding (`internal/media/sfu/`), but the FE SFU epic cannot ship as a pure FE change. **Three real gaps require either BE work or FE workarounds before FE-1 (SignalingAdapter v2) is implementable**:

1. **No server-initiated renegotiation.** The signaling flow is **client-offer / server-answer** only. When a new presenter publishes a track mid-call, the SFU calls `AddTrack` on every existing subscriber PeerConnection (`internal/media/sfu/peer.go:124`, `internal/media/sfu/room.go:501`) but does **not** create a server-side offer. The current FE explicitly ignores incoming WS offers (`frontend/src/lib/webrtc/engine.ts:530`).
2. **No track→user metadata channel.** The outbound `TrackLocalStaticRTP` reuses the publisher browser's `StreamID` (`internal/media/sfu/track.go:42-45`); there is no `userId:audio` naming scheme. The FE has no reliable way to know which sender owns an incoming track.
3. **Multi-node fanout is already wired.** `PolicyForCall` routes `meeting` calls to `regional_cascade` (`internal/service/mediaops/service.go:254-267`); `EnsurePlacement` populates `media_relay_edges` (`internal/service/mediaops/service.go:353-364`); the relay fabric starts in `cmd/server/main.go:432`; and `HandleMediaOffer` rejects mismatched-edge tokens (`internal/service/call/media.go:190,545`, test at `service_test.go:411-418`). **FE-1 must honor `control_url` from the media-session token.**

These three gaps drive the rest of this document.

## What the backend actually has today

### SFU primitives (Pion-native)

- `internal/media/sfu/room.go` — Room aggregate; `AddPresenter`, `AddViewer`, `handleNewTrack`, `PlanSubscriberAdaptation`.
- `internal/media/sfu/peer.go` — Per-participant wrapper around `*webrtc.PeerConnection`; `OnTrack` observation, late `AddTrack` for new presenter streams.
- `internal/media/sfu/track.go` — `TrackLocalStaticRTP` forwarders; `ObservedTrack.SourcePeer` carries the source user **server-side only** (not exposed to clients via track metadata).
- `internal/media/sfu/observer.go` — Track observer interface used by recording capture (`internal/service/recording/capture.go`).
- `internal/media/sfu/simulcast.go` — Simulcast layer handling.

Central forwarding, **not** mesh — every participant has one PeerConnection to the SFU. Multiple presenters work; viewers subscribe to all presenters.

### Signaling HTTP endpoints (`internal/handler/http/call.go` routes wired in `router.go`)

| Method | Path (under `/api/v1/workspaces/{workspaceID}/calls/{callID}`) | Purpose |
|---|---|---|
| POST | `/media-session/token` | Issue signed media JWT; **returns `control_url`, `media_url`, `node_id` in the placement** (`media.go:153-157`) |
| POST | `/media-session/offer` | Submit SDP offer → server returns answer SDP synchronously |
| POST | `/media-session/ice-candidate` | Submit trickled ICE candidate |
| POST | `/media-session/ice-restart` | Restart ICE on transient failure |
| POST | `/media-session/quality-report` | Push QoS sample |

`HandleMediaOffer` (`media.go:230-246`) sets remote description from the browser offer, creates answer, sets local, returns answer synchronously. There is no server-initiated offer code path anywhere in the call service or SFU.

### NATS signaling subjects (corrected)

| Subject | Events |
|---|---|
| `aloqa.signal.<userID>` | Server-trickled `signal.candidate`; legacy `signal.offer`/`signal.answer` for ForwardSignal; **`call.quality.adapted`** (`media.go:595-597`) |
| `aloqa.ws.<workspaceID>` | `call.started`, `call.ended`, `call.participant.joined/left/updated` (`service.go:1213,1223`) |

v1 of the spec wrongly placed `call.quality.adapted` on `aloqa.ws.<workspaceID>`. **Quality adaptation is per-user**: the publisher path is `subject := fmt.Sprintf("aloqa.signal.%s", userID)` at `media.go:595-596`, then publishes `TypeCallQualityAdapted` to that subject at `media.go:597`. FE-1 must subscribe per-user for quality events.

### `MediaControlPlane` interface (`service.go:32-41`)

```go
type MediaControlPlane interface {
    EnsurePlacement(ctx, *entity.Call, sfu.RoomOptions) (*entity.MediaRoomPlacement, error)
    ResolveParticipantPlacement(ctx, *entity.Call, *entity.CallParticipant, preferredNodeID string) (*entity.MediaRoomPlacement, error)
    CanServeNode(ctx, *entity.Call, nodeID string) (bool, error)
    PolicyForCall(*entity.Call) entity.MediaCallPolicy
    LocalNodeID() string
    IsLocalNode(nodeID string) bool
    GetCallQualityPolicy(ctx, workspaceID, callID uuid.UUID) (*entity.MediaQualityPolicy, error)
    RecordQualitySnapshot(ctx, entity.MediaQoSSample) error
}
```

The control plane is a **scheduler**, not a relay. It picks the SFU node, applies regional policy, records QoS. SDP relay happens inside the SFU node itself.

### Recording proves N-presenter forwarding works

`internal/service/recording/capture.go` observes presenter tracks via `internal/media/sfu/observer.go` and writes Pion `.ivf` / `.ogg` chunks per source. If recording works for N presenters, the RTP forwarding code path works for N+ subscribers — same observer-based fan-out.

### Multi-node routing — **already wired**

- `PolicyForCall` (`mediaops/service.go:254-267`):
  - `one_to_one` / `group` → `single_node`
  - `meeting` → `regional_cascade`
  - `webinar` / `selector` → `webinar_edges`
- `EnsurePlacement` (`mediaops/service.go:353-364`) inserts rows into `media_relay_edges` for cascade strategies.
- `cmd/server/main.go:432` boots the relay fabric.
- `HandleMediaOffer` rejects offers if the media-session token's `node_id` doesn't match the local node (`media.go:190,545`); the FE **must** retarget its media calls to the placement's `control_url`.

v1 said this was "scaffolded but not wired" — wrong. The FE must treat the placement returned by `/media-session/token` as authoritative for the API host of `/offer`, `/ice-candidate`, `/ice-restart`, `/quality-report`.

## What the FE SFU epic needs to do

Given the 3 gaps above, the epic re-shapes as follows. Items prefixed with **(BE)** are backend changes that must land **before** the FE PR depending on them; items prefixed **(FE)** are pure FE work.

### Epic FE-1 (ALOQA-216) — SignalingAdapter v2

**(FE)** A new adapter that:

- Calls `POST /media-session/token`, reads the returned placement.
- **Honors `placement.control_url`** for all subsequent media calls (offer, ICE, restart, quality). When the FE host differs from `control_url`, the adapter sends to `control_url`.
- POSTs the local SDP offer to `/media-session/offer`, applies the returned answer.
- Streams trickle ICE: POSTs local candidates to `/media-session/ice-candidate`; subscribes to `aloqa.signal.<userID>` for server-trickled candidates and for `call.quality.adapted` events.
- Handles ICE restart via `/media-session/ice-restart` on `iceConnectionState === "failed"`.
- Periodically POSTs `RTCStatsReport`-derived QoS to `/media-session/quality-report`.

**Contract:** the existing v1 adapter keeps the 1:1 mesh path. v2 lives alongside as a parallel implementation; the engine picks v1 or v2 based on a feature flag (`calls.sfu_v2`) until v2 is stable.

### Epic FE-2 (ALOQA-217) — WebRTCEngine subscription model + dispose async

Without backend renegotiation (Gap #1), FE-2 must work around the missing server-offer flow. Two options:

**Option A (FE-only — recommended for v2):** the FE pre-negotiates a fixed number of recvonly transceivers in the initial offer (e.g., 20 audio + 20 video + 4 screen). Pion binds incoming SFU tracks to existing transceivers without renegotiation. Caps N at 20 presenters; matches a realistic ceiling for the first multi-party milestone.

**Option B (BE work needed):** add a backend SFU renegotiation offer path — when a new presenter publishes, the SFU creates an offer and pushes it via WS (`aloqa.signal.<userID>` with `signal.offer` type) for every existing subscriber. Requires changes to `internal/media/sfu/room.go` (`handleNewTrack` → trigger `peer.CreateOffer`), a new WS event type `signal.sfu_offer`, and FE side to stop ignoring incoming offers (currently dropped at `frontend/src/lib/webrtc/engine.ts:530`).

**Recommendation:** ship FE-2 with Option A first (caps N at 20, sufficient for demo-day → 100-seat call window when not all are publishing). Track Option B as a follow-up ALOQA-NEW ticket if the cap becomes painful.

`engine.dispose()` becomes async (closes the PC, waits for `connectionState === "closed"`). Resolves ALOQA-77.

### Epic FE-3 (ALOQA-218) — Multi-stream media store

**Blocked by Gap #2.** The FE cannot key remote streams by `senderUserId` from `MediaStream.id` alone — the backend forwards the publisher browser's stream ID, not a server-controlled identifier.

**(BE) required before FE-3 ships:** add a track-identity contract. Two sub-options:

**Sub-option B1 (lighter — recommended):** the backend extends `internal/media/sfu/track.go:42-45` so outbound `TrackLocalStaticRTP` is constructed with `streamID := publisher.UserID.String()` (or `<userID>:<kind>` if multiple kinds per user need to be distinguished by stream). FE keys remote streams by `senderUserId := stream.id`. **Required BE patch** — ~10 LOC plus tests.

**Sub-option B2 (heavier):** publish a `call.tracks.snapshot` WS event listing each active track with `{track_id, stream_id, user_id, kind}`. FE caches and looks up `pc.ontrack` events against the snapshot. Requires new event type, payload, publish triggers on track add/remove. ~150 LOC plus tests.

**Recommendation:** B1. Lower complexity, matches what the FE engine already assumes (`engine.ts:503,510`). The track ID space is opaque to the FE — server-controlled stream IDs are safe.

After Gap #2 is closed: `calls/media-store` stops keying remote streams by `(callId)` and starts keying by `(callId, senderUserId, kind)`. `useParticipantSpeaking` (PR8) already takes `stream: MediaStream | null` — no change.

### Epic FE-4 (ALOQA-219) — N-participant grid + auto-spotlight

**(BE) needed if FE-4 wants authoritative track state:** add `participants_with_active_tracks` to the GetCall response. v1 wrongly called this redundant. The existing `CallParticipant.AudioMuted/VideoMuted/ScreenSharing` fields are **control-plane intent**, set by the HTTP media update path (`service.go:958-984`), while actual SFU track publication is observed independently at `internal/media/sfu/peer.go:55` and `internal/media/sfu/room.go:451`. There is no `GetCall`/`GetParticipants` API that surfaces authoritative SFU track state.

**Recommendation:** ship FE-4 first against the intent fields (good-enough proxy), then add the authoritative field as a small follow-up BE patch when telemetry shows the intent/observed delta matters. Track as ALOQA-NEW.

Auto-spotlight: when grid variant is N>4, the most recent speaker (driven by `useParticipantSpeaking` + a session-shared "last loud" buffer) gets the big tile. Threshold: enter ≥ 0.20 for 100 ms; switch at most once per 800 ms to prevent flicker. Pure FE.

### Epic FE-5 (ALOQA-220) — Integration smoke

**(FE)** Vitest + Playwright + a thin SFU test stub. v1 referenced `internal/media/sfu/_test_helpers.go` — **no such file exists**. The actual SFU test files are `internal/media/sfu/room_test.go`, `sfu_test.go`, etc. — regular package tests, not exported helpers.

FE-5 needs to ship one of:

- (FE-only) A pure-Go HTTP stub mimicking `/media-session/*` endpoints with a fake Pion peer.
- (BE follow-up) Expose a thin test-only HTTP wrapper around the SFU room (gated by `BUILD_FLAG_TEST_SFU=true`); requires a small BE patch and is **not** what the v1 spec implied existed.

**Recommendation:** FE-only stub. The Playwright smoke uses real browsers but a stubbed SFU — keeps the harness portable.

## Decision matrix (corrected ordering)

| FE PR | Blocking gap | BE work needed first | Order |
|---|---|---|---|
| FE-1 SignalingAdapter v2 | Gap #3 honoring `control_url` | None (already exposed) | 1st |
| FE-2 engine subscription + dispose async | Gap #1 (Option A workaround) | None (Option A); BE renegotiation patch only for Option B | 2nd |
| FE-3 multi-stream store | Gap #2 | **YES** — B1 patch to `track.go:42-45` (`streamID := userID`) | needs BE patch first |
| FE-4 N-grid + auto-spotlight | None (ships against intent fields) | Optional: `participants_with_active_tracks` patch (follow-up) | 4th |
| FE-5 integration smoke | None | None (FE-only stub) | 5th |

**Net BE work created by this audit:** exactly one small BE patch (B1, ~10 LOC + tests) to fix the track-identity contract before FE-3. Optional follow-up: server-initiated renegotiation if Option A's 20-participant cap becomes a problem.

## Pion vs LiveKit vs mediasoup — unchanged recommendation

| Criterion | Pion-native (current) | LiveKit | mediasoup |
|---|---|---|---|
| Go-native | ✅ | partial | ❌ |
| Team familiarity | ✅ | ⚠ | ❌ |
| Recording integration | ✅ wired | extra service | extra service |
| Multi-node cascading | wired (regional/webinar) | built-in | built-in |
| Operational overhead | low (in-process) | high (separate service) | high (separate service) |

LiveKit becomes attractive only at very large webinar scale (10k+ viewers / call). For the demo-day → 100-seat call window, Pion-native is the right tool.

## Risks / unknowns

1. **20-participant cap from FE-2 Option A.** If Calls scales beyond 20 active publishers before Option B lands, large meetings will fail to negotiate. Mitigation: telemetry on participant count + alert at 18.
2. **Simulcast layer subscription** — Pion stack supports simulcast (`simulcast.go`), but the FE adapter does not yet select layers. Out of scope for the first SFU epic; tracked separately.
3. **BWE per subscriber** — current SFU has no built-in per-subscriber bandwidth estimation. `PlanSubscriberAdaptation` is reactive (reads QoS, lowers quality). For 20+ subscribers we may need proactive BWE. Out of scope.
4. **TURN credential rotation** — currently static env vars (`cmd/server/main.go:153-164`). Long-term per-call TURN credentials via the media-session token endpoint. Out of scope.
5. **`media_relay_edges` activation surface** — already wired in the backend but the FE has no observability for it (no UI surface to show "you are connected via edge-2"). Acceptable for now.

## Out of scope (re-stated)

- Backend code, migrations, schema changes **except the B1 track-identity patch** which is required before FE-3 and **must be scheduled as a separate small BE PR after this design is approved**.
- LiveKit / mediasoup migration
- Server-initiated renegotiation (Option B)
- Simulcast layer selection UX
- Per-call TURN credential rotation
- E2EE for media (`Call.Settings.E2EE` flag exists but is not enforced today)

## Deliverables

This document is the deliverable. Sign-off from the architect unblocks the FE SFU epic and creates one additional Jira ticket: **"BE — Fix outbound track stream ID to user ID (B1)"** as a precondition for FE-3 (ALOQA-218). No backend code lands from BE-PR3 itself; the corresponding Jira (ALOQA-244) moves to Done when this doc merges to `develop`.
