package call

import (
	"context"
	"log/slog"
	"sync"
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
		settings.TokenTTL = 6 * time.Hour
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
	canSubscribe := true
	canPublishData := true
	grant := &auth.VideoGrant{
		Room:           call.ID.String(),
		RoomJoin:       true,
		CanPublish:     &canPublish,
		CanSubscribe:   &canSubscribe,
		CanPublishData: &canPublishData,
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

func (s *Service) removeLiveKitParticipantBestEffort(ctx context.Context, callID, userID uuid.UUID) {
	if err := s.RemoveLiveKitParticipant(ctx, callID, userID); err != nil {
		slog.WarnContext(ctx, "failed to remove livekit participant", "call_id", callID, "user_id", userID, "error", err)
	}
}

// livekitWebhookDedupe tracks recently-processed webhook event ids so
// LiveKit retries do not cause double-broadcasts.
type livekitWebhookDedupe struct {
	mu     sync.Mutex
	seen   map[string]time.Time
	maxAge time.Duration
}

func newLiveKitWebhookDedupe(maxAge time.Duration) *livekitWebhookDedupe {
	if maxAge <= 0 {
		maxAge = 10 * time.Minute
	}
	return &livekitWebhookDedupe{seen: make(map[string]time.Time), maxAge: maxAge}
}

// markFresh returns true if the event id has not been processed within
// maxAge. It also evicts older entries opportunistically.
func (d *livekitWebhookDedupe) markFresh(id string) bool {
	if id == "" {
		return true
	}
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, ts := range d.seen {
		if now.Sub(ts) > d.maxAge {
			delete(d.seen, k)
		}
	}
	if _, exists := d.seen[id]; exists {
		return false
	}
	d.seen[id] = now
	return true
}

// HandleLiveKitWebhook processes a verified LiveKit WebhookEvent and bridges
// it to domain state + workspace WS broadcasts. Webhook signature MUST be
// verified by the caller using protocol/webhook.ReceiveWebhookEvent.
func (s *Service) HandleLiveKitWebhook(ctx context.Context, ev *livekitpb.WebhookEvent) error {
	if ev == nil {
		return cerrors.InvalidInput("livekit webhook event is nil")
	}
	if !s.livekitDedupe.markFresh(ev.GetId()) {
		return nil
	}

	roomName := ev.GetRoom().GetName()
	if roomName == "" {
		slog.WarnContext(ctx, "livekit webhook missing room name", "event", ev.GetEvent(), "id", ev.GetId())
		return nil
	}
	callID, err := uuid.Parse(roomName)
	if err != nil {
		// room name is not a call uuid; ignore (room may belong to a different feature)
		return nil
	}

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
	default:
		// room_started / recording_started — not used in this slice
		return nil
	}
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

	if err := endCallWithReason(ctx, s.calls, callID, entity.CallEndReasonAllLeft); err != nil {
		return cerrors.Internal("failed to mark call ended", err)
	}
	markCallEnded(call, entity.CallEndReasonAllLeft)

	s.publishCallEvent(ctx, event.TypeCallEnded, call, call.CreatedBy)
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

	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return cerrors.Internal("failed to load call for livekit participant_joined", err)
	}

	// Transition ringing → active when a non-caller joins (or any first join in a multi-party call).
	if call.Status == entity.CallStatusRinging && userID != call.CreatedBy {
		if err := s.calls.UpdateStatus(ctx, callID, entity.CallStatusActive); err != nil {
			return cerrors.Internal("failed to mark call active", err)
		}
		call.Status = entity.CallStatusActive
	}

	s.publishParticipantEvent(ctx, event.TypeCallParticipantJoined, call, participant)
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

	participant, err := s.calls.GetParticipant(ctx, callID, userID)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return cerrors.Internal("failed to load participant for livekit participant_left", err)
	}

	if participant.Status != entity.ParticipantStatusDisconnected {
		if err := updateParticipantStatusWithReason(ctx, s.calls, participant.ID, entity.ParticipantStatusDisconnected, entity.ParticipantLeftReasonLeft); err != nil {
			return cerrors.Internal("failed to mark participant disconnected", err)
		}
		markParticipantDisconnected(participant, entity.ParticipantLeftReasonLeft)
	}

	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return cerrors.Internal("failed to load call for livekit participant_left", err)
	}

	s.publishParticipantEvent(ctx, event.TypeCallParticipantLeft, call, participant)
	participants, err := s.calls.ListParticipants(ctx, callID)
	if err != nil {
		return cerrors.Internal("failed to list participants for livekit participant_left", err)
	}
	if shouldAutoEndAfterLeave(call, participants) {
		if err := endCallWithReason(ctx, s.calls, callID, entity.CallEndReasonAllLeft); err != nil {
			return cerrors.Internal("failed to auto-end call for livekit participant_left", err)
		}
		markCallEnded(call, entity.CallEndReasonAllLeft)
		s.publishCallEvent(ctx, event.TypeCallEnded, call, userID)
		s.closeAllBreakoutSFURooms(ctx, callID)
		s.deleteLiveKitRoomBestEffort(ctx, callID)
		if s.sfu != nil {
			s.sfu.CloseRoom(callID.String())
		}
	}
	slog.InfoContext(ctx, "livekit participant_left bridged", "call_id", callID, "user_id", userID, "reason", p.GetDisconnectReason().String())
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
