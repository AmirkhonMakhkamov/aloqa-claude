-- Heal "zombie call" replay seeds: durable `call.started` events for calls that
-- have already ended. The realtime backend stores call.started with
-- replayable=true and re-delivers the replayable backlog to any freshly
-- (re)subscribed socket. A brand-new tab therefore received weeks-old
-- call.started events for long-ended calls and seeded a phantom in-call surface
-- (a "zombie call" with a 400+ hour timer) until the FE reconcile poll tore it
-- down. The code fix in this release tombstones call.started inside the
-- atomic call-end CTE (postgres/call.go) so NEW ended calls never leave a
-- replayable start event; this backfill heals the rows already in the database.
-- The compare is AS TEXT so a malformed event body can never raise a uuid cast
-- error. Idempotent: re-running matches nothing once healed. (zombie-calls)
UPDATE realtime_events re
SET replayable = false
WHERE re.type = 'call.started'
  AND re.replayable = true
  AND NOT EXISTS (
    SELECT 1
    FROM calls c
    WHERE c.id::text = (re.body #>> '{payload,call,id}')
      AND c.status <> 'ended'
  );

-- Keep the call-end tombstone lookup cheap: the end CTE matches a single
-- call.started row by the call id embedded in the event body, scoped to the
-- small set of still-replayable call.started events. A partial expression index
-- makes that an index probe instead of a scan as realtime_events grows.
-- Built non-CONCURRENTLY on purpose: realtime_events is a drained outbox
-- (verified ~2.5k rows / ~4.5 MB in prod) and the partial index covers only
-- still-replayable call.started rows, so the build and its brief write lock are
-- sub-second. The migration runner applies files in autocommit (no wrapping
-- transaction), so CONCURRENTLY would be possible, but it is unnecessary at this
-- table size and risks leaving an INVALID index on interruption.
CREATE INDEX IF NOT EXISTS idx_realtime_events_call_started_replayable
    ON realtime_events ((body #>> '{payload,call,id}'))
    WHERE type = 'call.started' AND replayable = true;
