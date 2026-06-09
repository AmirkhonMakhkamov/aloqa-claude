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

// publishShareEvent broadcasts a custom-payload screen-share event through the
// call-scoped fanout, preserving private channel-less call visibility.
func (s *Service) publishShareEvent(ctx context.Context, evtType event.Type, call *entity.Call, actorID uuid.UUID, payload any) {
	channelID := uuid.Nil
	if call.ChannelID != nil {
		channelID = *call.ChannelID
	}
	s.publishCallScoped(ctx, evtType, call, channelID, actorID, payload)
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
