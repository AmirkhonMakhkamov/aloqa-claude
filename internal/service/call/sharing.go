package call

import (
	"context"

	"github.com/google/uuid"
	livekitpb "github.com/livekit/protocol/livekit"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/pkg/cerrors"
)

// permissionForParticipant builds the full LiveKit ParticipantPermission from the
// participant's role, the meeting-level member-permission policy, and their screen
// grant. UpdateParticipant REPLACES the permission, so every field is set — which
// means a screen grant/revoke MUST rebuild from the same resolver, otherwise it
// would reset the member's mic/camera policy (ALK-812). An empty allowed source
// set clears CanPublish (LiveKit reads an empty CanPublishSources as no
// restriction). Host/co-host publish everything; viewers nothing. (ALK-697 + ALK-812)
func permissionForParticipant(role entity.CallRole, settings entity.CallSettings, hasScreenGrant bool) *livekitpb.ParticipantPermission {
	canMic, canCam, canShare, canPublish := resolveMemberPublish(role, settings, hasScreenGrant)
	if !canPublish {
		return &livekitpb.ParticipantPermission{CanSubscribe: true, CanPublish: false, CanPublishData: true}
	}
	return &livekitpb.ParticipantPermission{
		CanSubscribe:      true,
		CanPublish:        true,
		CanPublishData:    true,
		CanPublishSources: publishSources(canMic, canCam, canShare),
	}
}

// publishCallControlEvent broadcasts a custom-payload call-control event to the
// call-scoped fanout, preserving private channel-less call visibility.
func (s *Service) publishCallControlEvent(ctx context.Context, evtType event.Type, call *entity.Call, actorID uuid.UUID, payload any) {
	channelID := uuid.Nil
	if call.ChannelID != nil {
		channelID = *call.ChannelID
	}
	s.publishCallScoped(ctx, evtType, call, channelID, actorID, payload)
}

func (s *Service) publishShareEvent(ctx context.Context, evtType event.Type, call *entity.Call, actorID uuid.UUID, payload any) {
	s.publishCallControlEvent(ctx, evtType, call, actorID, payload)
}

func callPinsParticipant(call *entity.Call, userID uuid.UUID) bool {
	return call != nil && call.PinnedParticipantUserID != nil && *call.PinnedParticipantUserID == userID
}

func (s *Service) clearPinnedParticipantIfMatches(ctx context.Context, call *entity.Call, targetUserID, actorID uuid.UUID) error {
	if !callPinsParticipant(call, targetUserID) {
		return nil
	}
	if err := s.calls.SetPinnedParticipantUserID(ctx, call.ID, nil); err != nil {
		return err
	}
	call.PinnedParticipantUserID = nil
	s.publishCallControlEvent(ctx, event.TypeCallPinnedChanged, call, actorID,
		event.PinnedParticipantPayload{CallID: call.ID, PinnedParticipantUserID: nil})
	return nil
}

// GrantScreenShare lets a host/co-host grant a participant the right to share.
// Order: apply the live LiveKit permission FIRST (the real effect), then persist,
// then broadcast. If the LiveKit call fails we do not persist/broadcast.
func (s *Service) GrantScreenShare(ctx context.Context, workspaceID, callID, actorID, targetUserID uuid.UUID) error {
	call, err := s.requireCallAccess(ctx, workspaceID, callID, actorID)
	if err != nil {
		return err
	}
	if err := s.requireHostOrCoHost(ctx, callID, actorID); err != nil {
		return err
	}
	target, err := s.calls.GetParticipant(ctx, callID, targetUserID)
	if err != nil {
		return err
	}
	if target.Role == entity.CallRoleViewer {
		return cerrors.Forbidden("viewers cannot be granted screen share")
	}
	if target.Role == entity.CallRoleHost || target.Role == entity.CallRoleCoHost {
		return nil // host/co-host already share freely (spec §6) — nothing to grant
	}
	if err := s.updateLiveKitParticipantPermission(ctx, callID, targetUserID, target.Role, call.Settings, true); err != nil {
		return err
	}
	if err := s.calls.SetCanScreenShare(ctx, target.ID, true); err != nil {
		return err
	}
	target.CanScreenShare = true
	s.publishParticipantEvent(ctx, event.TypeCallParticipantUpdated, call, target)
	s.publishShareEvent(ctx, event.TypeCallShareRequestResolved, call, actorID,
		event.ShareRequestResolvedPayload{CallID: callID, RequesterUserID: targetUserID, Approved: true})
	return nil
}

// RevokeScreenShare cuts media access FIRST (LiveKit auto-unpublishes the active
// screen track → track_unpublished reconcile sets screen_sharing=false), then persists.
func (s *Service) RevokeScreenShare(ctx context.Context, workspaceID, callID, actorID, targetUserID uuid.UUID) error {
	call, err := s.requireCallAccess(ctx, workspaceID, callID, actorID)
	if err != nil {
		return err
	}
	if err := s.requireHostOrCoHost(ctx, callID, actorID); err != nil {
		return err
	}
	target, err := s.calls.GetParticipant(ctx, callID, targetUserID)
	if err != nil {
		return err
	}
	// Never strip a host/co-host (never gated, spec §6) or a viewer (can't share).
	// Only presenter/participant grants are revocable.
	if target.Role == entity.CallRoleHost || target.Role == entity.CallRoleCoHost || target.Role == entity.CallRoleViewer {
		return nil
	}
	// Rebuild the permission from the target's role with canShare=false so the
	// participant keeps camera/mic but loses screen; LiveKit auto-unpublishes any
	// live screen track.
	if err := s.updateLiveKitParticipantPermission(ctx, callID, targetUserID, target.Role, call.Settings, false); err != nil {
		return err // do not report a revoke that didn't cut access
	}
	if err := s.calls.SetCanScreenShare(ctx, target.ID, false); err != nil {
		return err
	}
	target.CanScreenShare = false
	s.publishParticipantEvent(ctx, event.TypeCallParticipantUpdated, call, target)
	return nil
}

func (s *Service) requireHostMediaControlTarget(ctx context.Context, workspaceID, callID, actorID, targetUserID uuid.UUID) (*entity.Call, *entity.CallParticipant, error) {
	call, err := s.requireCallAccess(ctx, workspaceID, callID, actorID)
	if err != nil {
		return nil, nil, err
	}
	if call.Status == entity.CallStatusEnded {
		return nil, nil, cerrors.Forbidden("call has already ended")
	}
	if err := s.requireHostOrCoHost(ctx, callID, actorID); err != nil {
		return nil, nil, err
	}
	if actorID == targetUserID {
		return nil, nil, cerrors.InvalidInput("use local media controls for yourself")
	}
	target, err := s.calls.GetParticipant(ctx, callID, targetUserID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, nil, cerrors.NotFound("target participant not found")
		}
		return nil, nil, cerrors.Internal("failed to get target participant", err)
	}
	if target.Status != entity.ParticipantStatusConnected {
		return nil, nil, cerrors.Forbidden("target participant is not connected")
	}
	return call, target, nil
}

func liveKitRoomNameForParticipant(callID uuid.UUID, participant *entity.CallParticipant) string {
	if participant != nil && participant.BreakoutRoomID != nil {
		return breakoutLiveKitRoomName(callID, *participant.BreakoutRoomID)
	}
	return callID.String()
}

func (s *Service) muteLiveKitParticipantTracks(ctx context.Context, callID uuid.UUID, target *entity.CallParticipant, source livekitpb.TrackSource) error {
	if s.livekitRooms == nil {
		return nil
	}
	roomName := liveKitRoomNameForParticipant(callID, target)
	lkParticipants, err := s.livekitRooms.ListParticipantsByRoom(ctx, roomName)
	if err != nil {
		return cerrors.Internal("failed to list livekit participants for media control", err)
	}
	identity := target.UserID.String()
	for _, p := range lkParticipants {
		if p.GetIdentity() != identity {
			continue
		}
		for _, track := range p.GetTracks() {
			if track.GetMuted() || track.GetSource() != source {
				continue
			}
			if err := s.livekitRooms.MutePublishedTrack(ctx, roomName, identity, track.GetSid()); err != nil {
				return err
			}
		}
		return nil
	}
	return nil
}

func boolPayloadPtr(value bool) *bool {
	return &value
}

// MuteParticipant lets a host/co-host force-mute a connected participant's mic.
func (s *Service) MuteParticipant(ctx context.Context, workspaceID, callID, actorID, targetUserID uuid.UUID) error {
	call, target, err := s.requireHostMediaControlTarget(ctx, workspaceID, callID, actorID, targetUserID)
	if err != nil {
		return err
	}
	if target.AudioMuted {
		return nil
	}
	if err := s.muteLiveKitParticipantTracks(ctx, callID, target, livekitpb.TrackSource_MICROPHONE); err != nil {
		return err
	}
	if err := s.calls.UpdateParticipantMedia(ctx, target.ID, true, target.VideoMuted, target.ScreenSharing); err != nil {
		return err
	}
	target.AudioMuted = true
	s.publishParticipantEvent(ctx, event.TypeCallParticipantUpdated, call, target)
	s.publishCallControlEvent(ctx, event.TypeCallParticipantMuted, call, actorID,
		event.CallParticipantMutedPayload{CallID: callID, UserID: targetUserID, AudioMuted: boolPayloadPtr(true)})
	return nil
}

// DisableParticipantCamera lets a host/co-host force-disable a connected
// participant's camera.
func (s *Service) DisableParticipantCamera(ctx context.Context, workspaceID, callID, actorID, targetUserID uuid.UUID) error {
	call, target, err := s.requireHostMediaControlTarget(ctx, workspaceID, callID, actorID, targetUserID)
	if err != nil {
		return err
	}
	if target.VideoMuted {
		return nil
	}
	if err := s.muteLiveKitParticipantTracks(ctx, callID, target, livekitpb.TrackSource_CAMERA); err != nil {
		return err
	}
	if err := s.calls.UpdateParticipantMedia(ctx, target.ID, target.AudioMuted, true, target.ScreenSharing); err != nil {
		return err
	}
	target.VideoMuted = true
	s.publishParticipantEvent(ctx, event.TypeCallParticipantUpdated, call, target)
	s.publishCallControlEvent(ctx, event.TypeCallParticipantMuted, call, actorID,
		event.CallParticipantMutedPayload{CallID: callID, UserID: targetUserID, VideoMuted: boolPayloadPtr(true)})
	return nil
}

// AskParticipantToUnmute asks a connected participant to unmute without forcing
// media state.
func (s *Service) AskParticipantToUnmute(ctx context.Context, workspaceID, callID, actorID, targetUserID uuid.UUID) error {
	call, _, err := s.requireHostMediaControlTarget(ctx, workspaceID, callID, actorID, targetUserID)
	if err != nil {
		return err
	}
	s.publishCallControlEvent(ctx, event.TypeCallAskUnmute, call, actorID,
		event.CallAskUnmutePayload{CallID: callID, UserID: targetUserID, RequestedByUserID: actorID})
	return nil
}

// updateLiveKitParticipantPermission applies the participant's rebuilt LiveKit
// permission so a grant/revoke (or a meeting-policy flip) takes effect at the
// media plane with no rejoin. It threads the call settings + role + screen grant
// through resolveMemberPublish so the mic/camera policy is never reset by a screen
// grant/revoke (ALK-812). A no-op when LiveKit is not wired up (local dev / tests
// without a room client) so the REST + persisted grant still works.
func (s *Service) updateLiveKitParticipantPermission(ctx context.Context, callID, userID uuid.UUID, role entity.CallRole, settings entity.CallSettings, hasScreenGrant bool) error {
	if s.livekitRooms == nil {
		return nil
	}
	return s.livekitRooms.UpdateParticipant(ctx, callID.String(), userID.String(),
		permissionForParticipant(role, settings, hasScreenGrant))
}

// RequestScreenShare: a connected non-viewer without a grant asks the host. Transient (no row).
func (s *Service) RequestScreenShare(ctx context.Context, workspaceID, callID, requesterID uuid.UUID) error {
	call, err := s.requireCallAccess(ctx, workspaceID, callID, requesterID)
	if err != nil {
		return err
	}
	if call.Status == entity.CallStatusEnded {
		return cerrors.Forbidden("call has already ended")
	}
	p, err := s.calls.GetParticipant(ctx, callID, requesterID)
	if err != nil {
		return err
	}
	// Only a connected, non-viewer participant without an existing grant can request.
	if p.Status != entity.ParticipantStatusConnected {
		return cerrors.Forbidden("participant is not connected")
	}
	if p.Role == entity.CallRoleViewer || canShareScreen(*p) { // viewer can't request; already allowed → no-op
		return nil
	}
	s.publishShareEvent(ctx, event.TypeCallShareRequestCreated, call, requesterID,
		event.ShareRequestPayload{CallID: callID, RequesterUserID: requesterID})
	return nil
}

// ResolveShareRequest: host approves (→ GrantScreenShare) or denies (→ broadcast resolved{false}).
func (s *Service) ResolveShareRequest(ctx context.Context, workspaceID, callID, actorID, requesterID uuid.UUID, approved bool) error {
	if approved {
		return s.GrantScreenShare(ctx, workspaceID, callID, actorID, requesterID)
	}
	call, err := s.requireCallAccess(ctx, workspaceID, callID, actorID)
	if err != nil {
		return err
	}
	if err := s.requireHostOrCoHost(ctx, callID, actorID); err != nil {
		return err
	}
	s.publishShareEvent(ctx, event.TypeCallShareRequestResolved, call, actorID,
		event.ShareRequestResolvedPayload{CallID: callID, RequesterUserID: requesterID, Approved: false})
	return nil
}

// SetFeaturedShare: host features one participant's share for everyone (nil clears).
func (s *Service) SetFeaturedShare(ctx context.Context, workspaceID, callID, actorID uuid.UUID, target *uuid.UUID) error {
	call, err := s.requireCallAccess(ctx, workspaceID, callID, actorID)
	if err != nil {
		return err
	}
	if err := s.requireHostOrCoHost(ctx, callID, actorID); err != nil {
		return err
	}
	if target != nil {
		if _, err := s.calls.GetParticipant(ctx, callID, *target); err != nil {
			return cerrors.InvalidInput("featured user is not in this call")
		}
	}
	if err := s.calls.SetFeaturedShareUserID(ctx, callID, target); err != nil {
		return err
	}
	s.publishShareEvent(ctx, event.TypeCallFeaturedShareUpdated, call, actorID,
		event.FeaturedSharePayload{CallID: callID, FeaturedShareUserID: target})
	return nil
}

// SetPinnedParticipant lets a host/co-host pin one connected participant for
// everyone (nil clears the global pin).
func (s *Service) SetPinnedParticipant(ctx context.Context, workspaceID, callID, actorID uuid.UUID, target *uuid.UUID) error {
	call, err := s.requireCallAccess(ctx, workspaceID, callID, actorID)
	if err != nil {
		return err
	}
	if call.Status == entity.CallStatusEnded {
		return cerrors.Forbidden("call has already ended")
	}
	if err := s.requireHostOrCoHost(ctx, callID, actorID); err != nil {
		return err
	}
	if target != nil {
		p, err := s.calls.GetParticipant(ctx, callID, *target)
		if err != nil {
			return cerrors.InvalidInput("pinned participant is not in this call")
		}
		if p.Status != entity.ParticipantStatusConnected {
			return cerrors.InvalidInput("pinned participant is not connected")
		}
	}
	if err := s.calls.SetPinnedParticipantUserID(ctx, callID, target); err != nil {
		return err
	}
	call.PinnedParticipantUserID = target
	s.publishCallControlEvent(ctx, event.TypeCallPinnedChanged, call, actorID,
		event.PinnedParticipantPayload{CallID: callID, PinnedParticipantUserID: target})
	return nil
}
