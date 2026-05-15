package call

import (
	"context"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/pkg/cerrors"
)

const callReactionMaxBytes = 32

func (s *Service) RaiseHand(ctx context.Context, workspaceID, callID, userID uuid.UUID) error {
	call, err := s.requireActiveInCallParticipant(ctx, workspaceID, callID, userID)
	if err != nil {
		return err
	}
	return s.publishCallInteraction(ctx, call, event.TypeCallHandRaised, event.CallHandPayload{
		CallID: call.ID,
		UserID: userID,
	})
}

func (s *Service) LowerHand(ctx context.Context, workspaceID, callID, userID uuid.UUID) error {
	call, err := s.requireActiveInCallParticipant(ctx, workspaceID, callID, userID)
	if err != nil {
		return err
	}
	return s.publishCallInteraction(ctx, call, event.TypeCallHandLowered, event.CallHandPayload{
		CallID: call.ID,
		UserID: userID,
	})
}

func (s *Service) SendCallReaction(ctx context.Context, workspaceID, callID, userID uuid.UUID, emoji string) error {
	if emoji == "" || len(emoji) > callReactionMaxBytes || !utf8.ValidString(emoji) {
		return cerrors.InvalidInput("invalid emoji")
	}
	call, err := s.requireActiveInCallParticipant(ctx, workspaceID, callID, userID)
	if err != nil {
		return err
	}
	return s.publishCallInteraction(ctx, call, event.TypeCallReaction, event.CallReactionPayload{
		CallID: call.ID,
		UserID: userID,
		Emoji:  emoji,
	})
}

func (s *Service) requireActiveInCallParticipant(ctx context.Context, workspaceID, callID, userID uuid.UUID) (*entity.Call, error) {
	call, err := s.requireCallAccess(ctx, workspaceID, callID, userID)
	if err != nil {
		return nil, err
	}
	if call.Status != entity.CallStatusActive {
		return nil, cerrors.Forbidden("call is not active")
	}
	participant, err := s.calls.GetParticipant(ctx, callID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.Forbidden("not in call")
		}
		return nil, cerrors.Internal("failed to load participant", err)
	}
	if participant.Status != entity.ParticipantStatusConnected {
		return nil, cerrors.Forbidden("not in call")
	}
	return call, nil
}

func (s *Service) publishCallInteraction(
	ctx context.Context,
	call *entity.Call,
	evtType event.Type,
	payload any,
) error {
	participants, err := s.calls.ListParticipants(ctx, call.ID)
	if err != nil {
		return cerrors.Internal("failed to list participants", err)
	}
	for _, p := range participants {
		if p.Status != entity.ParticipantStatusConnected {
			continue
		}
		subject := fmt.Sprintf("aloqa.signal.%s", p.UserID)
		_, body, _, err := event.Prepare(subject, event.Event{
			Type:        evtType,
			WorkspaceID: call.WorkspaceID,
			UserID:      p.UserID,
			Timestamp:   time.Now(),
			Payload:     payload,
		})
		if err != nil {
			slog.ErrorContext(ctx, "failed to prepare call interaction", "type", evtType, "error", err)
			continue
		}
		if err := s.pubsub.Publish(ctx, subject, body); err != nil {
			slog.ErrorContext(ctx, "failed to publish call interaction", "type", evtType, "subject", subject, "error", err)
		}
	}
	return nil
}
