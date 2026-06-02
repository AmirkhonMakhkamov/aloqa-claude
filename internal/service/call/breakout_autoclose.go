package call

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/pkg/cerrors"
)

const (
	defaultBreakoutAutoCloseInterval = 30 * time.Second
	defaultBreakoutAutoCloseLimit    = 100
)

func normalizeBreakoutAutoCloseInputs(interval time.Duration, limit int) (time.Duration, int) {
	if interval <= 0 {
		interval = defaultBreakoutAutoCloseInterval
	}
	if limit <= 0 {
		limit = defaultBreakoutAutoCloseLimit
	}
	return interval, limit
}

func (s *Service) RunBreakoutAutoCloseWorker(ctx context.Context, interval time.Duration, limit int) {
	interval, limit = normalizeBreakoutAutoCloseInputs(interval, limit)

	s.runBreakoutAutoCloseOnce(ctx, limit)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runBreakoutAutoCloseOnce(ctx, limit)
		}
	}
}

func (s *Service) runBreakoutAutoCloseOnce(ctx context.Context, limit int) {
	closed, err := s.CloseExpiredBreakoutRooms(ctx, time.Now().UTC(), limit)
	if err != nil {
		slog.WarnContext(ctx, "breakout auto-close sweep failed", "error", err)
		return
	}
	if closed > 0 {
		slog.InfoContext(ctx, "breakout auto-close sweep closed calls", "count", closed)
	}
}

func (s *Service) CloseExpiredBreakoutRooms(ctx context.Context, before time.Time, limit int) (int, error) {
	_, limit = normalizeBreakoutAutoCloseInputs(0, limit)
	if s.breakoutRooms == nil || s.calls == nil {
		return 0, nil
	}

	callIDs, err := s.breakoutRooms.ListCallsWithExpiredActiveBreakouts(ctx, before.UTC(), limit)
	if err != nil {
		return 0, cerrors.Internal("failed to list expired breakout rooms", err)
	}

	closed := 0
	for _, callID := range callIDs {
		if s.closeExpiredBreakoutCall(ctx, callID) {
			closed++
		}
	}
	return closed, nil
}

func (s *Service) closeExpiredBreakoutCall(ctx context.Context, callID uuid.UUID) bool {
	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		if isNotFound(err) {
			return false
		}
		slog.WarnContext(ctx, "failed to load call for breakout auto-close", "call_id", callID, "error", err)
		return false
	}

	if err := s.CloseAllBreakoutRooms(ctx, callID, call.CreatedBy); err != nil {
		slog.WarnContext(ctx, "failed to auto-close breakout rooms", "call_id", callID, "error", err)
		return false
	}

	slog.InfoContext(ctx, "breakout rooms auto-closed by sweeper", "call_id", callID)
	return true
}
