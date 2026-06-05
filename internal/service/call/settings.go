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
// distinguish "absent" from an explicit zero value. EntryMode is the single
// source of truth for the lobby; the legacy WaitingRoom flag is derived from it
// (S3 / ALK-821). Member-permission fields (chat, screen_sharing) and mid-call
// password rotation are intentionally NOT here — they are owned by S4 / S6.
type CallSettingsPatch struct {
	EntryMode        *entity.EntryMode
	MuteOnJoin       *bool
	BreakoutRooms    *bool
	BreakoutCreation *entity.BreakoutCreationPolicy
	MaxBreakoutRooms *int
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

	// No-op when the patch carries no fields — avoid a spurious settings write
	// and broadcast.
	if patch.EntryMode == nil && patch.MuteOnJoin == nil && patch.BreakoutRooms == nil && patch.BreakoutCreation == nil && patch.MaxBreakoutRooms == nil {
		return call, nil
	}

	settings := call.Settings

	if patch.MuteOnJoin != nil {
		settings.MuteOnJoin = *patch.MuteOnJoin
	}

	if patch.EntryMode != nil {
		mode := *patch.EntryMode
		if !mode.Valid() {
			return nil, cerrors.InvalidInput("invalid entry_mode")
		}
		// Switching to password (Join-by-PIN) mid-call is only allowed when the
		// call already has a configured join password — S3 never sets or rotates
		// it (deferred to S6). The existing hash is reused untouched.
		if mode == entity.EntryModePassword && call.JoinPasswordHash == "" {
			return nil, cerrors.InvalidInput("entry_mode=password requires a configured join password")
		}
		settings.EntryMode = mode
		// entry_mode is authoritative; keep the legacy waiting_room flag in sync
		// (mirrors StartCall normalisation) so a single source of truth drives the
		// lobby. Changing entry_mode is forward-only — it never retroactively
		// admits or rejects participants already in the waiting room.
		settings.WaitingRoom = mode == entity.EntryModeManualAdmit
	}

	if patch.BreakoutCreation != nil {
		policy := *patch.BreakoutCreation
		if !policy.Valid() {
			return nil, cerrors.InvalidInput("invalid breakout_creation")
		}
		settings.BreakoutCreation = policy
	}

	if patch.MaxBreakoutRooms != nil {
		maxRooms := *patch.MaxBreakoutRooms
		if maxRooms < 1 || maxRooms > 8 {
			return nil, cerrors.InvalidInput("max_breakout_rooms must be between 1 and 8")
		}
		settings.MaxBreakoutRooms = maxRooms
	}

	if patch.BreakoutRooms != nil {
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
	}

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
