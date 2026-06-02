package call

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/media/sfu"
	"aloqa/internal/pkg/cerrors"
)

type TurnCredentials struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
	TTL        int      `json:"ttl"`
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

// computeHmacCredential is the coturn `--use-auth-secret` scheme: HMAC-SHA1 of
// the timestamp:userID username, encoded as base64. Coturn validates the credential
// server-side using the same shared secret.
func computeHmacCredential(secret, username string) string {
	h := hmac.New(sha1.New, []byte(secret))
	h.Write([]byte(username))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// generateTurnHmacUsername builds a TTL-bound coturn username carrying the
// userID scope for operational log attribution.
func generateTurnHmacUsername(ttl time.Duration, userID uuid.UUID) string {
	expiry := time.Now().Add(ttl).Unix()
	return fmt.Sprintf("%d:%s", expiry, userID.String())
}

// stunFallbackURLs returns the operator-configured STUN servers when set,
// otherwise the public google STUN endpoint. Lets LAN / air-gapped deploys
// (WEBRTC_STUN_SERVERS) avoid an external Google dependency they explicitly
// overrode for SFU ICE.
func stunFallbackURLs(configured []string) []string {
	if len(configured) > 0 {
		return append([]string(nil), configured...)
	}
	return []string{"stun:stun.l.google.com:19302"}
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

	// TURN HMAC branch — when a shared secret is configured, issue short-lived
	// per-participant creds against coturn's --use-auth-secret mode.
	if s.media.TURNSecret != "" && len(s.media.TURNURLs) > 0 {
		ttl := s.media.TURNCredentialsTTL
		if ttl <= 0 || ttl > 10*time.Minute {
			ttl = 10 * time.Minute
		}
		username := generateTurnHmacUsername(ttl, userID)
		return &TurnCredentials{
			URLs:       append([]string(nil), s.media.TURNURLs...),
			Username:   username,
			Credential: computeHmacCredential(s.media.TURNSecret, username),
			TTL:        int(ttl.Seconds()),
		}, nil
	}

	// STUN-only fallback when TURN is not configured. BE includes the STUN
	// URL(s) as urls[0..] so FE doesn't need a second hardcoded fallback in this
	// branch. Honors WEBRTC_STUN_SERVERS so LAN deploys don't get a Google
	// dependency. Username/Credential are EMPTY STRINGS (not null) — the FE Zod
	// schema requires non-optional strings; omitting would surface as a parse error.
	if len(s.media.TURNURLs) == 0 {
		return &TurnCredentials{
			URLs:       stunFallbackURLs(s.media.STUNServers),
			Username:   "",
			Credential: "",
			TTL:        300,
		}, nil
	}

	// Partial-misconfig: URLs set but neither static creds nor HMAC secret.
	// Surface 500 (permanent misconfig) rather than silently returning TURN URLs
	// with empty creds — browsers would reject the config without a clear error.
	if s.media.TURNSecret == "" && (s.media.TURNUsername == "" || s.media.TURNCredential == "") {
		return nil, cerrors.Internal("turn service partially configured: WEBRTC_TURN_SERVER set but credentials missing — set WEBRTC_TURN_USERNAME+WEBRTC_TURN_PASSWORD or WEBRTC_TURN_SECRET", nil)
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

func (s *Service) ensureMediaPlacement(ctx context.Context, call *entity.Call) (*entity.MediaRoomPlacement, error) {
	if call == nil || s.control == nil {
		return nil, nil
	}
	return s.control.EnsurePlacement(ctx, call, s.roomOptions(call))
}

func (s *Service) shouldServePlacementLocally(placement *entity.MediaRoomPlacement) bool {
	if placement == nil || s.control == nil {
		return true
	}
	return s.control.IsLocalNode(placement.NodeID)
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
