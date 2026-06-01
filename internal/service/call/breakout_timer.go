package call

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// Breakout auto-close timers.
//
// When a host creates breakout rooms with a non-zero time limit, we schedule a
// best-effort in-memory timer that closes all breakout rooms for the call when
// the limit elapses. Timers are stored on Service.breakoutTimers, keyed by the
// call UUID.
//
// LIMITATION: these timers live only in process memory and do NOT survive a
// server restart or fail over to another instance. A durable scheduler (e.g. a
// DB-backed job or a leased timer in a coordination service) is a follow-up.

// scheduleBreakoutAutoClose arms (or re-arms) the auto-close timer for a call.
// maxSeconds <= 0 means "no time limit" and any existing timer is cancelled.
// hostUserID is the call host used as the actor when the timer fires, so the
// host-or-co-host authorization in CloseAllBreakoutRooms is satisfied.
func (s *Service) scheduleBreakoutAutoClose(callID, hostUserID uuid.UUID, maxSeconds int) {
	// Replace any previously scheduled timer for this call.
	s.cancelBreakoutAutoClose(callID)
	if maxSeconds <= 0 {
		return
	}

	timer := time.AfterFunc(time.Duration(maxSeconds)*time.Second, func() {
		// Drop the entry first so a concurrent CloseAll can't deadlock on the
		// stored timer (CloseAllBreakoutRooms also calls cancel, which is a
		// no-op once removed). CloseAll is idempotent when no rooms remain.
		s.breakoutTimers.Delete(callID)
		if err := s.CloseAllBreakoutRooms(context.Background(), callID, hostUserID); err != nil {
			slog.Warn("breakout auto-close timer failed", "call_id", callID, "error", err)
		} else {
			slog.Info("breakout rooms auto-closed by timer", "call_id", callID)
		}
	})
	s.breakoutTimers.Store(callID, timer)
}

// cancelBreakoutAutoClose stops and removes any pending auto-close timer for a
// call. Safe to call when no timer is scheduled.
func (s *Service) cancelBreakoutAutoClose(callID uuid.UUID) {
	if v, ok := s.breakoutTimers.LoadAndDelete(callID); ok {
		if timer, ok := v.(*time.Timer); ok {
			timer.Stop()
		}
	}
}

// breakoutRoomDeleteGrace is how long we wait after a breakout room is closed
// before deleting its LiveKit room. LiveKit's DeleteRoom disconnects every
// connected participant with DISCONNECT_REASON_ROOM_DELETED, which the FE
// interprets as the whole call ending. On close we broadcast the close events
// synchronously so clients begin returning to the main room immediately; the
// grace gives that async (WS + REST) round-trip time to complete so the room is
// empty by the time it is deleted and no one is kicked.
const breakoutRoomDeleteGrace = 10 * time.Second

// scheduleBreakoutLiveKitDelete defers the deletion of a breakout room's
// LiveKit room by breakoutRoomDeleteGrace. The pending timer is tracked in
// breakoutDeleteTimers keyed by the LiveKit room name so it can be cancelled
// (e.g. when the room name is reused by a fresh CreateBreakoutRooms, or when the
// main call ends and we delete immediately). Safe no-op when LiveKit is not
// configured.
func (s *Service) scheduleBreakoutLiveKitDelete(name string) {
	if s.livekitRooms == nil {
		return
	}
	// Replace any previously scheduled delete for this room name.
	s.cancelBreakoutLiveKitDelete(name)

	timer := time.AfterFunc(breakoutRoomDeleteGrace, func() {
		// Drop the tracking entry first; a concurrent cancel becomes a no-op.
		s.breakoutDeleteTimers.Delete(name)
		ctx := context.Background()
		if err := s.livekitRooms.DeleteRoomByName(ctx, name); err != nil {
			slog.Warn("deferred livekit breakout room delete failed", "room_name", name, "error", err)
		} else {
			slog.Info("livekit breakout room deleted after grace", "room_name", name)
		}
	})
	s.breakoutDeleteTimers.Store(name, timer)
}

// cancelBreakoutLiveKitDelete stops and removes any pending deferred LiveKit
// delete for a breakout room name. Safe to call when none is scheduled.
func (s *Service) cancelBreakoutLiveKitDelete(name string) {
	if v, ok := s.breakoutDeleteTimers.LoadAndDelete(name); ok {
		if timer, ok := v.(*time.Timer); ok {
			timer.Stop()
		}
	}
}
