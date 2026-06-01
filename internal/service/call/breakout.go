package call

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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
	// ParticipantUserIDs lets the host pre-assign connected call participants to
	// the room at creation time. Unknown / non-connected ids and the actor are
	// silently skipped.
	ParticipantUserIDs []uuid.UUID `json:"participant_user_ids,omitempty"`
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

	// Snapshot connected participants once so host pre-assignment can validate
	// the requested user ids without an N+1 lookup per id.
	connected, err := s.connectedParticipants(ctx, callID)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	rooms := make([]entity.BreakoutRoom, 0, len(inputs))
	maxTimeLimit := 0

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

		// Create a dedicated SFU room for this breakout room (legacy Pion plane).
		sfuRoomID := breakoutSFURoomID(callID, room.ID)
		if _, err := s.sfu.CreateRoom(sfuRoomID, sfu.RoomOptions{
			MaxPresenters: call.Settings.MaxParticipants,
		}); err != nil {
			slog.ErrorContext(ctx, "failed to create SFU room for breakout", "breakout_id", room.ID, "error", err)
		}

		// Provision the dedicated LiveKit room for this breakout (best-effort;
		// no-op when LiveKit is not configured).
		if err := s.EnsureLiveKitBreakoutRoom(ctx, call, room.ID); err != nil {
			slog.WarnContext(ctx, "failed to ensure livekit breakout room", "breakout_id", room.ID, "error", err)
		}

		rooms = append(rooms, room)

		s.publishBreakoutEvent(ctx, event.TypeBreakoutRoomCreated, call, event.BreakoutRoomPayload{
			CallID: callID,
			Room:   &room,
		})

		// Host-driven pre-assignment of participants into this room.
		for _, uid := range input.ParticipantUserIDs {
			if uid == userID {
				continue // never move the host/actor automatically
			}
			if !connected[uid] {
				continue // skip unknown / non-connected ids silently
			}
			if err := s.breakoutRooms.AssignParticipant(ctx, callID, uid, room.ID); err != nil {
				slog.ErrorContext(ctx, "failed to pre-assign participant to breakout room",
					"call_id", callID, "user_id", uid, "breakout_room_id", room.ID, "error", err)
				continue
			}
			assigned := room.ID
			s.publishBreakoutEvent(ctx, event.TypeBreakoutParticipantMoved, call, event.BreakoutParticipantMovedPayload{
				CallID:         callID,
				UserID:         uid,
				BreakoutRoomID: &assigned,
			})
		}

		if input.TimeLimit != nil && *input.TimeLimit > maxTimeLimit {
			maxTimeLimit = *input.TimeLimit
		}
	}

	// Schedule a best-effort auto-close if any room has a positive time limit.
	s.scheduleBreakoutAutoClose(callID, call.CreatedBy, maxTimeLimit)

	slog.InfoContext(ctx, "breakout rooms created", "call_id", callID, "count", len(rooms), "user_id", userID)
	return rooms, nil
}

// connectedParticipants returns the set of user ids that are connected
// participants of the call, used to validate host pre-assignment requests.
func (s *Service) connectedParticipants(ctx context.Context, callID uuid.UUID) (map[uuid.UUID]bool, error) {
	participants, err := s.calls.ListParticipants(ctx, callID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list participants for breakout assignment", "call_id", callID, "error", err)
		return nil, cerrors.Internal("failed to list call participants", err)
	}
	connected := make(map[uuid.UUID]bool, len(participants))
	for _, p := range participants {
		if p.Status == entity.ParticipantStatusConnected {
			connected[p.UserID] = true
		}
	}
	return connected, nil
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
) (*LiveKitJoinInfo, error) {
	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		return nil, s.wrapCallError(ctx, err, callID, "join breakout room")
	}

	if call.Status != entity.CallStatusActive {
		return nil, cerrors.Forbidden("call is not active")
	}

	// Verify the breakout room exists and is active.
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
	if room.Status != entity.BreakoutRoomStatusActive {
		return nil, cerrors.Forbidden("breakout room is closed")
	}

	// Verify participant is connected to the call.
	participant, err := s.calls.GetParticipant(ctx, callID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.Forbidden("user is not a participant in this call")
		}
		return nil, cerrors.Internal("failed to get participant", err)
	}
	if participant.Status != entity.ParticipantStatusConnected {
		return nil, cerrors.Forbidden("participant is not connected")
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
		return nil, cerrors.Internal("failed to join breakout room", err)
	}

	// Make sure the dedicated LiveKit breakout room exists before issuing a token.
	if err := s.EnsureLiveKitBreakoutRoom(ctx, call, breakoutRoomID); err != nil {
		slog.WarnContext(ctx, "failed to ensure livekit breakout room on join", "breakout_room_id", breakoutRoomID, "error", err)
	}

	s.publishBreakoutEvent(ctx, event.TypeBreakoutParticipantMoved, call, event.BreakoutParticipantMovedPayload{
		CallID:         callID,
		UserID:         userID,
		BreakoutRoomID: &breakoutRoomID,
	})

	slog.InfoContext(ctx, "participant joined breakout room",
		"call_id", callID, "user_id", userID, "breakout_room_id", breakoutRoomID)

	if !s.livekit.IsConfigured() {
		return nil, nil
	}
	return s.IssueLiveKitBreakoutJoinInfo(ctx, call, breakoutRoomID, userID)
}

// ReturnToMainRoom moves a participant from their current breakout room back
// to the main call room.
func (s *Service) ReturnToMainRoom(ctx context.Context, callID, userID uuid.UUID) (*LiveKitJoinInfo, error) {
	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		return nil, s.wrapCallError(ctx, err, callID, "return to main room")
	}

	participant, err := s.calls.GetParticipant(ctx, callID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.Forbidden("user is not a participant in this call")
		}
		return nil, cerrors.Internal("failed to get participant", err)
	}

	if participant.BreakoutRoomID == nil {
		return nil, cerrors.Conflict("participant is already in the main room")
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
		return nil, cerrors.Internal("failed to return to main room", err)
	}

	s.publishBreakoutEvent(ctx, event.TypeBreakoutParticipantMoved, call, event.BreakoutParticipantMovedPayload{
		CallID:         callID,
		UserID:         userID,
		BreakoutRoomID: nil,
	})

	slog.InfoContext(ctx, "participant returned to main room", "call_id", callID, "user_id", userID)

	if !s.livekit.IsConfigured() {
		return nil, nil
	}
	// Re-issue a token for the MAIN call room so the FE can reconnect there.
	return s.IssueLiveKitJoinInfo(ctx, call, userID, "")
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

	// Close the breakout SFU room (legacy Pion plane).
	sfuRoomID := breakoutSFURoomID(callID, breakoutRoomID)
	s.sfu.CloseRoom(sfuRoomID)

	// Delete the dedicated LiveKit breakout room (best-effort, swallows NotFound).
	if s.livekitRooms != nil {
		if err := s.livekitRooms.DeleteRoomByName(ctx, breakoutLiveKitRoomName(callID, breakoutRoomID)); err != nil {
			slog.WarnContext(ctx, "failed to delete livekit breakout room", "breakout_room_id", breakoutRoomID, "error", err)
		}
	}

	// Close the DB record.
	if err := s.breakoutRooms.Close(ctx, breakoutRoomID); err != nil {
		slog.ErrorContext(ctx, "failed to close breakout room", "breakout_room_id", breakoutRoomID, "error", err)
		return cerrors.Internal("failed to close breakout room", err)
	}

	s.publishBreakoutEvent(ctx, event.TypeBreakoutRoomClosed, call, event.BreakoutRoomPayload{
		CallID: callID,
		Room:   room,
	})

	// If no active breakout rooms remain, cancel any pending auto-close timer.
	if remaining, err := s.breakoutRooms.ListByCall(ctx, callID); err == nil && !hasActiveBreakoutRoom(remaining) {
		s.cancelBreakoutAutoClose(callID)
	}

	slog.InfoContext(ctx, "breakout room closed", "call_id", callID, "breakout_room_id", breakoutRoomID, "user_id", userID)
	return nil
}

// hasActiveBreakoutRoom reports whether any of the rooms is still active.
func hasActiveBreakoutRoom(rooms []entity.BreakoutRoom) bool {
	for _, r := range rooms {
		if r.Status == entity.BreakoutRoomStatusActive {
			return true
		}
	}
	return false
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

	// Cancel any pending auto-close timer first (CloseAll supersedes it and the
	// timer itself ends up here — cancelling makes the operation idempotent).
	s.cancelBreakoutAutoClose(callID)

	// Close each breakout room media plane and unassign participants.
	for _, room := range rooms {
		if room.Status != entity.BreakoutRoomStatusActive {
			continue
		}
		if err := s.breakoutRooms.UnassignAllByRoom(ctx, room.ID); err != nil {
			slog.ErrorContext(ctx, "failed to unassign participants", "breakout_room_id", room.ID, "error", err)
		}
		sfuRoomID := breakoutSFURoomID(callID, room.ID)
		s.sfu.CloseRoom(sfuRoomID)
		if s.livekitRooms != nil {
			if err := s.livekitRooms.DeleteRoomByName(ctx, breakoutLiveKitRoomName(callID, room.ID)); err != nil {
				slog.WarnContext(ctx, "failed to delete livekit breakout room", "breakout_room_id", room.ID, "error", err)
			}
		}
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

// breakoutRoomNameSeparator delimits the call and breakout-room UUIDs in a
// breakout room name ("{callID}:breakout:{breakoutRoomID}").
const breakoutRoomNameSeparator = ":breakout:"

// breakoutSFURoomID returns the SFU room ID for a breakout room.
// Format: {callID}:breakout:{breakoutRoomID}
func breakoutSFURoomID(callID, breakoutRoomID uuid.UUID) string {
	return fmt.Sprintf("%s%s%s", callID, breakoutRoomNameSeparator, breakoutRoomID)
}

// breakoutLiveKitRoomName returns the LiveKit room name for a breakout room.
// It uses the same scheme as breakoutSFURoomID so the main-room/breakout-room
// distinction is consistent across the legacy SFU and the LiveKit media plane.
func breakoutLiveKitRoomName(callID, breakoutRoomID uuid.UUID) string {
	return fmt.Sprintf("%s%s%s", callID, breakoutRoomNameSeparator, breakoutRoomID)
}

// parseBreakoutRoomName parses a "{callID}:breakout:{breakoutRoomID}" room name
// back into its call and breakout-room UUIDs. ok is false if the name does not
// match the scheme or either segment is not a valid UUID.
func parseBreakoutRoomName(name string) (callID, breakoutRoomID uuid.UUID, ok bool) {
	left, right, found := strings.Cut(name, breakoutRoomNameSeparator)
	if !found {
		return uuid.Nil, uuid.Nil, false
	}
	callID, err := uuid.Parse(left)
	if err != nil {
		return uuid.Nil, uuid.Nil, false
	}
	breakoutRoomID, err = uuid.Parse(right)
	if err != nil {
		return uuid.Nil, uuid.Nil, false
	}
	return callID, breakoutRoomID, true
}
