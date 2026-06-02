package call

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/platform/txscope"
)

const (
	defaultStaleCallCleanupInterval = 30 * time.Second
	defaultEmptyCallGrace           = 5 * time.Minute
	defaultStaleCallCleanupLimit    = 100
)

type staleCallRepository interface {
	ListStaleOpen(ctx context.Context, before time.Time, limit int) ([]entity.Call, error)
}

func normalizeStaleCallCleanupInputs(interval, grace time.Duration, limit int) (time.Duration, time.Duration, int) {
	if interval <= 0 {
		interval = defaultStaleCallCleanupInterval
	}
	if grace <= 0 {
		grace = defaultEmptyCallGrace
	}
	if limit <= 0 {
		limit = defaultStaleCallCleanupLimit
	}
	return interval, grace, limit
}

func (s *Service) RunStaleCallCleanupWorker(ctx context.Context, interval, grace time.Duration, limit int) {
	interval, grace, limit = normalizeStaleCallCleanupInputs(interval, grace, limit)

	s.runStaleCallCleanupOnce(ctx, grace, limit)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runStaleCallCleanupOnce(ctx, grace, limit)
		}
	}
}

func (s *Service) runStaleCallCleanupOnce(ctx context.Context, grace time.Duration, limit int) {
	ended, err := s.CleanupStaleOpenCalls(ctx, grace, limit)
	if err != nil {
		slog.WarnContext(ctx, "stale call cleanup failed", "error", err)
		return
	}
	if ended > 0 {
		slog.InfoContext(ctx, "stale call cleanup ended calls", "count", ended)
	}
}

func (s *Service) CleanupStaleOpenCalls(ctx context.Context, grace time.Duration, limit int) (int, error) {
	_, grace, limit = normalizeStaleCallCleanupInputs(0, grace, limit)

	repo, ok := s.calls.(staleCallRepository)
	if !ok {
		return 0, nil
	}
	if !s.livekit.IsConfigured() {
		return 0, nil
	}

	calls, err := repo.ListStaleOpen(ctx, time.Now().UTC().Add(-grace), limit)
	if err != nil {
		return 0, cerrors.Internal("failed to list stale open calls", err)
	}

	ended := 0
	for i := range calls {
		call := calls[i]
		hasMediaParticipants, err := s.hasActiveMediaParticipants(ctx, call)
		if err != nil {
			slog.WarnContext(ctx, "skipping stale call cleanup after media inspection error", "call_id", call.ID, "error", err)
			continue
		}
		if hasMediaParticipants {
			continue
		}

		didEnd, err := s.endStaleOpenCall(ctx, &call)
		if err != nil {
			slog.WarnContext(ctx, "failed to end stale open call", "call_id", call.ID, "error", err)
			continue
		}
		if didEnd {
			ended++
		}
	}

	return ended, nil
}

func (s *Service) endStaleOpenCall(ctx context.Context, staleCall *entity.Call) (bool, error) {
	if staleCall == nil {
		return false, nil
	}
	if s.tx != nil {
		return s.endStaleOpenCallTx(ctx, staleCall.ID)
	}
	return s.endStaleOpenCallDirect(ctx, staleCall.ID)
}

func (s *Service) endStaleOpenCallTx(ctx context.Context, callID uuid.UUID) (bool, error) {
	ended := false
	var endedCall *entity.Call

	err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
		repo := scope.Calls()
		if repo == nil {
			return cerrors.Unavailable("call transaction scope is not configured")
		}

		call, err := repo.GetByID(ctx, callID)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}

		callReason, participantReason, ok := staleCleanupReasons(call.Status)
		if !ok {
			return nil
		}

		didEnd, err := endStaleCallStatus(ctx, repo, call.ID, call.Status, callReason)
		if err != nil {
			return err
		}
		if !didEnd {
			return nil
		}

		participants, err := repo.ListParticipants(ctx, call.ID)
		if err != nil {
			return err
		}
		if err := disconnectStaleParticipants(ctx, repo, participants, participantReason); err != nil {
			return err
		}

		markCallEnded(call, callReason)
		if err := s.enqueueCallEventTx(ctx, scope, event.TypeCallEnded, call, call.CreatedBy); err != nil {
			return err
		}
		ended = true
		endedCall = call
		return nil
	})
	if err != nil || !ended {
		return false, err
	}

	// Record the call in its channel/DM chat history (best-effort).
	s.emitCallEndedChatMessage(ctx, endedCall)
	s.cleanupEndedMediaRooms(ctx, endedCall.ID)
	return true, nil
}

func (s *Service) endStaleOpenCallDirect(ctx context.Context, callID uuid.UUID) (bool, error) {
	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}

	callReason, participantReason, ok := staleCleanupReasons(call.Status)
	if !ok {
		return false, nil
	}

	didEnd, err := endStaleCallStatus(ctx, s.calls, call.ID, call.Status, callReason)
	if err != nil {
		return false, err
	}
	if !didEnd {
		return false, nil
	}

	participants, err := s.calls.ListParticipants(ctx, call.ID)
	if err != nil {
		return false, err
	}
	if err := disconnectStaleParticipants(ctx, s.calls, participants, participantReason); err != nil {
		return false, err
	}

	markCallEnded(call, callReason)
	s.publishCallEvent(ctx, event.TypeCallEnded, call, call.CreatedBy)
	// Record the call in its channel/DM chat history (best-effort).
	s.emitCallEndedChatMessage(ctx, call)
	s.cleanupEndedMediaRooms(ctx, call.ID)
	return true, nil
}

func staleCleanupReasons(status entity.CallStatus) (entity.CallEndReason, entity.ParticipantLeftReason, bool) {
	switch status {
	case entity.CallStatusRinging:
		return entity.CallEndReasonMissed, entity.ParticipantLeftReasonMissed, true
	case entity.CallStatusActive:
		return entity.CallEndReasonAllLeft, entity.ParticipantLeftReasonTimeout, true
	default:
		return "", "", false
	}
}

func endStaleCallStatus(
	ctx context.Context,
	repo repository.CallRepository,
	callID uuid.UUID,
	status entity.CallStatus,
	reason entity.CallEndReason,
) (bool, error) {
	switch status {
	case entity.CallStatusRinging:
		return cancelRingingWithReason(ctx, repo, callID, reason)
	case entity.CallStatusActive:
		return endCallWithReasonIfNotEnded(ctx, repo, callID, reason)
	default:
		return false, nil
	}
}

func disconnectStaleParticipants(
	ctx context.Context,
	repo repository.CallRepository,
	participants []entity.CallParticipant,
	reason entity.ParticipantLeftReason,
) error {
	for i := range participants {
		participant := participants[i]
		if participant.Status == entity.ParticipantStatusDisconnected {
			continue
		}
		if _, err := disconnectParticipantIfConnected(ctx, repo, participant.ID, reason); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) hasActiveMediaParticipants(ctx context.Context, call entity.Call) (bool, error) {
	if !s.livekit.IsConfigured() {
		return true, nil
	}
	if s.livekitRooms == nil {
		return true, cerrors.Unavailable("livekit room service is not configured")
	}
	participants, err := s.livekitRooms.ListParticipants(ctx, call.ID)
	if err != nil {
		return true, err
	}
	if call.Status == entity.CallStatusRinging {
		for i := range participants {
			if participants[i].GetIdentity() != call.CreatedBy.String() {
				return true, nil
			}
		}
		return false, nil
	}
	return len(participants) > 0, nil
}

func (s *Service) cleanupEndedMediaRooms(ctx context.Context, callID uuid.UUID) {
	s.closeAllBreakoutSFURooms(ctx, callID)
	s.deleteLiveKitRoomBestEffort(ctx, callID)
	if s.sfu != nil {
		s.sfu.CloseRoom(callID.String())
	}
}
