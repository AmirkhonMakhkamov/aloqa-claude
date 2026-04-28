package call

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/media/sfu"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
)

// CreateBreakoutRoomInput describes a single breakout room to create.
type CreateBreakoutRoomInput struct {
	Name      string `json:"name"`
	TimeLimit *int   `json:"time_limit,omitempty"` // seconds
}

// CreateBreakoutRooms creates one or more breakout rooms within an active call.
// Only the host or co-host may create breakout rooms, and the call must have
// breakout rooms enabled in its settings.
func (s *Service) CreateBreakoutRooms(
	ctx context.Context,
	callID, userID uuid.UUID,
	inputs []CreateBreakoutRoomInput,
) ([]entity.BreakoutRoom, error) {
	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		return nil, s.wrapCallError(ctx, err, callID, "create breakout rooms")
	}

	if call.Status != entity.CallStatusActive {
		return nil, cerrors.Forbidden("breakout rooms can only be created in an active call")
	}

	if !call.Settings.BreakoutRooms {
		return nil, cerrors.Forbidden("breakout rooms are not enabled for this call")
	}

	if err := s.requireHostOrCoHost(ctx, callID, userID); err != nil {
		return nil, err
	}

	if len(inputs) == 0 {
		return nil, cerrors.InvalidInput("at least one breakout room is required")
	}

	now := time.Now()
	rooms := make([]entity.BreakoutRoom, 0, len(inputs))

	for _, input := range inputs {
		if input.Name == "" {
			return nil, cerrors.InvalidInput("breakout room name is required")
		}

		room := entity.BreakoutRoom{
			ID:        id.New(),
			CallID:    callID,
			Name:      input.Name,
			CreatedBy: userID,
			TimeLimit: input.TimeLimit,
			Status:    entity.BreakoutRoomStatusActive,
			CreatedAt: now,
		}

		if err := s.breakoutRooms.Create(ctx, &room); err != nil {
			slog.ErrorContext(ctx, "failed to create breakout room", "call_id", callID, "name", input.Name, "error", err)
			return nil, cerrors.Internal("failed to create breakout room", err)
		}

		// Create a dedicated SFU room for this breakout room.
		sfuRoomID := breakoutSFURoomID(callID, room.ID)
		if _, err := s.sfu.CreateRoom(sfuRoomID, sfu.RoomOptions{
			MaxPresenters: call.Settings.MaxParticipants,
		}); err != nil {
			slog.ErrorContext(ctx, "failed to create SFU room for breakout", "breakout_id", room.ID, "error", err)
		}

		rooms = append(rooms, room)

		s.publishBreakoutEvent(ctx, event.TypeBreakoutRoomCreated, call, event.BreakoutRoomPayload{
			CallID: callID,
			Room:   &room,
		})
	}

	slog.InfoContext(ctx, "breakout rooms created", "call_id", callID, "count", len(rooms), "user_id", userID)
	return rooms, nil
}

// ListBreakoutRooms returns all breakout rooms for a call.
func (s *Service) ListBreakoutRooms(ctx context.Context, callID uuid.UUID) ([]entity.BreakoutRoom, error) {
	if _, err := s.calls.GetByID(ctx, callID); err != nil {
		return nil, s.wrapCallError(ctx, err, callID, "list breakout rooms")
	}

	rooms, err := s.breakoutRooms.ListByCall(ctx, callID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list breakout rooms", "call_id", callID, "error", err)
		return nil, cerrors.Internal("failed to list breakout rooms", err)
	}

	return rooms, nil
}

// JoinBreakoutRoom moves a participant from the main room (or another
// breakout room) into the specified breakout room. Each breakout room has
// its own SFU room so the participant's media is isolated.
func (s *Service) JoinBreakoutRoom(
	ctx context.Context,
	callID, userID, breakoutRoomID uuid.UUID,
) error {
	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		return s.wrapCallError(ctx, err, callID, "join breakout room")
	}

	if call.Status != entity.CallStatusActive {
		return cerrors.Forbidden("call is not active")
	}

	// Verify the breakout room exists and is active.
	room, err := s.breakoutRooms.GetByID(ctx, breakoutRoomID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("breakout room not found")
		}
		return cerrors.Internal("failed to get breakout room", err)
	}
	if room.CallID != callID {
		return cerrors.Forbidden("breakout room does not belong to this call")
	}
	if room.Status != entity.BreakoutRoomStatusActive {
		return cerrors.Forbidden("breakout room is closed")
	}

	// Verify participant is connected to the call.
	participant, err := s.calls.GetParticipant(ctx, callID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.Forbidden("user is not a participant in this call")
		}
		return cerrors.Internal("failed to get participant", err)
	}
	if participant.Status != entity.ParticipantStatusConnected {
		return cerrors.Forbidden("participant is not connected")
	}

	// If already in a breakout room, remove from that SFU room first.
	if participant.BreakoutRoomID != nil {
		oldSFURoomID := breakoutSFURoomID(callID, *participant.BreakoutRoomID)
		if sfuRoom, ok := s.sfu.GetRoom(oldSFURoomID); ok {
			sfuRoom.RemovePeer(userID.String())
		}
	} else {
		// Leaving the main SFU room.
		if sfuRoom, ok := s.sfu.GetRoom(callID.String()); ok {
			sfuRoom.RemovePeer(userID.String())
		}
	}

	// Assign participant to breakout room in DB.
	if err := s.breakoutRooms.AssignParticipant(ctx, callID, userID, breakoutRoomID); err != nil {
		slog.ErrorContext(ctx, "failed to assign participant to breakout room",
			"call_id", callID, "user_id", userID, "breakout_room_id", breakoutRoomID, "error", err)
		return cerrors.Internal("failed to join breakout room", err)
	}

	s.publishBreakoutEvent(ctx, event.TypeBreakoutParticipantMoved, call, event.BreakoutParticipantMovedPayload{
		CallID:         callID,
		UserID:         userID,
		BreakoutRoomID: &breakoutRoomID,
	})

	slog.InfoContext(ctx, "participant joined breakout room",
		"call_id", callID, "user_id", userID, "breakout_room_id", breakoutRoomID)
	return nil
}

// ReturnToMainRoom moves a participant from their current breakout room back
// to the main call room.
func (s *Service) ReturnToMainRoom(ctx context.Context, callID, userID uuid.UUID) error {
	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		return s.wrapCallError(ctx, err, callID, "return to main room")
	}

	participant, err := s.calls.GetParticipant(ctx, callID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.Forbidden("user is not a participant in this call")
		}
		return cerrors.Internal("failed to get participant", err)
	}

	if participant.BreakoutRoomID == nil {
		return cerrors.Conflict("participant is already in the main room")
	}

	// Remove from breakout SFU room.
	oldSFURoomID := breakoutSFURoomID(callID, *participant.BreakoutRoomID)
	if sfuRoom, ok := s.sfu.GetRoom(oldSFURoomID); ok {
		sfuRoom.RemovePeer(userID.String())
	}

	// Clear breakout_room_id in DB.
	if err := s.breakoutRooms.UnassignParticipant(ctx, callID, userID); err != nil {
		slog.ErrorContext(ctx, "failed to unassign participant from breakout room",
			"call_id", callID, "user_id", userID, "error", err)
		return cerrors.Internal("failed to return to main room", err)
	}

	s.publishBreakoutEvent(ctx, event.TypeBreakoutParticipantMoved, call, event.BreakoutParticipantMovedPayload{
		CallID:         callID,
		UserID:         userID,
		BreakoutRoomID: nil,
	})

	slog.InfoContext(ctx, "participant returned to main room", "call_id", callID, "user_id", userID)
	return nil
}

// CloseBreakoutRoom closes a specific breakout room. All participants in the
// room are moved back to the main call. Only host or co-host can close rooms.
func (s *Service) CloseBreakoutRoom(ctx context.Context, callID, userID, breakoutRoomID uuid.UUID) error {
	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		return s.wrapCallError(ctx, err, callID, "close breakout room")
	}

	if err := s.requireHostOrCoHost(ctx, callID, userID); err != nil {
		return err
	}

	room, err := s.breakoutRooms.GetByID(ctx, breakoutRoomID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("breakout room not found")
		}
		return cerrors.Internal("failed to get breakout room", err)
	}
	if room.CallID != callID {
		return cerrors.Forbidden("breakout room does not belong to this call")
	}
	if room.Status != entity.BreakoutRoomStatusActive {
		return cerrors.Conflict("breakout room is already closed")
	}

	// Move all participants back to main room.
	if err := s.breakoutRooms.UnassignAllByRoom(ctx, breakoutRoomID); err != nil {
		slog.ErrorContext(ctx, "failed to unassign participants from breakout room",
			"breakout_room_id", breakoutRoomID, "error", err)
	}

	// Close the breakout SFU room.
	sfuRoomID := breakoutSFURoomID(callID, breakoutRoomID)
	s.sfu.CloseRoom(sfuRoomID)

	// Close the DB record.
	if err := s.breakoutRooms.Close(ctx, breakoutRoomID); err != nil {
		slog.ErrorContext(ctx, "failed to close breakout room", "breakout_room_id", breakoutRoomID, "error", err)
		return cerrors.Internal("failed to close breakout room", err)
	}

	s.publishBreakoutEvent(ctx, event.TypeBreakoutRoomClosed, call, event.BreakoutRoomPayload{
		CallID: callID,
		Room:   room,
	})

	slog.InfoContext(ctx, "breakout room closed", "call_id", callID, "breakout_room_id", breakoutRoomID, "user_id", userID)
	return nil
}

// CloseAllBreakoutRooms closes every active breakout room in the call and
// returns all participants to the main room. Only host or co-host.
func (s *Service) CloseAllBreakoutRooms(ctx context.Context, callID, userID uuid.UUID) error {
	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		return s.wrapCallError(ctx, err, callID, "close all breakout rooms")
	}

	if err := s.requireHostOrCoHost(ctx, callID, userID); err != nil {
		return err
	}

	rooms, err := s.breakoutRooms.ListByCall(ctx, callID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list breakout rooms for close-all", "call_id", callID, "error", err)
		return cerrors.Internal("failed to list breakout rooms", err)
	}

	// Close each SFU breakout room and unassign participants.
	for _, room := range rooms {
		if room.Status != entity.BreakoutRoomStatusActive {
			continue
		}
		if err := s.breakoutRooms.UnassignAllByRoom(ctx, room.ID); err != nil {
			slog.ErrorContext(ctx, "failed to unassign participants", "breakout_room_id", room.ID, "error", err)
		}
		sfuRoomID := breakoutSFURoomID(callID, room.ID)
		s.sfu.CloseRoom(sfuRoomID)
	}

	// Bulk-close all breakout rooms in DB.
	if err := s.breakoutRooms.CloseAllByCall(ctx, callID); err != nil {
		slog.ErrorContext(ctx, "failed to close all breakout rooms", "call_id", callID, "error", err)
		return cerrors.Internal("failed to close all breakout rooms", err)
	}

	s.publishBreakoutEvent(ctx, event.TypeBreakoutRoomsAllClosed, call, event.BreakoutRoomsAllClosedPayload{
		CallID: callID,
	})

	slog.InfoContext(ctx, "all breakout rooms closed", "call_id", callID, "user_id", userID)
	return nil
}

// BroadcastToBreakoutRooms sends a message from the host to all active
// breakout rooms. This is used for announcements like "5 minutes remaining".
func (s *Service) BroadcastToBreakoutRooms(
	ctx context.Context,
	callID, userID uuid.UUID,
	message string,
) error {
	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		return s.wrapCallError(ctx, err, callID, "broadcast to breakout rooms")
	}

	if err := s.requireHostOrCoHost(ctx, callID, userID); err != nil {
		return err
	}

	if message == "" {
		return cerrors.InvalidInput("message is required")
	}

	s.publishBreakoutEvent(ctx, event.TypeBreakoutBroadcast, call, event.BreakoutBroadcastPayload{
		CallID:  callID,
		UserID:  userID,
		Message: message,
	})

	slog.InfoContext(ctx, "broadcast sent to breakout rooms", "call_id", callID, "user_id", userID)
	return nil
}

// ListBreakoutRoomParticipants returns the connected participants in a
// specific breakout room.
func (s *Service) ListBreakoutRoomParticipants(
	ctx context.Context,
	callID, breakoutRoomID uuid.UUID,
) ([]entity.CallParticipant, error) {
	room, err := s.breakoutRooms.GetByID(ctx, breakoutRoomID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.NotFound("breakout room not found")
		}
		return nil, cerrors.Internal("failed to get breakout room", err)
	}
	if room.CallID != callID {
		return nil, cerrors.Forbidden("breakout room does not belong to this call")
	}

	participants, err := s.breakoutRooms.ListParticipants(ctx, breakoutRoomID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list breakout room participants",
			"breakout_room_id", breakoutRoomID, "error", err)
		return nil, cerrors.Internal("failed to list participants", err)
	}

	return participants, nil
}

// --- Helpers ---

// requireHostOrCoHost verifies that the user is host or co-host of the call.
func (s *Service) requireHostOrCoHost(ctx context.Context, callID, userID uuid.UUID) error {
	participant, err := s.calls.GetParticipant(ctx, callID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.Forbidden("user is not a participant in this call")
		}
		return cerrors.Internal("failed to verify participant role", err)
	}

	if participant.Role != entity.CallRoleHost && participant.Role != entity.CallRoleCoHost {
		return cerrors.Forbidden("only host or co-host can perform this action")
	}

	return nil
}

// wrapCallError maps common call lookup errors to the right AppError.
func (s *Service) wrapCallError(ctx context.Context, err error, callID uuid.UUID, action string) error {
	if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
		return cerrors.NotFound("call not found")
	}
	slog.ErrorContext(ctx, fmt.Sprintf("failed to get call for %s", action), "call_id", callID, "error", err)
	return cerrors.Internal("failed to get call", err)
}

// publishBreakoutEvent publishes a breakout room event to the workspace's WS subject.
func (s *Service) publishBreakoutEvent(ctx context.Context, evtType event.Type, call *entity.Call, payload any) {
	channelID := uuid.Nil
	if call.ChannelID != nil {
		channelID = *call.ChannelID
	}
	subject := fmt.Sprintf("aloqa.ws.%s", call.WorkspaceID)
	s.doPublish(ctx, evtType, subject, call.WorkspaceID, channelID, call.CreatedBy, payload)
}

// breakoutSFURoomID returns the SFU room ID for a breakout room.
// Format: {callID}:breakout:{breakoutRoomID}
func breakoutSFURoomID(callID, breakoutRoomID uuid.UUID) string {
	return fmt.Sprintf("%s:breakout:%s", callID, breakoutRoomID)
}
