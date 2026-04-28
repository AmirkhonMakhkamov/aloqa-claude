# Storage Reliability

## What changed
- Postgres now applies pool, statement, lock, and idle-in-transaction controls from config.
- Redis now exposes configurable pool sizing, pool timeout, retry count, and operation timeout.
- Background workers use a shared timeout/retry policy for idempotent storage work.
- Background workers back off when the Postgres pool is saturated instead of competing with request traffic.
- Presence online membership is sharded across Redis keys to avoid a single hot workspace key.
- Migration files are validated at server startup for numbering gaps, duplicates, and filename safety.
- Storage runtime and audit reports are available from admin endpoints:
  - `GET /api/v1/workspaces/{workspaceID}/admin/storage/runtime`
  - `GET /api/v1/workspaces/{workspaceID}/admin/storage/audit`

## Retry rules
- Request-path writes: no automatic retry unless the operation is known to be idempotent.
- Request-path reads and cache lookups: bounded timeout, retry only on transient transport/pool errors.
- Background workers: exponential backoff with bounded retries and per-attempt timeouts.
- Permanent application errors are never retried.

## Timeout policy
- Postgres connection timeout, statement timeout, lock timeout, and idle-in-transaction timeout are configured centrally.
- Redis dial/read/write/pool timeouts are configured centrally.
- Worker batch operations run with bounded per-attempt timeouts.

## Pagination review
- Message timelines and thread replies already use cursor pagination and should remain keyset-based.
- Search, audit log, and recording admin listings still use offset pagination today.
- Before very large scale, move those administrative feeds to cursor or keyset pagination with stable tie-breakers.

## Index and archival strategy
- Added reliability indexes for recordings, QoS samples, realtime cleanup, and search-job cleanup.
- Large operational tables to archive or prune first:
  - `realtime_events`
  - `search_index_jobs`
  - `media_qos_samples`
  - `audit_log`
  - `notifications`
- Recommended approach:
  - short hot retention
  - periodic cold export or partition pruning
  - explicit legal-hold exceptions where required

## Hot-key notes
- Workspace presence uses sharded online sets to reduce write contention on large workspaces.
- Session-scoped presence remains the source of truth; workspace membership is derived from session activity.
- For very large enterprise tenants, the next step would be paged presence reads or role-filtered directory views instead of full workspace scans.
