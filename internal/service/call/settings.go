package call

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/pkg/cerrors"
)

// CallSettingsPatch describes a partial call settings update. Pointer fields
// distinguish "absent" from an explicit false value.
type CallSettingsPatch struct {
	BreakoutRooms *bool
}

// UpdateCallSettings applies host-controlled partial settings updates to a
// live call and broadcasts the resulting settings to connected clients.
func (s *Service) UpdateCallSettings(ctx context.Context, callID, actorID uuid.UUID, patch CallSettingsPatch) (*entity.Call, error) {
	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		return nil, s.wrapCallError(ctx, err, callID, "update call settings")
	}

	if err := s.requireHostOrCoHost(ctx, callID, actorID); err != nil {
		return nil, err
	}

	if call.Status != entity.CallStatusActive {
		return nil, cerrors.Forbidden("call is not active")
	}

	if patch.BreakoutRooms == nil {
		return call, nil
	}

	settings := call.Settings
	if !*patch.BreakoutRooms {
		rooms, err := s.breakoutRooms.ListByCall(ctx, callID)
		if err != nil {
			slog.ErrorContext(ctx, "failed to list breakout rooms for settings update", "call_id", callID, "error", err)
			return nil, cerrors.Internal("failed to list breakout rooms", err)
		}
		if hasActiveBreakoutRoom(rooms) {
			if err := s.CloseAllBreakoutRooms(ctx, callID, actorID); err != nil {
				return nil, err
			}
		}
	}
	settings.BreakoutRooms = *patch.BreakoutRooms

	if err := s.calls.UpdateSettings(ctx, callID, settings); err != nil {
		return nil, s.wrapCallError(ctx, err, callID, "update call settings")
	}
	call.Settings = settings
	s.publishCallSettingsChanged(ctx, call)

	return call, nil
}

func (s *Service) publishCallSettingsChanged(ctx context.Context, call *entity.Call) {
	channelID := uuid.Nil
	if call.ChannelID != nil {
		channelID = *call.ChannelID
	}

	subject := fmt.Sprintf("aloqa.ws.%s", call.WorkspaceID)
	s.doPublish(ctx, event.TypeCallSettingsChanged, subject, call.WorkspaceID, channelID, call.CreatedBy, event.CallSettingsChangedPayload{
		CallID:   call.ID,
		Settings: call.Settings,
	})
}
