# Performance And Latency

This backend can enforce several low-latency and smoothness foundations, but final guarantees require deployment-level SLOs, client measurements, and media-edge capacity planning.

## Calls

- WebRTC stack: Pion SFU with AV1, VP9, VP8, Opus, optional experimental Lyra for native clients/gateways.
- Packet recovery and feedback: NACK, RTCP reports, TWCC, and simulcast headers are enabled.
- Admission control: per-node rooms, per-call presenters/viewers, active screen-share streams, and per-presenter track counts.
- Adaptive bitrate: receivers report per-stream available bitrate, packet loss, RTT, jitter, audio loss/jitter, dropped frames, decode time, freezes, NACKs/PLIs, device class, low-power mode, and screen-share context.
- The SFU applies per-subscriber simulcast layer switches without renegotiation by replacing the sender track when Pion allows it.
- The adaptive decision is audio-first: emergency audio degradation bypasses normal anti-flap delay, forces the lowest available video layer, and can recommend temporarily suspending video until speech recovers.
- The adaptive response also gives receiver guidance for max video bitrate, max FPS, audio/video target jitter buffers, lip-sync window, sync mode, and degradation mode.
- Tunable deployment knobs: layer bitrate floors, critical-video floor, upswitch/downswitch hysteresis, required good/poor samples, and EWMA smoothing alpha.
- Smoothness requirements still needed: server-side RTCP stats ingestion, regional media-edge selection, TURN relay-rate alerts, browser/TURN E2E tests, and production SLO dashboards for freeze ratio, audio underruns, RTT, packet loss, and join time.

## Chat

- Message delivery should remain workspace-scoped and channel-authorized before indexing, notifications, or WebSocket fanout.
- Realtime fanout should use durable event/outbox semantics for external integrations and at-least-once delivery for app marketplace webhooks.
- Delivery SLOs should be measured separately for API write latency, broker publish latency, WebSocket fanout latency, and notification latency.

## Recordings And AI

- Recordings move from `recording` to `processing` to `ready`.
- When processing succeeds, the service emits a `recording.ready` hook with recording ID, call ID, storage path, duration, file size, and format.
- AI transcription/summary/analyze workers should consume this hook asynchronously and read the artifact from storage with workspace-scoped authorization.

## Workspace Boundary

- Workspace membership is the core authorization boundary.
- Cross-workspace user visibility and contact must go through workspace connection policy, not global user lookup.
- Custom workspace roles should grant explicit permissions rather than relying on title/name matching.
