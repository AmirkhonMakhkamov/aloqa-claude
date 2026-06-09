-- Reverse the tombstone-lookup index. The data heal (replayable = false on
-- call.started events of ended calls) is intentionally NOT reversed: re-enabling
-- replay of ended-call start events would reintroduce the zombie-call surfaces.
-- (zombie-calls)
DROP INDEX IF EXISTS idx_realtime_events_call_started_replayable;
