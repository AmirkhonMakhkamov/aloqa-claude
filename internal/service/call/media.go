package call

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/media/sfu"
	"aloqa/internal/pkg/cerrors"
)

const (
	mediaRolePresenter = "presenter"
	mediaRoleViewer    = "viewer"
)

type MediaJoinToken struct {
	Token               string                          `json:"token"`
	Role                string                          `json:"role"`
	ExpiresAt           time.Time                       `json:"expires_at"`
	NodeID              string                          `json:"node_id,omitempty"`
	Region              string                          `json:"region,omitempty"`
	ControlURL          string                          `json:"control_url,omitempty"`
	MediaURL            string                          `json:"media_url,omitempty"`
	RoutingMode         entity.MediaRoutingMode         `json:"routing_mode,omitempty"`
	FanoutStrategy      entity.MediaFanoutStrategy      `json:"fanout_strategy,omitempty"`
	OverflowPolicy      entity.MediaOverflowPolicy      `json:"overflow_policy,omitempty"`
	ScreenSharePriority entity.MediaScreenSharePriority `json:"screen_share_priority,omitempty"`
	TURNStrategy        string                          `json:"turn_strategy,omitempty"`
}

type TurnCredentials struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
	TTL        int      `json:"ttl"`
}

type MediaOfferInput struct {
	CallID uuid.UUID `json:"-"`
	Token  string    `json:"token"`
	SDP    string    `json:"sdp"`
	Type   string    `json:"type"`
}

type MediaAnswer struct {
	SDP  string `json:"sdp"`
	Type string `json:"type"`
}

type MediaICECandidateInput struct {
	CallID        uuid.UUID `json:"-"`
	Token         string    `json:"token"`
	Candidate     string    `json:"candidate"`
	SDPMid        string    `json:"sdp_mid,omitempty"`
	SDPMLineIndex *int      `json:"sdp_mline_index,omitempty"`
}

type MediaQualityReportInput struct {
	StreamID             string  `json:"stream_id"`
	AvailableBitrateKbps int     `json:"available_bitrate_kbps,omitempty"`
	ObservedBitrateKbps  int     `json:"observed_bitrate_kbps,omitempty"`
	PacketLossPct        float64 `json:"packet_loss_pct,omitempty"`
	RoundTripTimeMs      int     `json:"round_trip_time_ms,omitempty"`
	JitterMs             float64 `json:"jitter_ms,omitempty"`
	AudioPacketLossPct   float64 `json:"audio_packet_loss_pct,omitempty"`
	AudioJitterMs        float64 `json:"audio_jitter_ms,omitempty"`
	FramesPerSecond      float64 `json:"frames_per_second,omitempty"`
	DroppedFramesPct     float64 `json:"dropped_frames_pct,omitempty"`
	DecodeTimeMs         float64 `json:"decode_time_ms,omitempty"`
	FreezeCountDelta     int     `json:"freeze_count_delta,omitempty"`
	NACKCountDelta       int     `json:"nack_count_delta,omitempty"`
	PLICountDelta        int     `json:"pli_count_delta,omitempty"`
	DeviceClass          string  `json:"device_class,omitempty"`
	LowPowerMode         bool    `json:"low_power_mode,omitempty"`
	ScreenShare          bool    `json:"screen_share,omitempty"`
}

type mediaTokenClaims struct {
	jwt.RegisteredClaims
	WorkspaceID string `json:"workspace_id"`
	CallID      string `json:"call_id"`
	Role        string `json:"role"`
	NodeID      string `json:"node_id,omitempty"`
	Region      string `json:"region,omitempty"`
}

// IssueTurnCredentials returns TURN servers and credentials scoped to an active call participant.
// When TURN is not configured (no TURNURLs), falls back to a public STUN response so FE clients
// can still complete direct/LAN paths. Access check runs FIRST so non-participants cannot probe.
func (s *Service) IssueTurnCredentials(ctx context.Context, workspaceID, callID, userID uuid.UUID) (*TurnCredentials, error) {
	if _, err := s.requireCallAccess(ctx, workspaceID, callID, userID); err != nil {
		return nil, err
	}
	participant, err := s.calls.GetParticipant(ctx, callID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.Forbidden("join the call before fetching turn credentials")
		}
		return nil, cerrors.Internal("failed to get participant", err)
	}
	if participant.Status != entity.ParticipantStatusConnected {
		return nil, cerrors.Forbidden("participant is not connected")
	}

	// STUN-only fallback when TURN is not configured. BE includes the google STUN
	// URL as urls[0] so FE doesn't need a second hardcoded fallback in this branch.
	// Username/Credential are EMPTY STRINGS (not null) — the FE Zod schema requires
	// non-optional strings; omitting would surface as a parse error.
	if len(s.media.TURNURLs) == 0 {
		return &TurnCredentials{
			URLs:       []string{"stun:stun.l.google.com:19302"},
			Username:   "",
			Credential: "",
			TTL:        300,
		}, nil
	}

	// Partial-misconfig: URLs set but neither static creds nor HMAC secret.
	// Surface 500 (permanent misconfig) rather than silently returning TURN URLs
	// with empty creds — browsers would reject the config without a clear error.
	if s.media.TURNSecret == "" && (s.media.TURNUsername == "" || s.media.TURNCredential == "") {
		return nil, cerrors.Internal("turn service partially configured: URLs set but credentials missing — set TURN_USERNAME+TURN_CREDENTIAL or TURN_SECRET", nil)
	}

	ttl := s.media.TURNCredentialsTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if ttl > 30*time.Minute {
		ttl = 30 * time.Minute
	}

	return &TurnCredentials{
		URLs:       append([]string(nil), s.media.TURNURLs...),
		Username:   s.media.TURNUsername,
		Credential: s.media.TURNCredential,
		TTL:        int(ttl.Seconds()),
	}, nil
}

// IssueMediaJoinToken returns a short-lived token scoped to one call/user/role.
func (s *Service) IssueMediaJoinToken(ctx context.Context, workspaceID, callID, userID uuid.UUID) (*MediaJoinToken, error) {
	if len(s.media.TokenSecret) == 0 {
		return nil, cerrors.Unavailable("media token signing is not configured")
	}
	call, err := s.requireCallAccess(ctx, workspaceID, callID, userID)
	if err != nil {
		return nil, err
	}

	participant, err := s.calls.GetParticipant(ctx, callID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.Forbidden("join the call before joining media")
		}
		return nil, cerrors.Internal("failed to get participant", err)
	}
	if participant.Status != entity.ParticipantStatusConnected {
		return nil, cerrors.Forbidden("participant is not connected")
	}

	role := mediaRolePresenter
	if participant.Role == entity.CallRoleViewer {
		role = mediaRoleViewer
	}
	placement, err := s.ensureMediaPlacement(ctx, call)
	if err != nil {
		return nil, err
	}
	if s.control != nil {
		if resolved, resolveErr := s.control.ResolveParticipantPlacement(ctx, call, participant, s.control.LocalNodeID()); resolveErr != nil {
			return nil, resolveErr
		} else if resolved != nil {
			placement = resolved
		}
	}

	ttl := s.media.TokenTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	now := time.Now().UTC()
	expiresAt := now.Add(ttl)
	claims := mediaTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        uuid.NewString(),
		},
		WorkspaceID: workspaceID.String(),
		CallID:      callID.String(),
		Role:        role,
	}
	if placement != nil {
		claims.NodeID = placement.NodeID
		claims.Region = placement.Region
	}

	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.media.TokenSecret)
	if err != nil {
		return nil, cerrors.Internal("failed to sign media token", err)
	}

	result := &MediaJoinToken{Token: token, Role: role, ExpiresAt: expiresAt}
	if placement != nil {
		result.NodeID = placement.NodeID
		result.Region = placement.Region
		result.ControlURL = placement.ControlURL
		result.MediaURL = placement.MediaURL
		result.RoutingMode = placement.RoutingMode
		result.FanoutStrategy = placement.FanoutStrategy
		result.OverflowPolicy = placement.OverflowPolicy
		result.ScreenSharePriority = placement.ScreenSharePriority
		result.TURNStrategy = placement.TURNStrategy
	}
	return result, nil
}

// HandleMediaOffer creates a role-scoped SFU peer and returns the server answer.
func (s *Service) HandleMediaOffer(ctx context.Context, input MediaOfferInput) (*MediaAnswer, error) {
	if input.SDP == "" {
		return nil, cerrors.InvalidInput("sdp is required")
	}
	token, err := s.validateMediaToken(input.Token)
	if err != nil {
		return nil, err
	}
	if input.CallID != uuid.Nil && input.CallID != token.callID {
		return nil, cerrors.Forbidden("media token is not valid for this call")
	}
	if s.sfu == nil {
		return nil, cerrors.Unavailable("media server is not available")
	}
	call, err := s.requireCallAccess(ctx, token.workspaceID, token.callID, token.userID)
	if err != nil {
		return nil, err
	}
	if call.Status == entity.CallStatusEnded {
		return nil, cerrors.Forbidden("call has already ended")
	}
	if err := s.ensureLocalMediaPlacement(ctx, call, token.nodeID); err != nil {
		return nil, err
	}

	pc, err := s.sfu.NewPeerConnection()
	if err != nil {
		return nil, cerrors.Internal("failed to create peer connection", err)
	}
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		s.publishICECandidate(context.Background(), call.WorkspaceID, call.ID, token.userID, candidate)
	})

	room, err := s.ensureMediaRoom(ctx, call)
	if err != nil {
		if closeErr := pc.Close(); closeErr != nil {
			slog.WarnContext(ctx, "failed to close peer connection after media room error", "call_id", call.ID, "user_id", token.userID, "error", closeErr)
		}
		return nil, err
	}
	room.RemovePeer(token.userID.String())

	if token.role == mediaRoleViewer {
		if _, err := room.AddViewer(token.userID.String(), pc); err != nil {
			if closeErr := pc.Close(); closeErr != nil {
				slog.WarnContext(ctx, "failed to close viewer peer connection after room add error", "call_id", call.ID, "user_id", token.userID, "error", closeErr)
			}
			return nil, cerrors.Conflict(fmt.Sprintf("failed to add viewer peer: %s", err.Error()))
		}
	} else {
		if _, err := room.AddPresenter(token.userID.String(), pc); err != nil {
			if closeErr := pc.Close(); closeErr != nil {
				slog.WarnContext(ctx, "failed to close presenter peer connection after room add error", "call_id", call.ID, "user_id", token.userID, "error", closeErr)
			}
			return nil, cerrors.Conflict(fmt.Sprintf("failed to add presenter peer: %s", err.Error()))
		}
	}

	offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: input.SDP}
	if err := pc.SetRemoteDescription(offer); err != nil {
		room.RemovePeer(token.userID.String())
		return nil, cerrors.InvalidInput("invalid remote offer")
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		room.RemovePeer(token.userID.String())
		return nil, cerrors.Internal("failed to create answer", err)
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		room.RemovePeer(token.userID.String())
		return nil, cerrors.Internal("failed to set local description", err)
	}

	return &MediaAnswer{SDP: answer.SDP, Type: answer.Type.String()}, nil
}

// AddMediaICECandidate adds a trickled remote ICE candidate to the user's SFU peer.
func (s *Service) AddMediaICECandidate(ctx context.Context, input MediaICECandidateInput) error {
	token, err := s.validateMediaToken(input.Token)
	if err != nil {
		return err
	}
	if input.CallID != uuid.Nil && input.CallID != token.callID {
		return cerrors.Forbidden("media token is not valid for this call")
	}
	if s.sfu == nil {
		return cerrors.Unavailable("media server is not available")
	}
	call, err := s.requireCallAccess(ctx, token.workspaceID, token.callID, token.userID)
	if err != nil {
		return err
	}
	if err := s.ensureLocalMediaPlacement(ctx, call, token.nodeID); err != nil {
		return err
	}
	if input.Candidate == "" {
		return cerrors.InvalidInput("candidate is required")
	}
	room, ok := s.sfu.GetRoom(token.callID.String())
	if !ok {
		return cerrors.NotFound("media room not found")
	}
	peer, _, ok := room.Peer(token.userID.String())
	if !ok {
		return cerrors.NotFound("media peer not found")
	}

	init := webrtc.ICECandidateInit{
		Candidate:     input.Candidate,
		SDPMid:        &input.SDPMid,
		SDPMLineIndex: intPtrToUint16Ptr(input.SDPMLineIndex),
	}
	if input.SDPMid == "" {
		init.SDPMid = nil
	}
	if err := peer.PC.AddICECandidate(init); err != nil {
		return cerrors.InvalidInput("invalid ICE candidate")
	}
	return nil
}

// RestartMediaICE creates an ICE restart answer for an existing media peer.
func (s *Service) RestartMediaICE(ctx context.Context, input MediaOfferInput) (*MediaAnswer, error) {
	token, err := s.validateMediaToken(input.Token)
	if err != nil {
		return nil, err
	}
	if input.CallID != uuid.Nil && input.CallID != token.callID {
		return nil, cerrors.Forbidden("media token is not valid for this call")
	}
	if s.sfu == nil {
		return nil, cerrors.Unavailable("media server is not available")
	}
	call, err := s.requireCallAccess(ctx, token.workspaceID, token.callID, token.userID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureLocalMediaPlacement(ctx, call, token.nodeID); err != nil {
		return nil, err
	}
	room, ok := s.sfu.GetRoom(token.callID.String())
	if !ok {
		return nil, cerrors.NotFound("media room not found")
	}
	peer, _, ok := room.Peer(token.userID.String())
	if !ok {
		return nil, cerrors.NotFound("media peer not found")
	}
	if input.SDP != "" {
		offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: input.SDP}
		if err := peer.PC.SetRemoteDescription(offer); err != nil {
			return nil, cerrors.InvalidInput("invalid restart offer")
		}
	}
	answer, err := peer.PC.CreateAnswer(nil)
	if err != nil {
		return nil, cerrors.Internal("failed to create ICE restart answer", err)
	}
	if err := peer.PC.SetLocalDescription(answer); err != nil {
		return nil, cerrors.Internal("failed to set ICE restart answer", err)
	}
	return &MediaAnswer{SDP: answer.SDP, Type: answer.Type.String()}, nil
}

func (s *Service) ReportNetworkQuality(ctx context.Context, workspaceID, callID, userID uuid.UUID, input MediaQualityReportInput) (*sfu.AdaptiveDecision, error) {
	if err := validateQualityReport(input); err != nil {
		return nil, err
	}
	if s.sfu == nil {
		return nil, cerrors.Unavailable("media server is not available")
	}
	call, err := s.requireCallAccess(ctx, workspaceID, callID, userID)
	if err != nil {
		return nil, err
	}
	if call.Status == entity.CallStatusEnded {
		return nil, cerrors.Forbidden("call has already ended")
	}
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

	room, ok := s.sfu.GetRoom(callID.String())
	if !ok {
		return nil, cerrors.NotFound("media room not found")
	}

	decision, err := room.PlanSubscriberAdaptation(sfu.NetworkSample{
		UserID:               userID.String(),
		StreamID:             input.StreamID,
		AvailableBitrateKbps: input.AvailableBitrateKbps,
		ObservedBitrateKbps:  input.ObservedBitrateKbps,
		PacketLossPct:        input.PacketLossPct,
		RoundTripTimeMs:      input.RoundTripTimeMs,
		JitterMs:             input.JitterMs,
		AudioPacketLossPct:   input.AudioPacketLossPct,
		AudioJitterMs:        input.AudioJitterMs,
		FramesPerSecond:      input.FramesPerSecond,
		DroppedFramesPct:     input.DroppedFramesPct,
		DecodeTimeMs:         input.DecodeTimeMs,
		FreezeCountDelta:     input.FreezeCountDelta,
		NACKCountDelta:       input.NACKCountDelta,
		PLICountDelta:        input.PLICountDelta,
		DeviceClass:          sfu.DeviceClass(input.DeviceClass),
		LowPowerMode:         input.LowPowerMode,
		ScreenShare:          input.ScreenShare,
		Timestamp:            time.Now().UTC(),
	})
	if err != nil {
		if isMissingMediaTarget(err) {
			decision = fallbackAdaptiveDecision(userID, input)
		} else {
			return &decision, cerrors.Internal("failed to adapt media quality", err)
		}
	}
	if s.control != nil {
		policy, err := s.control.GetCallQualityPolicy(ctx, workspaceID, callID)
		if err != nil {
			return nil, err
		}
		decision = applyRuntimeQualityPolicy(decision, policy)
		if err := s.control.RecordQualitySnapshot(ctx, clientQualitySnapshot(call, participant, input, decision)); err != nil {
			return nil, err
		}
	}
	if err := room.ApplyAdaptiveDecision(decision); err != nil && !isMissingMediaTarget(err) {
		return &decision, cerrors.Internal("failed to apply media quality decision", err)
	}
	if decision.Changed || decision.AudioPriority || decision.VideoSuspended {
		s.publishQualityDecision(ctx, call, userID, decision, "client_report")
	}
	return &decision, nil
}

func (s *Service) ensureMediaRoom(ctx context.Context, call *entity.Call) (*sfu.Room, error) {
	if s.sfu == nil {
		return nil, cerrors.Unavailable("media server is not available")
	}
	if err := s.ensureLocalMediaPlacement(ctx, call, ""); err != nil {
		return nil, err
	}
	if room, ok := s.sfu.GetRoom(call.ID.String()); ok {
		return room, nil
	}
	room, err := s.sfu.CreateRoom(call.ID.String(), s.roomOptions(call))
	if err != nil {
		slog.ErrorContext(ctx, "failed to create media room", "call_id", call.ID, "error", err)
		return nil, cerrors.Internal("failed to create media room", err)
	}
	return room, nil
}

func (s *Service) roomOptions(call *entity.Call) sfu.RoomOptions {
	policy := s.callPolicy(call)
	maxPresenters := policy.MaxPresenters
	maxViewers := policy.MaxViewers
	if maxPresenters <= 0 {
		maxPresenters = sfu.DefaultMaxPresenters
	}
	if s.media.MaxPresentersPerCall > 0 && maxPresenters > s.media.MaxPresentersPerCall {
		maxPresenters = s.media.MaxPresentersPerCall
	}
	if s.media.MaxViewersPerCall > 0 && (maxViewers == 0 || maxViewers > s.media.MaxViewersPerCall) {
		maxViewers = s.media.MaxViewersPerCall
	}
	return sfu.RoomOptions{
		MaxPresenters:         maxPresenters,
		MaxViewers:            maxViewers,
		MaxTracksPerPresenter: s.media.MaxTracksPerPresenter,
		Recording:             call.Settings.Recording,
		Simulcast:             true,
		Adaptive:              s.media.Adaptive,
	}
}

func validateQualityReport(input MediaQualityReportInput) error {
	if input.StreamID == "" {
		return cerrors.InvalidInput("stream_id is required")
	}
	if input.AvailableBitrateKbps < 0 || input.ObservedBitrateKbps < 0 {
		return cerrors.InvalidInput("bitrate values cannot be negative")
	}
	if input.RoundTripTimeMs < 0 {
		return cerrors.InvalidInput("round_trip_time_ms cannot be negative")
	}
	for name, value := range map[string]float64{
		"packet_loss_pct":       input.PacketLossPct,
		"audio_packet_loss_pct": input.AudioPacketLossPct,
		"dropped_frames_pct":    input.DroppedFramesPct,
	} {
		if value < 0 || value > 100 {
			return cerrors.InvalidInput(fmt.Sprintf("%s must be between 0 and 100", name))
		}
	}
	if input.JitterMs < 0 || input.AudioJitterMs < 0 || input.FramesPerSecond < 0 || input.DecodeTimeMs < 0 {
		return cerrors.InvalidInput("timing metrics cannot be negative")
	}
	if input.FreezeCountDelta < 0 || input.NACKCountDelta < 0 || input.PLICountDelta < 0 {
		return cerrors.InvalidInput("counter deltas cannot be negative")
	}
	return nil
}

type validatedMediaToken struct {
	workspaceID uuid.UUID
	callID      uuid.UUID
	userID      uuid.UUID
	role        string
	nodeID      string
}

func (s *Service) validateMediaToken(tokenString string) (*validatedMediaToken, error) {
	if tokenString == "" {
		return nil, cerrors.Unauthorized("media token is required")
	}
	if len(s.media.TokenSecret) == 0 {
		return nil, cerrors.Unavailable("media token signing is not configured")
	}
	claims := &mediaTokenClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.media.TokenSecret, nil
	})
	if err != nil || token == nil || !token.Valid {
		return nil, cerrors.Unauthorized("invalid media token")
	}

	workspaceID, err := uuid.Parse(claims.WorkspaceID)
	if err != nil {
		return nil, cerrors.Unauthorized("invalid media token")
	}
	callID, err := uuid.Parse(claims.CallID)
	if err != nil {
		return nil, cerrors.Unauthorized("invalid media token")
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return nil, cerrors.Unauthorized("invalid media token")
	}
	if claims.Role != mediaRolePresenter && claims.Role != mediaRoleViewer {
		return nil, cerrors.Unauthorized("invalid media token")
	}

	return &validatedMediaToken{
		workspaceID: workspaceID,
		callID:      callID,
		userID:      userID,
		role:        claims.Role,
		nodeID:      claims.NodeID,
	}, nil
}

func (s *Service) ensureMediaPlacement(ctx context.Context, call *entity.Call) (*entity.MediaRoomPlacement, error) {
	if call == nil || s.control == nil {
		return nil, nil
	}
	return s.control.EnsurePlacement(ctx, call, s.roomOptions(call))
}

func (s *Service) ensureLocalMediaPlacement(ctx context.Context, call *entity.Call, tokenNodeID string) error {
	if s.control == nil || call == nil {
		return nil
	}
	if tokenNodeID != "" && !s.control.IsLocalNode(tokenNodeID) {
		return cerrors.Unavailable("media session is assigned to a different media edge")
	}
	allowed, err := s.control.CanServeNode(ctx, call, s.control.LocalNodeID())
	if err != nil {
		return err
	}
	if !allowed {
		placement, placementErr := s.ensureMediaPlacement(ctx, call)
		if placementErr != nil {
			return placementErr
		}
		if placement != nil {
			return cerrors.Unavailable(fmt.Sprintf("media session is assigned to %s", placement.ControlURL))
		}
		return cerrors.Unavailable("media session is assigned to a different media edge")
	}
	return nil
}

func (s *Service) shouldServePlacementLocally(placement *entity.MediaRoomPlacement) bool {
	if placement == nil || s.control == nil {
		return true
	}
	return s.control.IsLocalNode(placement.NodeID)
}

func (s *Service) publishICECandidate(ctx context.Context, workspaceID, callID, userID uuid.UUID, candidate *webrtc.ICECandidate) {
	candidateJSON := candidate.ToJSON()
	var lineIndex *int
	if candidateJSON.SDPMLineIndex != nil {
		n := int(*candidateJSON.SDPMLineIndex)
		lineIndex = &n
	}
	mid := ""
	if candidateJSON.SDPMid != nil {
		mid = *candidateJSON.SDPMid
	}
	payload := event.SignalPayload{
		CallID:        callID,
		FromUser:      uuid.Nil,
		ToUser:        userID,
		Candidate:     candidateJSON.Candidate,
		SDPMid:        mid,
		SDPMLineIndex: lineIndex,
	}
	subject := fmt.Sprintf("aloqa.signal.%s", userID)
	s.doPublish(ctx, event.TypeSignalCandidate, subject, workspaceID, uuid.Nil, uuid.Nil, payload)
}

func (s *Service) publishQualityDecision(ctx context.Context, call *entity.Call, userID uuid.UUID, decision sfu.AdaptiveDecision, source string) {
	subject := fmt.Sprintf("aloqa.signal.%s", userID)
	s.doPublish(ctx, event.TypeCallQualityAdapted, subject, call.WorkspaceID, uuid.Nil, userID, event.CallQualityPayload{
		CallID:              call.ID,
		UserID:              userID,
		StreamID:            decision.StreamID,
		Source:              source,
		PreviousQuality:     string(decision.PreviousQuality),
		TargetQuality:       string(decision.TargetQuality),
		NetworkGrade:        string(decision.NetworkGrade),
		AudioPriority:       decision.AudioPriority,
		VideoSuspended:      decision.VideoSuspended,
		SyncMode:            decision.SyncMode,
		VideoDegradeMode:    decision.VideoDegradeMode,
		MaxVideoBitrateKbps: decision.MaxVideoBitrateKbps,
		MaxVideoFPS:         decision.MaxVideoFPS,
		TargetAudioBufferMs: decision.TargetAudioBufferMs,
		TargetVideoBufferMs: decision.TargetVideoBufferMs,
		LipSyncWindowMs:     decision.LipSyncWindowMs,
		Reasons:             decision.Reasons,
	})
}

func intPtrToUint16Ptr(v *int) *uint16 {
	if v == nil {
		return nil
	}
	if *v < 0 || *v > 65535 {
		return nil
	}
	n := uint16(*v)
	return &n
}

func applyRuntimeQualityPolicy(decision sfu.AdaptiveDecision, policy *entity.MediaQualityPolicy) sfu.AdaptiveDecision {
	if policy == nil {
		return decision
	}
	if !policy.MeetingWideDowngrade && policy.Mode == entity.MediaQualityPolicyAuto {
		return decision
	}
	switch policy.Mode {
	case entity.MediaQualityPolicyConserveBandwidth:
		if decision.TargetQuality == sfu.QualityHigh {
			decision.TargetQuality = sfu.QualityMedium
		}
		if decision.MaxVideoFPS > 20 {
			decision.MaxVideoFPS = 20
		}
		if decision.MaxVideoBitrateKbps > 900 {
			decision.MaxVideoBitrateKbps = 900
		}
		decision.Reasons = append(decision.Reasons, "meeting-wide conserve-bandwidth policy applied")
	case entity.MediaQualityPolicyForceLow:
		decision.TargetQuality = sfu.QualityLow
		decision.MaxVideoFPS = 12
		if decision.MaxVideoBitrateKbps > 250 {
			decision.MaxVideoBitrateKbps = 250
		}
		decision.Reasons = append(decision.Reasons, "meeting-wide force-low policy applied")
	case entity.MediaQualityPolicyAudioOnly:
		decision.TargetQuality = sfu.QualityLow
		decision.VideoSuspended = true
		decision.MaxVideoFPS = 0
		decision.MaxVideoBitrateKbps = 0
		decision.TargetVideoBufferMs = 0
		decision.VideoDegradeMode = "suspend_video_until_audio_recovers"
		decision.Reasons = append(decision.Reasons, "meeting-wide audio-only policy applied")
	}
	decision.Changed = decision.TargetQuality != decision.PreviousQuality
	return decision
}

func clientQualitySnapshot(call *entity.Call, participant *entity.CallParticipant, input MediaQualityReportInput, decision sfu.AdaptiveDecision) entity.MediaQoSSample {
	mediaKind := "video"
	if input.ScreenShare {
		mediaKind = "screen"
	}
	return entity.MediaQoSSample{
		ID:                           uuid.New(),
		WorkspaceID:                  call.WorkspaceID,
		CallID:                       call.ID,
		UserID:                       participant.UserID,
		StreamID:                     input.StreamID,
		Source:                       entity.MediaTelemetrySourceClient,
		ParticipantRole:              string(participant.Role),
		MediaKind:                    mediaKind,
		PacketLossPct:                input.PacketLossPct,
		JitterMs:                     input.JitterMs,
		RoundTripTimeMs:              float64(input.RoundTripTimeMs),
		AvailableOutgoingBitrateKbps: 0,
		AvailableIncomingBitrateKbps: input.AvailableBitrateKbps,
		Metadata: map[string]any{
			"audio_packet_loss_pct":   input.AudioPacketLossPct,
			"audio_jitter_ms":         input.AudioJitterMs,
			"observed_bitrate_kbps":   input.ObservedBitrateKbps,
			"frames_per_second":       input.FramesPerSecond,
			"dropped_frames_pct":      input.DroppedFramesPct,
			"decode_time_ms":          input.DecodeTimeMs,
			"freeze_count_delta":      input.FreezeCountDelta,
			"nack_count_delta":        input.NACKCountDelta,
			"pli_count_delta":         input.PLICountDelta,
			"device_class":            input.DeviceClass,
			"low_power_mode":          input.LowPowerMode,
			"decision_target_quality": string(decision.TargetQuality),
			"decision_network_grade":  string(decision.NetworkGrade),
		},
		SampledAt: time.Now().UTC(),
	}
}

func isMissingMediaTarget(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found")
}

func fallbackAdaptiveDecision(userID uuid.UUID, input MediaQualityReportInput) sfu.AdaptiveDecision {
	grade := sfu.NetworkGradeGood
	maxFPS := 30
	maxBitrate := input.AvailableBitrateKbps
	if maxBitrate <= 0 {
		maxBitrate = input.ObservedBitrateKbps
	}
	targetQuality := sfu.QualityMedium
	switch {
	case input.PacketLossPct >= 10 || input.RoundTripTimeMs >= 700 || input.JitterMs >= 120:
		targetQuality = sfu.QualityLow
		grade = sfu.NetworkGradeCritical
		maxFPS = 12
		if maxBitrate <= 0 || maxBitrate > 250 {
			maxBitrate = 250
		}
	case input.PacketLossPct >= 4 || input.RoundTripTimeMs >= 300 || input.JitterMs >= 60:
		targetQuality = sfu.QualityLow
		grade = sfu.NetworkGradePoor
		maxFPS = 15
		if maxBitrate <= 0 || maxBitrate > 400 {
			maxBitrate = 400
		}
	default:
		if maxBitrate <= 0 {
			maxBitrate = 900
		}
	}
	return sfu.AdaptiveDecision{
		UserID:                 userID.String(),
		StreamID:               input.StreamID,
		PreviousQuality:        targetQuality,
		TargetQuality:          targetQuality,
		NetworkGrade:           grade,
		Changed:                false,
		SyncMode:               "audio_clock_master",
		VideoDegradeMode:       "awaiting_server_track_mapping",
		MaxVideoBitrateKbps:    maxBitrate,
		MaxVideoFPS:            maxFPS,
		TargetAudioBufferMs:    60,
		TargetVideoBufferMs:    100,
		LipSyncWindowMs:        100,
		EstimatedBandwidthKbps: maxBitrate,
		Reasons:                []string{"fallback adaptive decision pending server-side track mapping"},
		DecidedAt:              time.Now().UTC(),
	}
}
