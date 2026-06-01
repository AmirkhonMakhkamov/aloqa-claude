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
