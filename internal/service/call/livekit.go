package call

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/livekit/protocol/auth"
	livekitpb "github.com/livekit/protocol/livekit"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/pkg/cerrors"
)

// LiveKitSettings holds the SFU connection parameters needed to issue
// access tokens and accept signed webhooks.
type LiveKitSettings struct {
	URL       string
	APIKey    string
	APISecret string
	TokenTTL  time.Duration
}

const liveKitWebhookClaimLease = 2 * time.Minute

// screenShareSources returns the publishable track sources for a participant.
// Hosts/co-hosts and granted participants may publish screen + audio; everyone
// else who can publish is limited to camera + microphone. Setting CanPublishSources
// supersedes CanPublish, so the SDK cannot publish a screen track unless listed.
func screenShareSources(canShare bool) []livekitpb.TrackSource {
	base := []livekitpb.TrackSource{livekitpb.TrackSource_CAMERA, livekitpb.TrackSource_MICROPHONE}
	if canShare {
		return append(base, livekitpb.TrackSource_SCREEN_SHARE, livekitpb.TrackSource_SCREEN_SHARE_AUDIO)
	}
	return base
}

// canShareScreen reports whether a participant may publish a screen-share track:
// hosts/co-hosts always may; everyone else needs an explicit per-participant grant.
func canShareScreen(p entity.CallParticipant) bool {
	return p.Role == entity.CallRoleHost || p.Role == entity.CallRoleCoHost || p.CanScreenShare
}

// LiveKitJoinInfo is appended to the StartCall / JoinCall response so the FE
// can connect to the LiveKit room without a second round-trip.
type LiveKitJoinInfo struct {
	URL            string    `json:"livekit_url"`
	AccessToken    string    `json:"access_token"`
	ExpiresAt      time.Time `json:"expires_at"`
	TokenExpiresAt time.Time `json:"token_expires_at"`
	RefreshAfter   time.Time `json:"refresh_after"`
}

// IsConfigured reports whether the SFU integration is wired up.
func (s LiveKitSettings) IsConfigured() bool {
	return s.URL != "" && s.APIKey != "" && s.APISecret != ""
}

// SetLiveKit installs the LiveKit settings on the call service.
func (s *Service) SetLiveKit(settings LiveKitSettings) {
	if settings.TokenTTL <= 0 {
		// Short initial TTL; LiveKit refreshes connected clients' tokens
		// server-side, so long calls are unaffected (see config.go).
		settings.TokenTTL = 30 * time.Minute
	}
	s.livekit = settings
	if settings.IsConfigured() {
		s.livekitRooms = newLiveKitRoomServiceClient(settings)
	} else {
		s.livekitRooms = nil
	}
}

func (s *Service) SetLiveKitRoomClient(client LiveKitRoomClient) {
	s.livekitRooms = client
}

// IssueLiveKitJoinInfo signs an access token granting the user permission to
// join the LiveKit room dedicated to the given call. The grant is derived from
// the persisted participant row so waiting-room users cannot bypass admission
// and viewer roles cannot publish media.
func (s *Service) IssueLiveKitJoinInfo(ctx context.Context, call *entity.Call, userID uuid.UUID, displayName string) (*LiveKitJoinInfo, error) {
	if call == nil {
		return nil, cerrors.InvalidInput("call is required")
	}
	if !s.livekit.IsConfigured() {
		return nil, cerrors.Unavailable("livekit is not configured")
	}

	participant, err := s.calls.GetParticipant(ctx, call.ID, userID)
	if err != nil {
		if isNotFound(err) {
			return nil, cerrors.Forbidden("user is not a participant in this call")
		}
		return nil, cerrors.Internal("failed to load call participant for livekit token", err)
	}
	if participant.Status != entity.ParticipantStatusConnected {
		return nil, cerrors.Forbidden("participant is not connected")
	}

	canPublish := participant.Role != entity.CallRoleViewer
	return s.mintLiveKitToken(call.ID.String(), userID, displayName, canPublish, canShareScreen(*participant))
}

// IssueLiveKitBreakoutJoinInfo signs an access token granting the user
// permission to join the dedicated LiveKit room for a breakout sub-session.
// The room name uses the breakout scheme ({callID}:breakout:{breakoutRoomID})
// so breakout media is isolated from the main room. The grant is derived from
// the persisted call participant row (must be connected; viewers cannot publish).
func (s *Service) IssueLiveKitBreakoutJoinInfo(ctx context.Context, call *entity.Call, breakoutRoomID, userID uuid.UUID) (*LiveKitJoinInfo, error) {
	if call == nil {
		return nil, cerrors.InvalidInput("call is required")
	}
	if !s.livekit.IsConfigured() {
		return nil, cerrors.Unavailable("livekit is not configured")
	}

	participant, err := s.calls.GetParticipant(ctx, call.ID, userID)
	if err != nil {
		if isNotFound(err) {
			return nil, cerrors.Forbidden("user is not a participant in this call")
		}
		return nil, cerrors.Internal("failed to load call participant for livekit breakout token", err)
	}
	if participant.Status != entity.ParticipantStatusConnected {
		return nil, cerrors.Forbidden("participant is not connected")
	}

	canPublish := participant.Role != entity.CallRoleViewer
	roomName := breakoutLiveKitRoomName(call.ID, breakoutRoomID)
	return s.mintLiveKitToken(roomName, userID, "", canPublish, canShareScreen(*participant))
}

// mintLiveKitToken signs a LiveKit access token for the given room and user.
// Shared by the main-room and breakout-room join flows so the grant/signing
// logic stays in one place. When canPublish is true the publishable track
// sources are gated by canShareScreen — non-granted, non-host participants get
// camera + microphone only, so the SDK cannot publish a screen track at all.
func (s *Service) mintLiveKitToken(roomName string, userID uuid.UUID, displayName string, canPublish, canShare bool) (*LiveKitJoinInfo, error) {
	canPublishFlag := canPublish
	canSubscribe := true
	canPublishData := true
	grant := &auth.VideoGrant{
		Room:           roomName,
		RoomJoin:       true,
		CanPublish:     &canPublishFlag,
		CanSubscribe:   &canSubscribe,
		CanPublishData: &canPublishData,
	}
	if canPublish {
		// Setting CanPublishSources supersedes CanPublish; only sharers get
		// the screen + screen-audio sources (ALK-697).
		grant.SetCanPublishSources(screenShareSources(canShare))
	}

	name := displayName
	if name == "" {
		name = userID.String()
	}

	at := auth.NewAccessToken(s.livekit.APIKey, s.livekit.APISecret).
		SetVideoGrant(grant).
		SetIdentity(userID.String()).
		SetName(name).
		SetValidFor(s.livekit.TokenTTL)

	token, err := at.ToJWT()
	if err != nil {
		return nil, cerrors.Internal("failed to sign livekit access token", err)
	}

	expiresAt := time.Now().Add(s.livekit.TokenTTL).UTC()
	return &LiveKitJoinInfo{
		URL:            s.livekit.URL,
		AccessToken:    token,
		ExpiresAt:      expiresAt,
		TokenExpiresAt: expiresAt,
		RefreshAfter:   expiresAt.Add(-(s.livekit.TokenTTL / 2)),
	}, nil
}

// LiveKitConfigured exposes the livekit configuration state to handlers.
func (s *Service) LiveKitConfigured() bool {
	return s.livekit.IsConfigured()
}

func (s *Service) EnsureLiveKitRoom(ctx context.Context, call *entity.Call) error {
	if call == nil || !s.livekit.IsConfigured() || s.livekitRooms == nil {
		return nil
	}

	maxParticipants := uint32(0)
	if cap := s.effectiveParticipantCap(call); cap > 0 {
		maxParticipants = uint32(cap)
	}

	return s.livekitRooms.EnsureRoom(ctx, LiveKitEnsureRoomArgs{
		CallID:          call.ID,
		WorkspaceID:     call.WorkspaceID,
		CallType:        call.Type,
		MaxParticipants: maxParticipants,
		EmptyTimeout:    defaultLiveKitEmptyTimeout,
		Metadata:        liveKitRoomMetadata(call),
	})
}

// EnsureLiveKitBreakoutRoom creates (or refreshes) the dedicated LiveKit room
// backing a breakout sub-session. Best-effort and a no-op when LiveKit is not
// wired up, mirroring EnsureLiveKitRoom.
func (s *Service) EnsureLiveKitBreakoutRoom(ctx context.Context, call *entity.Call, breakoutRoomID uuid.UUID) error {
	if call == nil || !s.livekit.IsConfigured() || s.livekitRooms == nil {
		return nil
	}

	// Cancel any pending deferred delete for this room name so a freshly
	// (re-)created or rejoined breakout room is not deleted out from under its
	// participants by a grace timer left over from a prior close.
	s.cancelBreakoutLiveKitDelete(breakoutLiveKitRoomName(call.ID, breakoutRoomID))

	maxParticipants := uint32(0)
	if cap := s.effectiveParticipantCap(call); cap > 0 {
		maxParticipants = uint32(cap)
	}

	return s.livekitRooms.EnsureRoom(ctx, LiveKitEnsureRoomArgs{
		CallID:          call.ID,
		RoomName:        breakoutLiveKitRoomName(call.ID, breakoutRoomID),
		WorkspaceID:     call.WorkspaceID,
		CallType:        call.Type,
		MaxParticipants: maxParticipants,
		EmptyTimeout:    defaultLiveKitEmptyTimeout,
		Metadata:        liveKitRoomMetadata(call),
	})
}

func (s *Service) EnsureLiveKitRoomRequired(ctx context.Context, call *entity.Call) error {
	if call == nil {
		return cerrors.InvalidInput("call is required")
	}
	if !s.livekit.IsConfigured() {
		return nil
	}
	if s.livekitRooms == nil {
		return cerrors.Unavailable("livekit room service is not configured")
	}
	return s.EnsureLiveKitRoom(ctx, call)
}

func (s *Service) DeleteLiveKitRoom(ctx context.Context, callID uuid.UUID) error {
	if !s.livekit.IsConfigured() || s.livekitRooms == nil {
		return nil
	}
	return s.livekitRooms.DeleteRoom(ctx, callID)
}

func (s *Service) RemoveLiveKitParticipant(ctx context.Context, callID, userID uuid.UUID) error {
	if !s.livekit.IsConfigured() || s.livekitRooms == nil {
		return nil
	}
	return s.livekitRooms.RemoveParticipant(ctx, callID, userID)
}

func (s *Service) ensureLiveKitRoomBestEffort(ctx context.Context, call *entity.Call) {
	if err := s.EnsureLiveKitRoom(ctx, call); err != nil {
		slog.WarnContext(ctx, "failed to ensure livekit room", "call_id", call.ID, "error", err)
	}
}

func (s *Service) deleteLiveKitRoomBestEffort(ctx context.Context, callID uuid.UUID) {
	if err := s.DeleteLiveKitRoom(ctx, callID); err != nil {
		slog.WarnContext(ctx, "failed to delete livekit room", "call_id", callID, "error", err)
	}
}

// stopActiveEgressForCallBestEffort stops an in-flight recording egress when a
// call ends without an explicit host Stop (ALK-701). The ensuing egress_ended
// webhook finalizes the recording to ready. No-op when egress is not wired.
func (s *Service) stopActiveEgressForCallBestEffort(ctx context.Context, callID uuid.UUID) {
	if s.egressSink == nil {
		return
	}
	if err := s.egressSink.StopActiveEgressForCall(ctx, callID); err != nil {
		slog.WarnContext(ctx, "failed to stop active egress on call end", "call_id", callID, "error", err)
	}
}

func (s *Service) removeLiveKitParticipantBestEffort(ctx context.Context, callID, userID uuid.UUID) {
	if err := s.RemoveLiveKitParticipant(ctx, callID, userID); err != nil {
		slog.WarnContext(ctx, "failed to remove livekit participant", "call_id", callID, "user_id", userID, "error", err)
	}
}

// HandleLiveKitWebhook processes a verified LiveKit WebhookEvent and bridges
// it to domain state + workspace WS broadcasts. Webhook signature MUST be
// verified by the caller using protocol/webhook.ReceiveWebhookEvent.
func (s *Service) HandleLiveKitWebhook(ctx context.Context, ev *livekitpb.WebhookEvent) error {
	if ev == nil {
		return cerrors.InvalidInput("livekit webhook event is nil")
	}

	roomName := ev.GetRoom().GetName()
	if roomName == "" {
		// Egress webhooks (egress_*) may not populate the top-level Room; the
		// room is on the EgressInfo instead (ALK-701).
		roomName = ev.GetEgressInfo().GetRoomName()
	}
	if roomName == "" {
		slog.WarnContext(ctx, "livekit webhook missing room name", "event", ev.GetEvent(), "id", ev.GetId())
		return nil
	}

	// Breakout rooms use the "{callID}:breakout:{breakoutRoomID}" scheme and are
	// not bare call UUIDs. Bridge their participant_left so the participant is
	// unassigned from the breakout room in our domain state; ignore everything else.
	if strings.Contains(roomName, breakoutRoomNameSeparator) {
		return s.handleLiveKitBreakoutWebhook(ctx, ev, roomName)
	}

	callID, err := uuid.Parse(roomName)
	if err != nil {
		// room name is not a call uuid; ignore (room may belong to a different feature)
		return nil
	}

	claim, claimToken, err := s.claimLiveKitWebhookEvent(ctx, ev, callID)
	if err != nil {
		return cerrors.Internal("failed to claim livekit webhook event", err)
	}
	switch claim {
	case entity.LiveKitWebhookClaimDuplicate:
		return nil
	case entity.LiveKitWebhookClaimInProgress:
		return cerrors.Conflict("livekit webhook event is already processing")
	}

	if err := s.processLiveKitWebhook(ctx, ev, callID); err != nil {
		return err
	}
	if err := s.markLiveKitWebhookEventProcessed(ctx, ev.GetId(), claimToken); err != nil {
		return err
	}
	return nil
}

func (s *Service) processLiveKitWebhook(ctx context.Context, ev *livekitpb.WebhookEvent, callID uuid.UUID) error {
	switch ev.GetEvent() {
	case "room_finished":
		return s.handleLiveKitRoomFinished(ctx, callID)
	case "participant_joined":
		return s.handleLiveKitParticipantJoined(ctx, callID, ev.GetParticipant())
	case "participant_left":
		return s.handleLiveKitParticipantLeft(ctx, callID, ev.GetParticipant())
	case "track_published":
		return s.handleLiveKitTrackChanged(ctx, callID, ev.GetParticipant(), ev.GetTrack(), true)
	case "track_unpublished":
		return s.handleLiveKitTrackChanged(ctx, callID, ev.GetParticipant(), ev.GetTrack(), false)
	case "egress_started", "egress_updated", "egress_ended":
		return s.handleLiveKitEgressEvent(ctx, callID, ev.GetEvent(), ev.GetEgressInfo())
	default:
		// room_started and other events are not used in this slice.
		return nil
	}
}

// handleLiveKitEgressEvent bridges a LiveKit egress_* webhook to the recording
// finalizer (ALK-701). egress_ended on EGRESS_COMPLETE finalizes the recording
// to ready with the composite artifact; FAILED/ABORTED marks it failed.
func (s *Service) handleLiveKitEgressEvent(ctx context.Context, callID uuid.UUID, eventName string, info *livekitpb.EgressInfo) error {
	if info == nil || s.egressSink == nil {
		return nil
	}
	out := EgressEventInfo{
		EgressID: info.GetEgressId(),
		CallID:   callID,
	}
	switch eventName {
	case "egress_started":
		out.Phase = EgressEventStarted
	case "egress_updated":
		out.Phase = EgressEventUpdated
	case "egress_ended":
		out.Phase = EgressEventEnded
		out.Succeeded = info.GetStatus() == livekitpb.EgressStatus_EGRESS_COMPLETE
		if files := info.GetFileResults(); len(files) > 0 {
			out.FileSizeBytes = files[0].GetSize()
			// LiveKit reports duration in nanoseconds.
			out.DurationSeconds = int(files[0].GetDuration() / int64(time.Second))
		}
	}
	return s.egressSink.HandleEgressEvent(ctx, out)
}

func (s *Service) claimLiveKitWebhookEvent(ctx context.Context, ev *livekitpb.WebhookEvent, callID uuid.UUID) (entity.LiveKitWebhookClaimResult, string, error) {
	eventID := ev.GetId()
	if eventID == "" {
		return entity.LiveKitWebhookClaimProcess, "", nil
	}
	webhookRepo, ok := s.calls.(liveKitWebhookRepository)
	if !ok {
		return entity.LiveKitWebhookClaimProcess, "", nil
	}
	leaseExpiresAt := time.Now().Add(liveKitWebhookClaimLease).UTC()
	claimToken := uuid.NewString()
	result, err := webhookRepo.ClaimLiveKitWebhookEvent(ctx, &entity.LiveKitWebhookEvent{
		EventID:        eventID,
		CallID:         callID,
		EventType:      ev.GetEvent(),
		Status:         "processing",
		ClaimToken:     claimToken,
		ReceivedAt:     time.Now().UTC(),
		LeaseExpiresAt: &leaseExpiresAt,
	})
	return result, claimToken, err
}

func (s *Service) markLiveKitWebhookEventProcessed(ctx context.Context, eventID, claimToken string) error {
	if eventID == "" {
		return nil
	}
	webhookRepo, ok := s.calls.(liveKitWebhookRepository)
	if !ok {
		return nil
	}
	marked, err := webhookRepo.MarkLiveKitWebhookEventProcessed(ctx, eventID, claimToken)
	if err != nil {
		return cerrors.Internal("failed to mark livekit webhook processed", err)
	}
	if !marked {
		return cerrors.Conflict("livekit webhook event claim was superseded")
	}
	return nil
}

func (s *Service) handleLiveKitRoomFinished(ctx context.Context, callID uuid.UUID) error {
	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return cerrors.Internal("failed to load call for livekit room_finished", err)
	}
	if call.Status == entity.CallStatusEnded {
		return nil
	}

	ended, err := endCallWithReasonIfNotEnded(ctx, s.calls, callID, entity.CallEndReasonAllLeft)
	if err != nil {
		return cerrors.Internal("failed to mark call ended", err)
	}
	if !ended {
		return nil
	}
	markCallEnded(call, entity.CallEndReasonAllLeft)

	s.publishCallEvent(ctx, event.TypeCallEnded, call, call.CreatedBy)
	s.stopActiveEgressForCallBestEffort(ctx, callID)
	s.closeAllBreakoutSFURooms(ctx, callID)
	s.deleteLiveKitRoomBestEffort(ctx, callID)
	if s.sfu != nil {
		s.sfu.CloseRoom(callID.String())
	}
	slog.InfoContext(ctx, "livekit room_finished bridged", "call_id", callID)
	return nil
}

func (s *Service) handleLiveKitParticipantJoined(ctx context.Context, callID uuid.UUID, p *livekitpb.ParticipantInfo) error {
	if p == nil {
		return nil
	}
	userID, err := uuid.Parse(p.GetIdentity())
	if err != nil {
		return nil
	}

	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return cerrors.Internal("failed to load call for livekit participant_joined", err)
	}
	if call.Status == entity.CallStatusEnded {
		return nil
	}

	participant, err := s.calls.GetParticipant(ctx, callID, userID)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return cerrors.Internal("failed to load participant for livekit participant_joined", err)
	}

	if participant.Status != entity.ParticipantStatusConnected {
		s.removeLiveKitParticipantBestEffort(ctx, callID, userID)
		slog.WarnContext(ctx, "ignored livekit participant_joined for non-connected participant", "call_id", callID, "user_id", userID, "status", participant.Status)
		return nil
	}

	// Transition ringing → active when a non-caller joins (or any first join in a multi-party call).
	activatedCall := false
	if call.Status == entity.CallStatusRinging && userID != call.CreatedBy {
		activated, err := activateRingingCall(ctx, s.calls, callID)
		if err != nil {
			return cerrors.Internal("failed to mark call active", err)
		}
		if !activated {
			return nil
		}
		call.Status = entity.CallStatusActive
		activatedCall = true
	}

	if activatedCall {
		s.publishParticipantEvent(ctx, event.TypeCallParticipantJoined, call, participant)
	}
	slog.InfoContext(ctx, "livekit participant_joined bridged", "call_id", callID, "user_id", userID)
	return nil
}

func (s *Service) handleLiveKitParticipantLeft(ctx context.Context, callID uuid.UUID, p *livekitpb.ParticipantInfo) error {
	if p == nil {
		return nil
	}
	userID, err := uuid.Parse(p.GetIdentity())
	if err != nil {
		return nil
	}

	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return cerrors.Internal("failed to load call for livekit participant_left", err)
	}
	if call.Status == entity.CallStatusEnded {
		return nil
	}

	participant, err := s.calls.GetParticipant(ctx, callID, userID)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return cerrors.Internal("failed to load participant for livekit participant_left", err)
	}

	// Breakout transition, not a call leave: breakout rooms are separate LiveKit
	// rooms, so moving a participant into one disconnects them from the MAIN room
	// and fires participant_left here. While they are assigned to a breakout room
	// we must NOT mark them disconnected, emit participant.left, or count this
	// toward auto-end — doing so emptied the host's roster ("0 in each room", movers
	// "fell out") and could even auto-end the call. Their real call leave is handled
	// by the breakout-room participant_left webhook (handleLiveKitBreakoutWebhook).
	if participant.BreakoutRoomID != nil {
		slog.InfoContext(ctx, "livekit participant_left ignored while in breakout room",
			"call_id", callID, "user_id", userID, "breakout_room_id", *participant.BreakoutRoomID)
		return nil
	}

	if participant.Status != entity.ParticipantStatusDisconnected {
		disconnected, err := disconnectParticipantIfConnected(ctx, s.calls, participant.ID, entity.ParticipantLeftReasonLeft)
		if err != nil {
			return cerrors.Internal("failed to mark participant disconnected", err)
		}
		if disconnected {
			markParticipantDisconnected(participant, entity.ParticipantLeftReasonLeft)
			s.publishParticipantEvent(ctx, event.TypeCallParticipantLeft, call, participant)
		}
	}

	return s.autoEndAfterLiveKitParticipantLeft(ctx, call, callID, userID, p.GetDisconnectReason().String())
}

func (s *Service) autoEndAfterLiveKitParticipantLeft(ctx context.Context, call *entity.Call, callID, userID uuid.UUID, reason string) error {
	participants, err := s.calls.ListParticipants(ctx, callID)
	if err != nil {
		return cerrors.Internal("failed to list participants for livekit participant_left", err)
	}
	if shouldAutoEndAfterLeave(call, participants) {
		ended, err := endCallWithReasonIfNotEnded(ctx, s.calls, callID, entity.CallEndReasonAllLeft)
		if err != nil {
			return cerrors.Internal("failed to auto-end call for livekit participant_left", err)
		}
		if !ended {
			return nil
		}
		markCallEnded(call, entity.CallEndReasonAllLeft)
		s.publishCallEvent(ctx, event.TypeCallEnded, call, userID)
		s.stopActiveEgressForCallBestEffort(ctx, callID)
		s.closeAllBreakoutSFURooms(ctx, callID)
		s.deleteLiveKitRoomBestEffort(ctx, callID)
		if s.sfu != nil {
			s.sfu.CloseRoom(callID.String())
		}
	}
	slog.InfoContext(ctx, "livekit participant_left bridged", "call_id", callID, "user_id", userID, "reason", reason)
	return nil
}

// handleLiveKitBreakoutWebhook bridges webhooks for breakout-room LiveKit rooms
// (named "{callID}:breakout:{breakoutRoomID}"). Only participant_left is acted
// on: the participant is unassigned from the breakout room in our domain state,
// mirroring ReturnToMainRoom's effect. Other breakout events are ignored.
//
// We deliberately skip the livekit_webhook_events idempotency claim here: that
// table keys on a bare call UUID, and unassigning an already-unassigned
// participant is itself a no-op, so duplicate deliveries are harmless.
func (s *Service) handleLiveKitBreakoutWebhook(ctx context.Context, ev *livekitpb.WebhookEvent, roomName string) error {
	if ev.GetEvent() != "participant_left" {
		return nil
	}

	callID, breakoutRoomID, ok := parseBreakoutRoomName(roomName)
	if !ok {
		return nil
	}

	p := ev.GetParticipant()
	if p == nil {
		return nil
	}
	userID, err := uuid.Parse(p.GetIdentity())
	if err != nil {
		return nil
	}

	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return cerrors.Internal("failed to load call for livekit breakout participant_left", err)
	}

	participant, err := s.calls.GetParticipant(ctx, callID, userID)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return cerrors.Internal("failed to load participant for livekit breakout participant_left", err)
	}
	// Only act when the participant is actually assigned to this breakout room.
	if participant.BreakoutRoomID == nil || *participant.BreakoutRoomID != breakoutRoomID {
		return nil
	}

	if err := s.breakoutRooms.UnassignParticipant(ctx, callID, userID); err != nil {
		return cerrors.Internal("failed to unassign participant on livekit breakout participant_left", err)
	}

	s.publishBreakoutEvent(ctx, event.TypeBreakoutParticipantMoved, call, event.BreakoutParticipantMovedPayload{
		CallID:         callID,
		UserID:         userID,
		BreakoutRoomID: nil,
	})
	slog.InfoContext(ctx, "livekit breakout participant_left bridged", "call_id", callID, "breakout_room_id", breakoutRoomID, "user_id", userID)
	return nil
}

func (s *Service) handleLiveKitTrackChanged(ctx context.Context, callID uuid.UUID, p *livekitpb.ParticipantInfo, track *livekitpb.TrackInfo, screenSharing bool) error {
	if p == nil || track == nil {
		return nil
	}
	if track.GetSource() != livekitpb.TrackSource_SCREEN_SHARE {
		return nil
	}

	userID, err := uuid.Parse(p.GetIdentity())
	if err != nil {
		return nil
	}
	participant, err := s.calls.GetParticipant(ctx, callID, userID)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return cerrors.Internal("failed to load participant for livekit track event", err)
	}
	// Clear a stale host-featured share when its owner's screen track goes away.
	// Runs before the no-change early-return so a duplicate/late unpublish still
	// clears it. Best-effort: a failed clear logs and does not fail the webhook. (ALK-697)
	if !screenSharing {
		if c, cerr := s.calls.GetByID(ctx, callID); cerr == nil &&
			c.FeaturedShareUserID != nil && *c.FeaturedShareUserID == userID {
			if clearErr := s.calls.SetFeaturedShareUserID(ctx, callID, nil); clearErr != nil {
				slog.ErrorContext(ctx, "failed to clear featured share on track unpublish", "call_id", callID, "error", clearErr)
			} else {
				c.FeaturedShareUserID = nil
				s.publishShareEvent(ctx, event.TypeCallFeaturedShareUpdated, c, userID,
					event.FeaturedSharePayload{CallID: callID, FeaturedShareUserID: nil})
			}
		}
	}
	if participant.ScreenSharing == screenSharing {
		return nil
	}
	if err := s.calls.UpdateParticipantMedia(ctx, participant.ID, participant.AudioMuted, participant.VideoMuted, screenSharing); err != nil {
		return cerrors.Internal("failed to update participant screen share from livekit", err)
	}
	participant.ScreenSharing = screenSharing

	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return cerrors.Internal("failed to load call for livekit track event", err)
	}
	s.publishParticipantEvent(ctx, event.TypeCallParticipantUpdated, call, participant)
	slog.InfoContext(ctx, "livekit screen-share track bridged", "call_id", callID, "user_id", userID, "screen_sharing", screenSharing)
	return nil
}

func isNotFound(err error) bool {
	if appErr, ok := cerrors.AsAppError(err); ok {
		return appErr.Code == cerrors.CodeNotFound
	}
	return false
}
