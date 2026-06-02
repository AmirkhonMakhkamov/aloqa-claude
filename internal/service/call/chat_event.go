package call

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/platform/txscope"
)

// callEventPayload is the structured envelope persisted in messages.call_event
// on the type='system' message written into a call's channel/DM timeline when
// the call ends. The FE mirrors this schema (see packages/core domain/message).
type callEventPayload struct {
	CallID             uuid.UUID   `json:"call_id"`
	CallType           string      `json:"call_type"`
	EndReason          string      `json:"end_reason"`
	StartedAt          *time.Time  `json:"started_at"`
	EndedAt            *time.Time  `json:"ended_at"`
	DurationSeconds    int64       `json:"duration_seconds"`
	InitiatorID        uuid.UUID   `json:"initiator_id"`
	ParticipantUserIDs []uuid.UUID `json:"participant_user_ids"`
	ParticipantCount   int         `json:"participant_count"`
	HasRecording       bool        `json:"has_recording"`
}

// emitCallEndedChatMessage writes a type='system' call-event message into the
// call's channel/DM timeline and broadcasts it via the existing message.created
// path, so it appears in the chat history (Telegram/WhatsApp-style record).
//
// Best-effort and isolated from the call-end transaction: it runs AFTER the call
// has already transitioned to ended, in its own transaction (so the durable
// outbox is used in production), and never returns an error to the caller — a
// failure here must not undo the call ending. Skips calls with no channel
// (standalone group calls) and the selector placeholder type.
func (s *Service) emitCallEndedChatMessage(ctx context.Context, call *entity.Call) {
	if call == nil || call.ChannelID == nil || call.Type == entity.CallTypeSelector {
		return
	}

	participants, err := s.calls.ListParticipants(ctx, call.ID)
	if err != nil {
		slog.WarnContext(ctx, "call-ended chat message: failed to list participants",
			"call_id", call.ID, "error", err)
		participants = nil
	}

	raw, err := json.Marshal(buildCallEventPayload(call, participants))
	if err != nil {
		slog.ErrorContext(ctx, "call-ended chat message: failed to marshal call_event",
			"call_id", call.ID, "error", err)
		return
	}

	now := time.Now().UTC()
	channelID := *call.ChannelID
	msg := &entity.Message{
		ID:        id.New(),
		ChannelID: channelID,
		UserID:    call.CreatedBy,
		Content:   callEndedFallbackContent(call.EndReason),
		Type:      entity.MessageTypeSystem,
		CallEvent: raw,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// ChannelType in the payload is only meaningful for self-channels (saved
	// messages), which calls never target, so nil is correct here.
	payload := event.NewMessagePayload(msg, nil)

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Messages() == nil {
				return cerrors.Unavailable("message transaction scope is not configured")
			}
			if err := scope.Messages().Create(ctx, msg); err != nil {
				return err
			}
			if err := s.enqueueRealtimeTx(ctx, scope, event.TypeMessageCreated,
				fmt.Sprintf("aloqa.chat.%s", channelID), call.WorkspaceID, channelID, call.CreatedBy, payload); err != nil {
				return err
			}
			return s.enqueueRealtimeTx(ctx, scope, event.TypeMessageCreated,
				fmt.Sprintf("aloqa.ws.%s", call.WorkspaceID), call.WorkspaceID, channelID, call.CreatedBy, payload)
		}); err != nil {
			slog.ErrorContext(ctx, "failed to persist call-ended chat message",
				"call_id", call.ID, "channel_id", channelID, "error", err)
		}
		return
	}

	// Non-transactional fallback (s.tx unset — used in unit tests and any
	// deployment without a transaction manager).
	if s.messages == nil {
		return
	}
	if err := s.messages.Create(ctx, msg); err != nil {
		slog.ErrorContext(ctx, "failed to persist call-ended chat message",
			"call_id", call.ID, "channel_id", channelID, "error", err)
		return
	}
	s.doPublish(ctx, event.TypeMessageCreated,
		fmt.Sprintf("aloqa.chat.%s", channelID), call.WorkspaceID, channelID, call.CreatedBy, payload)
	s.doPublish(ctx, event.TypeMessageCreated,
		fmt.Sprintf("aloqa.ws.%s", call.WorkspaceID), call.WorkspaceID, channelID, call.CreatedBy, payload)
}

// buildCallEventPayload derives the persisted call-event envelope from the ended
// call and its participants. Participants are those that actually joined
// (JoinedAt != nil), deduplicated by user; missed/cancelled calls naturally
// yield an empty set and zero duration.
func buildCallEventPayload(call *entity.Call, participants []entity.CallParticipant) callEventPayload {
	userIDs, count := joinedParticipants(participants)

	var duration int64
	if call.StartedAt != nil && call.EndedAt != nil {
		if d := call.EndedAt.Sub(*call.StartedAt); d > 0 {
			duration = int64(d.Seconds())
		}
	}

	return callEventPayload{
		CallID:             call.ID,
		CallType:           string(call.Type),
		EndReason:          string(call.EndReason),
		StartedAt:          call.StartedAt,
		EndedAt:            call.EndedAt,
		DurationSeconds:    duration,
		InitiatorID:        call.CreatedBy,
		ParticipantUserIDs: userIDs,
		ParticipantCount:   count,
		HasRecording:       false,
	}
}

func joinedParticipants(participants []entity.CallParticipant) ([]uuid.UUID, int) {
	seen := make(map[uuid.UUID]struct{}, len(participants))
	ids := make([]uuid.UUID, 0, len(participants))
	for _, p := range participants {
		if p.JoinedAt == nil {
			continue
		}
		if _, ok := seen[p.UserID]; ok {
			continue
		}
		seen[p.UserID] = struct{}{}
		ids = append(ids, p.UserID)
	}
	return ids, len(ids)
}

// callEndedFallbackContent is a neutral English content string used by surfaces
// that do not understand call_event (channel-list last-message preview, push
// notifications). The chat timeline renders a rich localized row from call_event.
func callEndedFallbackContent(reason entity.CallEndReason) string {
	switch reason {
	case entity.CallEndReasonMissed, entity.CallEndReasonCancelled:
		return "Missed call"
	default:
		return "Call"
	}
}
