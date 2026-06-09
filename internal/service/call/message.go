package call

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/pkg/pagination"
	"aloqa/internal/platform/txscope"
)

const callMessageMaxBody = 2000

func (s *Service) SendCallMessage(ctx context.Context, workspaceID, callID, senderID uuid.UUID, body string) (*entity.CallMessage, error) {
	if err := validateCallMessageBody(body); err != nil {
		return nil, err
	}

	call, err := s.requireCallAccess(ctx, workspaceID, callID, senderID)
	if err != nil {
		return nil, err
	}

	participant, err := s.calls.GetParticipant(ctx, callID, senderID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.Forbidden("not in call")
		}
		slog.ErrorContext(ctx, "failed to get call participant for message send", "call_id", callID, "user_id", senderID, "error", err)
		return nil, cerrors.Internal("failed to verify participant", err)
	}
	if participant.Status != entity.ParticipantStatusConnected {
		return nil, cerrors.Forbidden("not in call")
	}
	// Chat policy (ALK-812): when chat is disabled, members cannot SEND, but the
	// host/co-host still can (they own the meeting). Reading history is never
	// gated here — only the send path. The FE renders a read-only panel for members.
	if !call.Settings.Chat && participant.Role != entity.CallRoleHost && participant.Role != entity.CallRoleCoHost {
		return nil, cerrors.Forbidden("call chat is disabled")
	}
	if call.Status == entity.CallStatusEnded {
		return nil, cerrors.Forbidden("call is not active")
	}
	if s.callMessages == nil {
		return nil, cerrors.Unavailable("call messaging is not configured")
	}

	msg := &entity.CallMessage{
		ID:        id.New(),
		CallID:    callID,
		SenderID:  senderID,
		Body:      body,
		CreatedAt: time.Now().UTC(),
	}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.CallMessages() == nil {
				return cerrors.Unavailable("call message transaction scope is not configured")
			}
			if err := scope.CallMessages().Create(ctx, msg); err != nil {
				return err
			}
			return s.enqueueCallMessageEventTx(ctx, scope, event.TypeCallMessageCreated, call, msg)
		}); err != nil {
			slog.ErrorContext(ctx, "failed to create call message transaction", "call_id", callID, "user_id", senderID, "error", err)
			return nil, cerrors.Internal("failed to create call message", err)
		}
	} else {
		if err := s.callMessages.Create(ctx, msg); err != nil {
			slog.ErrorContext(ctx, "failed to create call message", "call_id", callID, "user_id", senderID, "error", err)
			return nil, cerrors.Internal("failed to create call message", err)
		}
		s.publishCallMessageEvent(ctx, event.TypeCallMessageCreated, call, msg)
	}

	slog.InfoContext(ctx, "call message sent", "message_id", msg.ID, "call_id", callID, "user_id", senderID)
	return msg, nil
}

func (s *Service) ListCallMessages(ctx context.Context, workspaceID, callID, requesterID uuid.UUID, params pagination.Params) ([]entity.CallMessage, error) {
	if _, err := s.requireCallAccess(ctx, workspaceID, callID, requesterID); err != nil {
		return nil, err
	}
	if s.callMessages == nil {
		return nil, cerrors.Unavailable("call messaging is not configured")
	}

	items, err := s.callMessages.ListByCall(ctx, callID, params)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list call messages", "call_id", callID, "user_id", requesterID, "error", err)
		return nil, cerrors.Internal("failed to list call messages", err)
	}
	return items, nil
}

func (s *Service) DeleteCallMessage(ctx context.Context, workspaceID, callID, requesterID, messageID uuid.UUID) error {
	call, err := s.requireCallAccess(ctx, workspaceID, callID, requesterID)
	if err != nil {
		return err
	}
	if s.callMessages == nil {
		return cerrors.Unavailable("call messaging is not configured")
	}

	msg, err := s.callMessages.GetByID(ctx, messageID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("call message not found")
		}
		slog.ErrorContext(ctx, "failed to get call message", "message_id", messageID, "call_id", callID, "error", err)
		return cerrors.Internal("failed to get call message", err)
	}
	if msg.CallID != callID {
		return cerrors.NotFound("call message not found")
	}
	if msg.SenderID != requesterID {
		if err := s.requireHostOrCoHost(ctx, callID, requesterID); err != nil {
			return err
		}
	}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.CallMessages() == nil {
				return cerrors.Unavailable("call message transaction scope is not configured")
			}
			if err := scope.CallMessages().SoftDelete(ctx, messageID, callID); err != nil {
				return err
			}
			return s.enqueueCallMessageEventTx(ctx, scope, event.TypeCallMessageDeleted, call, msg)
		}); err != nil {
			slog.ErrorContext(ctx, "failed to delete call message transaction", "message_id", messageID, "call_id", callID, "error", err)
			return cerrors.Internal("failed to delete call message", err)
		}
	} else {
		if err := s.callMessages.SoftDelete(ctx, messageID, callID); err != nil {
			slog.ErrorContext(ctx, "failed to delete call message", "message_id", messageID, "call_id", callID, "error", err)
			return cerrors.Internal("failed to delete call message", err)
		}
		s.publishCallMessageEvent(ctx, event.TypeCallMessageDeleted, call, msg)
	}

	slog.InfoContext(ctx, "call message deleted", "message_id", messageID, "call_id", callID, "user_id", requesterID)
	return nil
}

func validateCallMessageBody(body string) error {
	if !utf8.ValidString(body) {
		return cerrors.InvalidInput("body must be valid UTF-8")
	}
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return cerrors.InvalidInput("body is required")
	}
	if utf8.RuneCountInString(trimmed) > callMessageMaxBody {
		return cerrors.InvalidInput("body too long")
	}
	return nil
}
