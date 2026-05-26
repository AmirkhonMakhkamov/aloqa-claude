# LiveKit Production Runbook

This runbook covers the production LiveKit deployment for aloqa-claude. The
local compose files in this directory are for development only; production
should render secrets from the deployment secret manager into a host, VM,
Kubernetes, or managed container setup.

Primary references:

- LiveKit deployment:
  <https://docs.livekit.io/transport/self-hosting/deployment/>
- LiveKit ports and firewall:
  <https://docs.livekit.io/transport/self-hosting/ports-firewall/>
- LiveKit distributed mode:
  <https://docs.livekit.io/transport/self-hosting/distributed/>
- LiveKit webhooks:
  <https://docs.livekit.io/intro/basics/rooms-participants-tracks/webhooks-events/>

## Production Shape

- Public signaling URL: `wss://livekit.<env>.aloqa.example`.
- Public media endpoint: `media.<env>.aloqa.example` or the LiveKit node public
  IPs when no media L4 load balancer is used.
- Public API URL for webhook callbacks:
  `https://api.<env>.aloqa.example/livekit/webhook`.
- TLS terminates at a trusted certificate chain. Do not use self-signed
  certificates for browser clients.
- LiveKit port `7880` stays behind an HTTPS/WebSocket load balancer or reverse
  proxy. Do not expose plaintext `ws://` outside trusted private networks.
- LiveKit media ports are handled as L4 traffic. Do not proxy UDP media through
  an HTTP reverse proxy or CDN path.
- Redis is required for production and for every multi-node LiveKit deployment.
- The aloqa backend remains the source of truth for calls, participants, and
  durable webhook idempotency. LiveKit rooms are external media state.

Use these example files as templates:

- `scripts/livekit/livekit.production.example.yaml`
- `scripts/livekit/aloqa.production.env.example`

## TLS And Public URL

1. Create DNS records:
   - `livekit.<env>.aloqa.example` -> signaling HTTPS/WebSocket load balancer.
   - `media.<env>.aloqa.example` -> media L4 load balancer for LiveKit
     `7881/TCP` and `50000-60000/UDP`, or document the per-node public IPs
     that LiveKit advertises when no media L4 load balancer is used.
   - `turn.<env>.aloqa.example` -> TURN L4 load balancer or LiveKit node public
     IP if no L4 balancer is used.
   - `api.<env>.aloqa.example` -> aloqa backend HTTPS load balancer.
2. Issue publicly trusted certificates for the LiveKit signaling host and TURN
   host. The TURN certificate domain must match the `turn.domain` value.
3. Set the backend env:

   ```bash
   LIVEKIT_URL=wss://livekit.<env>.aloqa.example
   LIVEKIT_API_KEY=<active-livekit-api-key>
   LIVEKIT_API_SECRET=<active-livekit-api-secret>
   LIVEKIT_TOKEN_TTL=6h
   LIVEKIT_WEBHOOK_PATH=/livekit/webhook
   ```

4. Configure LiveKit `keys` with the same API key/secret pair and configure the
   webhook URL to the public backend endpoint. The URL path must match
   `LIVEKIT_WEBHOOK_PATH`:

   ```yaml
   webhook:
     api_key: <active-livekit-api-key>
     urls:
       - https://api.<env>.aloqa.example/livekit/webhook
   ```

## TURN, ICE, And Firewall

Open only the ports required by the selected topology.

| Flow                          | Port        | Protocol | Exposure                        |
| ----------------------------- | ----------- | -------- | ------------------------------- |
| Signaling HTTPS/WSS           | 443         | TCP      | Public LB -> LiveKit `7880`     |
| LiveKit API/WebSocket backend | 7880        | TCP      | Private behind LB only          |
| ICE TCP fallback              | 7881        | TCP      | Public L4 to LiveKit nodes      |
| ICE UDP media range           | 50000-60000 | UDP      | Public L4 to LiveKit nodes      |
| TURN/TLS                      | 5349 or 443 | TCP      | Public L4 to TURN/LiveKit TURN  |
| TURN/UDP                      | 3478 or 443 | UDP      | Public L4 to TURN/LiveKit TURN  |
| TURN relay range              | 61000-62000 | UDP      | TURN nodes to LiveKit SFU       |
| LiveKit Prometheus            | 6789        | TCP      | Private monitoring network only |
| LiveKit Redis                 | 6379        | TCP      | Private network only            |

Production defaults:

- Use `rtc.port_range_start: 50000` and `rtc.port_range_end: 60000` unless the
  platform has a stricter approved range. Keep firewall rules and config in
  lockstep.
- Use `rtc.use_external_ip: true` when LiveKit runs on hosts with public IP
  discovery. If a cloud/network shape requires explicit advertised IPs, set the
  node public IP through the deployment entrypoint instead of relying on private
  container addresses.
- Prefer host networking or a CNI/L4 setup that preserves UDP reachability.
- Validate both direct UDP and TURN-relayed calls. TURN must not be treated as a
  rare fallback; enterprise networks will rely on it.
- Pin `turn.relay_range_start: 61000` and `turn.relay_range_end: 62000`, or an
  environment-approved equivalent, and mirror that range in TURN/SFU
  firewall rules. LiveKit otherwise uses arbitrary available ports for TURN
  relay traffic.
- Keep TURN capacity separate from application CPU capacity when possible. Alert
  on TURN relay ratio, relay egress, auth failures, and allocation failures.

Validation commands:

```bash
LIVEKIT_MEDIA_TARGET=media.<env>.aloqa.example
TURN_TARGET=turn.<env>.aloqa.example

nc -vz livekit.<env>.aloqa.example 443
nc -vz "${LIVEKIT_MEDIA_TARGET}" 7881
nc -vz "${TURN_TARGET}" 5349
nc -vzu "${LIVEKIT_MEDIA_TARGET}" 50000
nc -vzu "${TURN_TARGET}" 3478
nc -vzu "${TURN_TARGET}" 61000
```

Set `LIVEKIT_MEDIA_TARGET` to a specific LiveKit node public IP when the
deployment does not expose a media L4 hostname. UDP `nc` checks only prove that
packets can be sent. Browser WebRTC validation is the real connectivity gate.

## Webhook Signature Endpoint

LiveKit posts signed webhook events to:

```text
POST https://api.<env>.aloqa.example${LIVEKIT_WEBHOOK_PATH}
Content-Type: application/webhook+json
Authorization: <LiveKit-signed JWT>
```

The handler uses `webhook.ReceiveWebhookEvent` with the configured
`LIVEKIT_API_KEY` and `LIVEKIT_API_SECRET`. During a planned rotation window it
also accepts `LIVEKIT_WEBHOOK_PREVIOUS_API_KEY` and
`LIVEKIT_WEBHOOK_PREVIOUS_API_SECRET` for verification only. Expected responses:

- `204`: valid signature and event processed or duplicate event ignored.
- `401`: invalid signature, invalid body, or wrong signing key.
- `503`: backend LiveKit integration is not configured.
- `409`: another backend node is already processing the same event; LiveKit can
  retry.

Smoke checks:

```bash
LIVEKIT_WEBHOOK_PATH="${LIVEKIT_WEBHOOK_PATH:-/livekit/webhook}"

# Unsigned payload should not be accepted. In a configured environment expect 401.
curl -i -X POST "https://api.<env>.aloqa.example${LIVEKIT_WEBHOOK_PATH}" \
  -H "Content-Type: application/webhook+json" \
  --data '{}'

# Local/dev webhook tail while using the compose stack.
scripts/livekit/observability/scripts/lk-runbook.sh webhook-tail
```

Webhook durability depends on migration `043_livekit_webhook_events.sql`. Run the
Postgres DSN validation test against a disposable, fully migrated database before
staging or production rollout:

```bash
ALOQA_POSTGRES_TEST_DSN='postgres://aloqa:aloqa@127.0.0.1:5432/aloqa_test?sslmode=disable' \
  go test -run TestCallRepoClaimLiveKitWebhookEventIsDurable ./internal/repository/postgres
```

## Redis And Multi-Node Assumptions

- LiveKit production requires Redis. When Redis is configured, LiveKit uses it
  for shared room data and cluster messaging.
- Use a dedicated Redis deployment or isolated logical database for LiveKit.
  Avoid sharing hot app/session keys and LiveKit cluster traffic in the same
  unbounded keyspace.
- Redis must be reachable by every LiveKit node with low latency. Use TLS,
  auth, private networking, persistence/replication according to the cloud
  provider's production profile, and alerts on latency, memory, evictions, and
  connection errors.
- LiveKit nodes are homogeneous in distributed mode. A room is still hosted by a
  single selected node, while other signaling nodes can bridge to it.
- Multi-region deployments need region-aware DNS/LB and LiveKit node selector
  config. Do not assume one room can span multiple SFU nodes until a cascaded
  media fabric is implemented.
- Termination should rely on LiveKit draining: send SIGTERM/SIGINT/SIGQUIT and
  allow active rooms to empty before shutting the node down.

## API Key Rotation

The backend signs new room tokens with `LIVEKIT_API_KEY` and
`LIVEKIT_API_SECRET`. Webhook verification accepts that active pair first, then
the optional `LIVEKIT_WEBHOOK_PREVIOUS_API_KEY` /
`LIVEKIT_WEBHOOK_PREVIOUS_API_SECRET` pair during a controlled rotation window.
LiveKit must keep both pairs in `keys` until old join tokens and webhook retries
have expired.

Rotation procedure:

1. Generate a new LiveKit API key and a high-entropy secret in the secret
   manager.
2. Add the new pair to LiveKit `keys` while keeping the old pair.
3. Reduce `LIVEKIT_TOKEN_TTL` to `15m` for one deploy cycle if the current TTL is
   longer and wait for old join tokens to expire.
4. Deploy the backend with:

   ```bash
   LIVEKIT_API_KEY=<new-livekit-api-key>
   LIVEKIT_API_SECRET=<new-livekit-api-secret>
   LIVEKIT_WEBHOOK_PREVIOUS_API_KEY=<old-livekit-api-key>
   LIVEKIT_WEBHOOK_PREVIOUS_API_SECRET=<old-livekit-api-secret>
   ```

5. Update LiveKit `webhook.api_key` to the new key and roll LiveKit nodes. Mixed
   old/new webhook signatures are accepted during this window.
6. Verify new room tokens, room creation, and signed webhook delivery.
7. Remove the old key from LiveKit and clear the two
   `LIVEKIT_WEBHOOK_PREVIOUS_*` backend env vars after the longest token TTL
   plus webhook retry window has elapsed.

## Metrics, Logging, And Alerts

LiveKit:

- Enable `prometheus_port: 6789`.
- Scrape `/metrics` only from the private monitoring network.
- Alert on:
  - LiveKit target down or scrape missing.
  - high CPU, memory, file descriptors, network egress, or packet drops.
  - room/participant counts near capacity.
  - ICE failure rate and reconnect spikes.
  - TURN relay ratio, TURN auth failures, allocation failures, and relay egress.
  - webhook non-2xx responses, especially `401`, `409`, and `5xx`.

Aloqa backend:

- Scrape `GET /metrics`.
- Watch existing operational alerts for degraded calls, durable event lag,
  dead-letter growth, DB/Redis pool pressure, WS replay failures, recording
  failures, and stalled workers.
- Log routing keys on all LiveKit paths: `call_id`, `workspace_id`, `user_id`,
  LiveKit event `id`, and LiveKit event `event`.
- Keep API key/secret values out of logs, metrics labels, and PR descriptions.

Minimum dashboards:

- LiveKit process and scrape health.
- rooms, participants, publishers, subscribers.
- signaling errors and webhook status codes.
- media quality: packet loss, RTT, jitter, bitrate, reconnects, ICE failures.
- TURN relay percentage and egress.
- aloqa `/metrics` SLO panel for DB, Redis, outbox, and call quality alerts.

## Local Validation

Start local LiveKit and Redis:

```bash
docker compose -f scripts/livekit/docker-compose.dev.yml up -d
docker compose -f scripts/livekit/docker-compose.dev.yml logs -f livekit-server
```

Export backend env for local development:

```bash
export LIVEKIT_URL=ws://localhost:7880
export LIVEKIT_API_KEY=APIaloqaDev
export LIVEKIT_API_SECRET=aloqa-livekit-development-secret-32bytes-min
```

Start observability and run probes:

```bash
scripts/livekit/observability/scripts/up.sh
scripts/livekit/observability/scripts/status.sh --json
scripts/livekit/observability/scripts/lk-runbook.sh rooms
scripts/livekit/observability/scripts/lk-runbook.sh token smoke-room smoke-user
scripts/livekit/observability/scripts/lk-runbook.sh publish-test smoke-room
scripts/livekit/observability/scripts/generate-metrics-inventory.sh
(cd scripts/livekit/observability && scripts/validate-dashboard.py)
```

Run lightweight backend checks:

```bash
go test ./internal/service/call ./internal/handler/http ./internal/config
```

Run the DSN-gated repository test only against disposable migrated Postgres:

```bash
ALOQA_POSTGRES_TEST_DSN='postgres://aloqa:aloqa@127.0.0.1:5432/aloqa_test?sslmode=disable' \
  go test -run TestCallRepoClaimLiveKitWebhookEventIsDurable ./internal/repository/postgres
```

## Staging Validation

1. Deploy one LiveKit node, Redis, and the aloqa backend using rendered
   production-style config.
2. Confirm DNS and TLS:

   ```bash
   curl -Iv https://livekit.<env>.aloqa.example
   curl -Iv https://api.<env>.aloqa.example
   ```

3. Confirm unsigned webhook rejection:

   ```bash
   LIVEKIT_WEBHOOK_PATH="${LIVEKIT_WEBHOOK_PATH:-/livekit/webhook}"

   curl -i -X POST "https://api.<env>.aloqa.example${LIVEKIT_WEBHOOK_PATH}" \
     -H "Content-Type: application/webhook+json" \
     --data '{}'
   ```

4. Create a real aloqa call from two browser users and verify:
   - both users receive LiveKit join info with a `wss://` URL.
   - camera/microphone publish and subscribe work.
   - screen share publish/unpublish generates correct call state.
   - leaving the call triggers LiveKit webhooks and participant cleanup.
   - metrics show active room/participants and return to idle after the call.
5. Repeat from a TURN-only network profile or browser config that forces relay.
6. Scale to at least two LiveKit nodes sharing Redis. Start new rooms on both
   nodes, then terminate one node with SIGTERM and verify drain behavior.
7. Run the Postgres DSN-gated webhook idempotency test against the migrated
   staging database clone, not against production.

## Wave 1 Gaps And Release Gates

These are explicit blockers or follow-up gates from Wave 1:

- Postgres migration DSN test: `TestCallRepoClaimLiveKitWebhookEventIsDurable`
  is skipped unless `ALOQA_POSTGRES_TEST_DSN` is set. Production rollout requires
  running it against a disposable migrated database and attaching the command
  output to the release notes.
- Durable outbox / partial side effects: LiveKit room creation, deletion, and
  participant removal are external side effects. Some paths are best-effort and
  not backed by a durable outbox or reconciliation worker. Before high-volume
  production, add either a durable LiveKit side-effect outbox or a periodic
  reconciler that compares aloqa call state with LiveKit room state and repairs
  drift.
- Browser media smoke dependency: backend checks cannot prove browser capture,
  SDP negotiation, autoplay policy, device permissions, screen sharing, or
  TURN-only behavior. Staging signoff depends on a browser media smoke run with
  two users and a TURN-only scenario.
- Webhook path changes require updating both backend `LIVEKIT_WEBHOOK_PATH` and
  every LiveKit `webhook.urls` entry in the same rollout.

## Rollback

1. Disable new call starts at the aloqa feature flag or deployment layer if
   available.
2. Revert backend `LIVEKIT_*` env to the previous key/URL pair.
3. Revert LiveKit config to the previous `keys`, `webhook.api_key`, and webhook
   URL.
4. Roll LiveKit nodes one at a time with drain enabled.
5. Keep Redis and Postgres intact. Do not purge
   `call_livekit_webhook_events`; it protects idempotency during retries.
6. Watch webhook `401`/`5xx`, call join failures, and room counts until they
   return to baseline.
