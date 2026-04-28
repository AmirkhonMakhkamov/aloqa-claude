# Observability and SRE

This backend now exposes a first-class operational surface for media, eventing,
storage pressure, WebSocket recovery, and recording health.

## Exported metrics

- `GET /metrics`
- Prometheus text format
- Covers:
  - call quality aggregate state
  - realtime/search queue depth and dead-letter counts
  - DB/Redis pool pressure
  - WS reconnect/replay counters
  - recording pipeline totals
  - worker health and consumer lag

## Operator APIs

Workspace admins with workspace settings permissions can inspect:

- `GET /api/v1/workspaces/{workspaceID}/admin/observability/dashboard`
- `GET /api/v1/workspaces/{workspaceID}/admin/observability/alerts`
- `GET /api/v1/workspaces/{workspaceID}/admin/observability/slos`

## Alert model

The dashboard computes alerts for:

- degraded calls
- durable-event lag
- dead-letter growth
- consumer lag
- DB / Redis pressure
- WS replay failures or dropped outbound messages
- recording pipeline failures
- stalled workers

## Worker coverage

The following workers heartbeat into the observability service:

- `search_indexer`
- `realtime_outbox`
- `media_node_heartbeat`
- `media_telemetry`
- `media_relay_fabric`
- `recording_processing`
- `recording_cleanup`
- `recording_maintenance`
- `recording_lifecycle`

## SLOs

Current derived SLOs:

- degraded call ratio
- durable event lag
- consumer lag
- WS replay success rate
- recording processing success rate
- Postgres pool utilization
- Redis pool utilization

## Important note

The alerting and SLO engine is currently in-process and runtime-derived. It is
production-useful, but the next natural step is exporting the same series into
Prometheus/OpenTelemetry and attaching paging routes in your deployment stack.
