-- Heal "zombie call" participants: rows left as status='connected'/'joining'/
-- 'waiting' with left_at=NULL on calls that have already ended. Before this
-- release, the call-end paths (host_ended / all_left / cancelled / room_finished)
-- only flipped calls.status to 'ended' and never disconnected the participants
-- who were not the one explicitly leaving. Those rows make the user look "still
-- in" an ended call, which the active-call surfaces and the FE in-call surface
-- keep resurfacing (the call showing 400+ hours). The code fix in this release
-- (disconnectRemainingParticipants in internal/service/call/service.go) prevents
-- new ones; this backfill cleans the rows already in the database. Idempotent:
-- re-running matches nothing once healed. (zombie-calls)

UPDATE call_participants cp
SET status      = 'disconnected',
    left_at     = COALESCE(cp.left_at, c.ended_at, now()),
    left_reason = COALESCE(cp.left_reason, 'timeout')
FROM calls c
WHERE c.id = cp.call_id
  AND c.status = 'ended'
  AND cp.status IN ('joining', 'connected', 'waiting')
  AND cp.left_at IS NULL;
