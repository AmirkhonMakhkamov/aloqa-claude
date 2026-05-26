# LiveKit Dev Observability Runbook

This subtree is a dev-only Prometheus + Grafana + LiveKit CLI companion for the
local `scripts/livekit/docker-compose.dev.yml` stack.

## Start

Start the SFU first:

```bash
docker compose -f scripts/livekit/docker-compose.dev.yml up -d
```

Then start observability:

```bash
scripts/livekit/observability/scripts/up.sh
```

Default URLs:

- Prometheus: `http://localhost:9090`
- Grafana: `http://localhost:3001` (`admin` / `admin`)
- Dashboard: Grafana folder `LiveKit`, dashboard `LiveKit Dev Fallback`

If you run the SFU with a custom compose project, export both knobs before
running `up.sh`:

```bash
export LIVEKIT_PROJECT=my-project
export LIVEKIT_NETWORK=my-project_default
```

## Status And Stop

```bash
scripts/livekit/observability/scripts/status.sh
scripts/livekit/observability/scripts/status.sh --json
scripts/livekit/observability/scripts/down.sh
```

`down.sh` only removes the observability project. It does not stop LiveKit,
Redis, or active rooms in the dev SFU.

## Metrics Inventory

Use the inventory generator after changing LiveKit image tags or dashboard
queries:

```bash
scripts/livekit/observability/scripts/generate-metrics-inventory.sh
(cd scripts/livekit/observability && scripts/validate-dashboard.py)
```

Generated files live under `.state/` and are intentionally ignored by Git.

## LiveKit CLI

The wrapper runs the pinned CLI container on the same Docker network as the
SFU:

```bash
scripts/livekit/observability/scripts/lk-runbook.sh rooms
scripts/livekit/observability/scripts/lk-runbook.sh participants <room>
scripts/livekit/observability/scripts/lk-runbook.sh token <room> <identity>
scripts/livekit/observability/scripts/lk-runbook.sh publish-test <room>
scripts/livekit/observability/scripts/lk-runbook.sh webhook-tail
```

If the upstream CLI image changes its binary name, run `docker run --rm
livekit/livekit-cli:v2.0.0 --help` and adjust only `lk-runbook.sh` plus the
inventory generator's best-effort activity section.

## Manual Dashboard Import

For richer online dashboards, import the upstream Grafana dashboard `15237`
from Grafana UI. Keep the committed fallback dashboard because it is available
offline and is validated against the captured metric inventory.

## Troubleshooting

| Symptom                                     | Check                                     | Likely Cause                                                                         | Fix                                                                                                                    |
| ------------------------------------------- | ----------------------------------------- | ------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------- |
| Prometheus target is down                   | `status.sh --json`                        | LiveKit was started before `prometheus_port: 6789` existed or the SFU is not running | Recreate the SFU with `docker compose -f scripts/livekit/docker-compose.dev.yml up -d --force-recreate livekit-server` |
| `LIVEKIT_NETWORK` not found                 | `docker network ls`                       | The SFU compose project name differs from the default repo name                      | Export `LIVEKIT_PROJECT` and `LIVEKIT_NETWORK`                                                                         |
| Grafana says datasource missing             | Grafana `Connections` page                | Provisioning did not reload after file edits                                         | Run `down.sh`, then `up.sh`                                                                                            |
| Fallback dashboard has blank LiveKit panels | Prometheus query tab                      | LiveKit did not emit room or participant series until a room exists                  | Run `lk-runbook.sh publish-test <room>` or start a browser call                                                        |
| Webhook delivery is unclear                 | `lk-runbook.sh webhook-tail`              | aloqa-claude is not listening on `0.0.0.0:8090`                                      | Start the backend or update `scripts/livekit/livekit.yaml` webhook URL                                                 |
| TURN or ICE failures                        | LiveKit logs and browser WebRTC internals | Local host candidates differ by OS and Docker backend                                | Capture `chrome://webrtc-internals` plus Grafana CPU/memory and LiveKit logs                                           |
