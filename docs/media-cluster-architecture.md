# Media Cluster Architecture

This backend now includes a first-class media control plane on top of the in-process Pion SFU. The goal is to move beyond single-process strength and make placement, routing, fanout, and QoS observability explicit.

## What Is Implemented

- Live media node heartbeats are published into Redis so horizontally scaled app/media nodes can advertise their region, URLs, room capacity, and current load.
- Durable room placements are stored in Postgres in `media_room_placements`, which makes routing sticky and inspectable.
- Server-side QoS samples are stored in Postgres in `media_qos_samples`, which gives us durable packet-loss, jitter, RTT, bitrate, and byte-history per call/user/stream.
- Workspace admins with call-moderation permission can read:
  - `GET /admin/media/nodes`
  - `GET /admin/media/topology`
  - `GET /admin/media/calls/{callID}/qos`
- Media join tokens now include routing metadata:
  - `node_id`
  - `region`
  - `control_url`
  - `media_url`
  - `routing_mode`
  - `fanout_strategy`
  - `overflow_policy`
  - `screen_share_priority`
  - `turn_strategy`
- Offer/ICE/restart handlers now reject requests on the wrong node instead of silently serving media on any random app instance.

## Multi-Node SFU Strategy

The system now treats media as a cluster problem, not just a local in-memory room:

1. A call gets a deterministic placement.
2. That placement is durable.
3. Clients are told which control/media edge should serve the call.
4. The serving node becomes the sticky media edge for that room.

Current placement rules:

- Prefer nodes in the same configured media region as the current node.
- Filter out draining or overloaded nodes.
- Hash the call ID across eligible nodes for deterministic distribution.
- Fall back according to overflow policy if the preferred region is full.

That gives us stable routing without requiring a central synchronous coordinator on every token request.

## Routing Rules

- `sticky_edge` means the room stays on one serving edge once placed.
- `regional_edge` is used for high-scale webinar-style flows where we expect media fanout to stretch beyond a single locality.
- Media tokens are node-scoped. If a token says `node_id=edge-b` and the request lands on `edge-a`, the API returns `UNAVAILABLE` and the client should reconnect to the advertised control URL.

## Regional Media Edge Design

The code now carries region and endpoint metadata per node and per placement. That enables the next deployment layer:

- Run API/media nodes in multiple regions.
- Publish region-specific `control_url` and `media_url`.
- Keep calls sticky inside one region by default.
- Spill over to another region only when policy allows it.

This is the foundation for regional ingress plus cross-region webinar fanout.

## TURN Fleet Strategy

TURN is now an explicit policy field on media placements and join tokens through `turn_strategy`.

Current default:

- `regional_turn_pool`

Recommended deployment shape:

- Separate TURN from app/API nodes.
- Deploy regional TURN pools close to media edges.
- Monitor relay ratio, RTT, relay egress, and auth failures separately from the SFU fleet.

## Room Overflow Policies

Room overflow is now policy-driven by call type:

- `one_to_one`: reject overflow
- `group`: reject overflow
- `meeting`: regional spillover when the preferred region is full
- `webinar` / `selector`: webinar fanout policy

This is encoded in durable room placement so operators can see why a call was routed the way it was.

## Screen Share Prioritization

Screen share priority is now part of the media placement policy:

- ordinary calls default to balanced behavior
- meetings, webinars, and selectors use protected screen-share priority

The intent is:

- preserve readable screen content under congestion
- avoid aggressive degradation of slide/demo streams
- still keep audio as the first safety target

This complements the existing adaptive controller, which is still audio-first.

## Participant Caps By Call Type

The media policy layer now enforces hard per-type ceilings independently from user-supplied settings:

- `one_to_one`
- `group`
- `meeting`
- `webinar`
- `selector`

If a call asks for more than the hard cap, runtime admission uses the policy cap instead.

## Webinar Fanout Strategy

For very large webinar audiences:

- below the fanout threshold, the policy can stay on a regional cascade
- above the threshold, the placement switches to `webinar_edges`

This is still a control-plane policy, not yet a full cascaded media replication fabric. The next deployment step is to run dedicated webinar edges or CDN-like passive fanout.

## Server-Side Telemetry As Truth

The backend now collects server-side Pion stats on an interval and stores them durably.

Collected sources include:

- selected ICE candidate pair
- inbound RTP stats
- remote inbound RTP stats

Persisted QoS fields include:

- packet loss
- jitter
- RTT
- available incoming bitrate
- available outgoing bitrate
- bytes sent
- bytes received
- participant role
- media kind
- connection state metadata

This makes the server-side view the durable operational truth for dashboards and call-history inspection.

## Operator Dashboards

The current operator surface is JSON API based:

- live node inventory
- workspace media topology
- per-call QoS history and summary

That gives us enough structure to build a UI dashboard later without needing to change the core backend contract again.

## Remaining Gaps

This is a strong step, but not the final end-state for a globally distributed media platform.

Still needed for true top-tier production scale:

- real cross-node room forwarding or cascaded SFU replication
- regional anycast or geo-aware edge discovery from the client side
- dedicated TURN fleet automation and autoscaling
- time-series metrics export to Prometheus/OpenTelemetry
- visual operator dashboards
- alerting on packet loss, RTT, relay ratio, and node overload
- node draining and placement rebalance workflows
- true webinar edge fanout implementation beyond control-plane policy
