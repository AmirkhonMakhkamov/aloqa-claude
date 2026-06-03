package call

import (
	"context"
	"log/slog"
	"time"
)

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
