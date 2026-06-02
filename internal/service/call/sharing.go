package call

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	livekitpb "github.com/livekit/protocol/livekit"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/pkg/cerrors"
)

// permissionForParticipant builds the full LiveKit ParticipantPermission for a
// participant. UpdateParticipant REPLACES the permission, so every field must be
// set. Viewers stay non-publishing; everyone else publishes camera+mic, plus
// screen + screen-audio when canShare is true. (ALK-697)
func permissionForParticipant(role entity.CallRole, canShare bool) *livekitpb.ParticipantPermission {
	if role == entity.CallRoleViewer {
		return &livekitpb.ParticipantPermission{CanSubscribe: true, CanPublish: false, CanPublishData: true}
	}
	return &livekitpb.ParticipantPermission{
		CanSubscribe:      true,
		CanPublish:        true,
		CanPublishData:    true,
		CanPublishSources: screenShareSources(canShare),
	}
}

// publishShareEvent broadcasts a custom-payload screen-share event to the
// workspace WS subject (mirrors publishParticipantEvent's channelID nil-guard).
func (s *Service) publishShareEvent(ctx context.Context, evtType event.Type, call *entity.Call, actorID uuid.UUID, payload any) {
	channelID := uuid.Nil
	if call.ChannelID != nil {
		channelID = *call.ChannelID
	}
	s.doPublish(ctx, evtType, fmt.Sprintf("aloqa.ws.%s", call.WorkspaceID), call.WorkspaceID, channelID, actorID, payload)
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
	if err := s.updateLiveKitParticipantPermission(ctx, callID, targetUserID, target.Role, true); err != nil {
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
	if err := s.updateLiveKitParticipantPermission(ctx, callID, targetUserID, target.Role, false); err != nil {
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
// permission so a grant/revoke takes effect at the media plane with no rejoin.
// A no-op when LiveKit is not wired up (e.g. local dev / tests without a room
// client) so the REST + persisted grant still works.
func (s *Service) updateLiveKitParticipantPermission(ctx context.Context, callID, userID uuid.UUID, role entity.CallRole, canShare bool) error {
	if s.livekitRooms == nil {
		return nil
	}
	return s.livekitRooms.UpdateParticipant(ctx, callID.String(), userID.String(),
		permissionForParticipant(role, canShare))
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
