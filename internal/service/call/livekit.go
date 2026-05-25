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
	URL         string    `json:"livekit_url"`
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
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
}

// IssueLiveKitJoinInfo signs an access token granting the user permission to
// join the LiveKit room dedicated to the given call.
func (s *Service) IssueLiveKitJoinInfo(call *entity.Call, userID uuid.UUID, displayName string) (*LiveKitJoinInfo, error) {
	if call == nil {
		return nil, cerrors.InvalidInput("call is required")
	}
	if !s.livekit.IsConfigured() {
		return nil, cerrors.Unavailable("livekit is not configured")
	}

	canPublish := true
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

	return &LiveKitJoinInfo{
		URL:         s.livekit.URL,
		AccessToken: token,
		ExpiresAt:   time.Now().Add(s.livekit.TokenTTL).UTC(),
	}, nil
}

// LiveKitConfigured exposes the livekit configuration state to handlers.
func (s *Service) LiveKitConfigured() bool {
	return s.livekit.IsConfigured()
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
	if s.livekitDedupe == nil {
		s.livekitDedupe = newLiveKitWebhookDedupe(10 * time.Minute)
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
	default:
		// room_started / track_published / track_unpublished / recording_started — not used in this slice
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

	if err := s.calls.End(ctx, callID); err != nil {
		return cerrors.Internal("failed to mark call ended", err)
	}
	now := time.Now()
	call.Status = entity.CallStatusEnded
	call.EndedAt = &now

	s.publishCallEvent(ctx, event.TypeCallEnded, call, call.CreatedBy)
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
		if err := s.calls.UpdateParticipantStatus(ctx, participant.ID, entity.ParticipantStatusConnected); err != nil {
			return cerrors.Internal("failed to mark participant connected", err)
		}
		participant.Status = entity.ParticipantStatusConnected
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
		if err := s.calls.UpdateParticipantStatus(ctx, participant.ID, entity.ParticipantStatusDisconnected); err != nil {
			return cerrors.Internal("failed to mark participant disconnected", err)
		}
		now := time.Now()
		participant.Status = entity.ParticipantStatusDisconnected
		participant.LeftAt = &now
	}

	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return cerrors.Internal("failed to load call for livekit participant_left", err)
	}

	s.publishParticipantEvent(ctx, event.TypeCallParticipantLeft, call, participant)
	slog.InfoContext(ctx, "livekit participant_left bridged", "call_id", callID, "user_id", userID, "reason", p.GetDisconnectReason().String())
	return nil
}

func isNotFound(err error) bool {
	if appErr, ok := cerrors.AsAppError(err); ok {
		return appErr.Code == cerrors.CodeNotFound
	}
	return false
}
