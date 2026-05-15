# BE-PR3 — SFU + Signaling-v2 Contract Audit (ALOQA-244)

> Investigation-only deliverable. **No code, no migrations.** This document is the gate for the FE SFU epic (ALOQA-216..220) and the source of truth for the contract between the FE engine and the backend SFU.

## Headline finding

**The backend already runs a native Pion SFU with selective forwarding.** The "1:1 mesh" assumption that drove the FE Calls polish session applies only to the FE engine (`@aloqa/core/webrtc`). The backend's `MediaControlPlane`, `internal/service/call/media.go`, `internal/media/sfu/`, and the recording pipeline are all designed around presenter ingest + observer-based fan-out.

The work needed to ship N-party calls is **entirely on the frontend**: replace the 1:1 mesh logic with a multi-stream subscription model that talks to the existing backend SFU endpoints.

## What the backend has today

### SFU primitives (Pion-native)

- `room.AddPresenter(userID, peerConnection)` and `room.AddViewer(userID, peerConnection)` in `internal/media/sfu/`
- Per-track RTP forwarding via observer interface (used by recording capture at `internal/service/recording/capture.go`)
- Central forwarding, **not** mesh — every participant has one PeerConnection to the SFU regardless of N

### Signaling HTTP endpoints (`internal/handler/http/call.go`)

| Method | Path | Purpose |
|---|---|---|
| POST | `/calls/{id}/media-session/token` | Issue signed media JWT |
| POST | `/calls/{id}/media-session/offer` | Submit SDP offer → server returns answer SDP synchronously |
| POST | `/calls/{id}/media-session/ice-candidate` | Submit trickled ICE candidate |
| POST | `/calls/{id}/media-session/ice-restart` | Restart ICE on transient failure |
| POST | `/calls/{id}/media-session/quality-report` | Push QoS sample |

### Signaling out-of-band (NATS pubsub)

- Server-issued trickle ICE → subject `aloqa.signal.<userID>`, payload `event.SignalCandidatePayload`
- Quality adapt notifications → `aloqa.ws.<workspaceID>`, type `call.quality.adapted`

### Existing media-control-plane interface (`internal/service/call/service.go:32-41`)

```go
type MediaControlPlane interface {
    EnsurePlacement(ctx, call, opts) (*MediaRoomPlacement, error)
    ResolveParticipantPlacement(...) (*MediaRoomPlacement, error)
    CanServeNode(ctx, call, nodeID) (bool, error)
    PolicyForCall(call) MediaCallPolicy
    LocalNodeID() string
    IsLocalNode(nodeID) bool
    GetCallQualityPolicy(ctx, workspaceID, callID) (*MediaQualityPolicy, error)
    RecordQualitySnapshot(ctx, sample MediaQoSSample) error
}
```

The control plane is a **scheduler**, not a relay. It picks the SFU node, applies regional policy, records QoS. SDP relay happens inside the SFU node itself.

### Recording proves the SFU works

`internal/service/recording/capture.go` observes presenter tracks via the SFU's track-observer interface and writes Pion `.ivf` / `.ogg` chunks. If recording works for N presenters, fan-out works for N viewers — same RTP forwarding code path.

### Cascading + webinar fan-out — scaffolded, not wired

- `024_media_relay_edges.sql` defines `media_relay_edges` table: `(call_id, target_node_id, role_scope, fanout_strategy, status)`
- `MediaCallPolicy.fanout_strategy` enum: `single_node | regional_cascade | webinar_edges`
- Default today: `single_node` (sticky_edge). All participants in one call connect to the same SFU node.
- Multi-node cascading is the next milestone after the FE SFU epic (out of scope here).

## What the frontend needs to do

The existing FE `@aloqa/core/webrtc` engine assumes a **single peer connection**. To consume the backend SFU, the FE must:

### FE-1 (ALOQA-216) — `SignalingAdapter v2`

A new adapter that:

- Issues the media-session token via `POST /media-session/token`.
- POSTs the local SDP offer to `/media-session/offer`, applies the returned answer.
- Streams trickle ICE: POSTs local candidates to `/media-session/ice-candidate`; subscribes to `aloqa.signal.<userID>` for server-trickled candidates.
- Handles ICE restart via `/media-session/ice-restart` on `iceConnectionState === "failed"`.
- Periodically POSTs `RTCStatsReport`-derived QoS to `/media-session/quality-report`.

**Contract:** the existing v1 adapter (`SignalingAdapter` interface in `packages/core/src/webrtc/`) keeps the 1:1 mesh path. v2 lives alongside as a parallel implementation; the engine picks v1 or v2 based on a feature flag until v2 is stable.

### FE-2 (ALOQA-217) — `WebRTCEngine` subscription model + `engine.dispose()` async

- Engine maintains **one** RTCPeerConnection (no longer two — the SFU is the only remote).
- Engine subscribes to incoming tracks via `pc.ontrack`. Each track maps to a `(senderUserId, kind)` tuple resolved from track-metadata (server-injected stream IDs follow the convention `userId:audio` / `userId:video` / `userId:screen`).
- `engine.dispose()` becomes async (closes the PC, waits for `connectionState === "closed"`). Resolves ALOQA-77.

### FE-3 (ALOQA-218) — Multi-stream media store

- `calls/media-store` stops keying remote streams by `(callId)` and starts keying by `(callId, senderUserId, kind)`.
- The store is consumed by `ParticipantTile` (existing).
- `useParticipantSpeaking` (PR8 just shipped) already takes a `stream: MediaStream | null` prop — no change needed.

### FE-4 (ALOQA-219) — N-participant grid + auto-spotlight

- `ParticipantGrid` already has `grid` / `spotlight` / `active-1on1` variants (PR2 shipped). The grid variant supports N>2 in layout but does not subscribe to N>2 streams today.
- **Auto-spotlight**: when grid variant is N>4, the most recent speaker (driven by `useParticipantSpeaking` + a session-shared "last loud" buffer) gets the big tile. Threshold: enter ≥ 0.20 for 100 ms; switch happens at most once per 800 ms to prevent flicker.

### FE-5 (ALOQA-220) — Integration smoke

- Vitest + Playwright + a local Pion SFU stub (the existing test harness in `internal/media/sfu/_test_helpers.go` can be exposed via a thin gRPC/HTTP wrapper).
- 5-participant smoke test: all start, all see all others, all leave, no leaks.

## Backend changes needed (audit conclusion)

**None for FE-1 through FE-3.** The existing endpoints + NATS subjects are sufficient.

**Minor for FE-4** (optional, can land in a separate BE-PR):
- Add `participants_with_active_tracks` field to `GetCall` response so the FE knows who is actively publishing without subscribing first. Today the FE infers this from `CallParticipant.AudioMuted/VideoMuted`, which is good enough but slightly stale.

**For FE-5**, expose the SFU test stub via a documented test-only HTTP endpoint (`/test/sfu/echo`) gated by `BUILD_FLAG_TEST_SFU=true`. Not for production builds.

## Pion vs LiveKit vs mediasoup — decision

The audit recommends **staying with Pion-native + the existing room/observer abstractions**. Reasons:

| Criterion | Pion-native (current) | LiveKit | mediasoup |
|---|---|---|---|
| Go-native | ✅ | ❌ (Go SDK only) | ❌ (Node/C++) |
| Team familiarity | ✅ (already shipped) | ⚠ | ❌ |
| Recording integration | ✅ already wired | extra service tier | extra service tier |
| Multi-node cascading | scaffolded (relay_edges) | built-in | built-in |
| Operational overhead | low (in-process) | high (separate service) | high (separate service) |

LiveKit becomes attractive only at very large webinar scale (10k+ viewers / call). For the demo-day → 100-seat call window, Pion-native is the right tool.

## Risks / unknowns

1. **Simulcast** — the Pion stack supports it but the recording observer does not yet subscribe to per-layer streams. If we want adaptive layer subscription in FE-4, the backend must surface available simulcast layers via the offer SDP. Audit says: yes, this works; the SFU forwards all RTP streams the publisher offers.
2. **Bandwidth caps** — current SFU has no built-in BWE per subscriber. Quality adaptation today is reactive (`PlanSubscriberAdaptation` reads QoS samples and lowers stream quality). For 20+ participant calls we'll need proactive BWE. Out of scope for the SFU epic; tracked separately.
3. **TURN credential rotation** — currently static env vars. Long-term we need per-call TURN credentials issued via the media-session token endpoint. Out of scope.

## Decision matrix for FE epic ordering

| FE PR | Blocks | Risk | Order |
|---|---|---|---|
| FE-1 SignalingAdapter v2 | everything else | high (new protocol surface) | first |
| FE-2 Engine subscription + dispose async | FE-3, FE-4 | medium | second |
| FE-3 Multi-stream store | FE-4 | low | third |
| FE-4 N-grid + auto-spotlight | FE-5 | medium (UX tuning) | fourth |
| FE-5 Integration smoke | none | low | fifth |

Recommend: ship FE-1 + FE-2 together as a single PR behind a feature flag (`calls.sfu_v2`). FE-3/4/5 follow once FE-1+2 are validated against staging.

## Out of scope (re-stated)

- Backend code, migrations, schema changes
- LiveKit / mediasoup migration
- Multi-node cascading work (relay_edges activation)
- Simulcast layer selection UX
- Per-call TURN credential rotation
- E2EE for media (`Call.Settings.E2EE` flag exists but is not enforced today; deferred)

## Deliverables

This document is the deliverable. Sign-off from architect + frontend lead unblocks the FE SFU epic. No backend PR ships from BE-PR3; the corresponding Jira (ALOQA-244) moves to Done when this doc is merged to `develop`.
