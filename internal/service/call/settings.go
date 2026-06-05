package call

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	livekitpb "github.com/livekit/protocol/livekit"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/pkg/cerrors"
)

// CallSettingsPatch describes a partial call settings update. Pointer fields
// distinguish "absent" from an explicit zero value. EntryMode is the single
// source of truth for the lobby; the legacy WaitingRoom flag is derived from it
// (S3 / ALK-821). The member-permission policy fields (ScreenSharing, Chat,
// MembersCanUnmuteMic, MembersCanEnableCamera) are owned by S4 / ALK-812.
type CallSettingsPatch struct {
	EntryMode              *entity.EntryMode
	MuteOnJoin             *bool
	BreakoutRooms          *bool
	ScreenSharing          *bool
	Chat                   *bool
	MembersCanUnmuteMic    *bool
	MembersCanEnableCamera *bool
}

// UpdateCallSettings applies host-controlled partial settings updates to a
// live call and broadcasts the resulting settings to connected clients.
func (s *Service) UpdateCallSettings(ctx context.Context, callID, actorID uuid.UUID, patch CallSettingsPatch) (*entity.Call, error) {
	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		return nil, s.wrapCallError(ctx, err, callID, "update call settings")
	}

	if err := s.requireHostOrCoHost(ctx, callID, actorID); err != nil {
		return nil, err
	}

	if call.Status != entity.CallStatusActive {
		return nil, cerrors.Forbidden("call is not active")
	}

	// No-op when the patch carries no fields — avoid a spurious settings write
	// and broadcast.
	if patch.EntryMode == nil && patch.MuteOnJoin == nil && patch.BreakoutRooms == nil &&
		patch.ScreenSharing == nil && patch.Chat == nil &&
		patch.MembersCanUnmuteMic == nil && patch.MembersCanEnableCamera == nil {
		return call, nil
	}

	// settings is the in-memory TARGET — for a restrictive publish-affecting flip
	// it is enforced at the media plane BEFORE it is persisted (see below), so a
	// failed enforcement never leaves the stored policy tighter than reality.
	settings := call.Settings

	if patch.MuteOnJoin != nil {
		settings.MuteOnJoin = *patch.MuteOnJoin
	}

	// Member-permission policy fields (ALK-812). Booleans are always valid; the
	// publish-affecting ones (screen_sharing / members_can_*) drive the media-plane
	// enforcement after the patch is assembled.
	if patch.ScreenSharing != nil {
		settings.ScreenSharing = *patch.ScreenSharing
	}
	if patch.Chat != nil {
		settings.Chat = *patch.Chat
	}
	if patch.MembersCanUnmuteMic != nil {
		settings.MembersCanUnmuteMic = patch.MembersCanUnmuteMic
	}
	if patch.MembersCanEnableCamera != nil {
		settings.MembersCanEnableCamera = patch.MembersCanEnableCamera
	}

	if patch.EntryMode != nil {
		mode := *patch.EntryMode
		if !mode.Valid() {
			return nil, cerrors.InvalidInput("invalid entry_mode")
		}
		// Switching to password (Join-by-PIN) mid-call is only allowed when the
		// call already has a configured join password — S3 never sets or rotates
		// it (deferred to S6). The existing hash is reused untouched.
		if mode == entity.EntryModePassword && call.JoinPasswordHash == "" {
			return nil, cerrors.InvalidInput("entry_mode=password requires a configured join password")
		}
		settings.EntryMode = mode
		// entry_mode is authoritative; keep the legacy waiting_room flag in sync
		// (mirrors StartCall normalisation) so a single source of truth drives the
		// lobby. Changing entry_mode is forward-only — it never retroactively
		// admits or rejects participants already in the waiting room.
		settings.WaitingRoom = mode == entity.EntryModeManualAdmit
	}

	if patch.BreakoutRooms != nil {
		if !*patch.BreakoutRooms {
			rooms, err := s.breakoutRooms.ListByCall(ctx, callID)
			if err != nil {
				slog.ErrorContext(ctx, "failed to list breakout rooms for settings update", "call_id", callID, "error", err)
				return nil, cerrors.Internal("failed to list breakout rooms", err)
			}
			if hasActiveBreakoutRoom(rooms) {
				if err := s.CloseAllBreakoutRooms(ctx, callID, actorID); err != nil {
					return nil, err
				}
			}
		}
		settings.BreakoutRooms = *patch.BreakoutRooms
	}

	// Member-publish policy enforcement (ALK-812). A RESTRICTIVE flip (a member
	// publish source going true→false) must take effect at the media plane BEFORE
	// the new settings are persisted, so a failed enforcement leaves the stored
	// policy unchanged (never "locked" while a member keeps publishing). A
	// permissive / non-publish change persists first, then re-applies best-effort.
	if publishRestrictionTightened(call.Settings, settings) {
		if err := s.enforceMemberPublishPolicy(ctx, callID, settings); err != nil {
			return nil, err // stored setting unchanged; no broadcast
		}
		if err := s.calls.UpdateSettings(ctx, callID, settings); err != nil {
			return nil, s.wrapCallError(ctx, err, callID, "update call settings")
		}
		call.Settings = settings
		s.publishCallSettingsChanged(ctx, call)
		// Close the join-during-enforcement window: a member who joined mid-flip
		// minted a stale permissive token. Best-effort (the persisted policy is
		// already authoritative; a residual sub-window self-heals on rejoin).
		s.reconcileMemberPublishPolicy(ctx, callID, settings)
		return call, nil
	}

	// Capture whether a publish field changed BEFORE mutating call.Settings —
	// otherwise the comparison below would be settings-vs-itself (always false).
	publishChanged := publishAffectingChanged(call.Settings, settings)
	if err := s.calls.UpdateSettings(ctx, callID, settings); err != nil {
		return nil, s.wrapCallError(ctx, err, callID, "update call settings")
	}
	call.Settings = settings
	if publishChanged {
		// A permissive publish flip (false→true): re-apply best-effort so the new
		// allowance reaches connected members without a rejoin. resolveMemberPublish
		// ANDs screen with the per-participant grant, so this never over-grants.
		s.reconcileMemberPublishPolicy(ctx, callID, settings)
	}
	s.publishCallSettingsChanged(ctx, call)

	return call, nil
}

// publishRestrictionTightened reports whether the patch tightens any member
// publish permission (screen / mic / camera going true→false), which requires
// the enforce-then-persist + live-track-stop path.
func publishRestrictionTightened(old, next entity.CallSettings) bool {
	return (old.ScreenSharing && !next.ScreenSharing) ||
		(old.ResolvedMembersCanUnmuteMic() && !next.ResolvedMembersCanUnmuteMic()) ||
		(old.ResolvedMembersCanEnableCamera() && !next.ResolvedMembersCanEnableCamera())
}

// publishAffectingChanged reports whether any member publish-permission field
// changed at all (either direction).
func publishAffectingChanged(old, next entity.CallSettings) bool {
	return old.ScreenSharing != next.ScreenSharing ||
		old.ResolvedMembersCanUnmuteMic() != next.ResolvedMembersCanUnmuteMic() ||
		old.ResolvedMembersCanEnableCamera() != next.ResolvedMembersCanEnableCamera()
}

// isPolicyEnforceableMember reports whether the member-publish policy must be
// enforced against this participant at the media plane: a connected member
// (non host/co-host) in the MAIN room. Breakout members are skipped — they
// re-mint their token through the resolver on return to main.
func isPolicyEnforceableMember(p entity.CallParticipant) bool {
	if p.Status != entity.ParticipantStatusConnected {
		return false
	}
	// Host/co-host are exempt; viewers already publish nothing (resolveMemberPublish
	// returns all-false for them), so there is nothing to enforce and no reason to
	// overwrite their LiveKit permission with a redundant UpdateParticipant.
	if p.Role == entity.CallRoleHost || p.Role == entity.CallRoleCoHost || p.Role == entity.CallRoleViewer {
		return false
	}
	return p.BreakoutRoomID == nil
}

// enforceMemberPublishPolicy applies the given target policy to every connected
// non-breakout member at the LiveKit media plane: for each, it rebuilds the
// permission (revoking now-denied sources so re-publish/re-unmute is blocked)
// and force-stops any live track of a now-denied source. Returns the first
// error so a RESTRICTIVE caller can abort before persisting. No-op without a
// configured LiveKit room client (local dev / tests).
func (s *Service) enforceMemberPublishPolicy(ctx context.Context, callID uuid.UUID, target entity.CallSettings) error {
	if s.livekitRooms == nil {
		return nil
	}
	participants, err := s.calls.ListParticipants(ctx, callID)
	if err != nil {
		return cerrors.Internal("failed to list participants for policy enforcement", err)
	}
	lkParticipants, err := s.livekitRooms.ListParticipants(ctx, callID)
	if err != nil {
		return cerrors.Internal("failed to list livekit participants for policy enforcement", err)
	}
	tracksByIdentity := indexLiveKitTracks(lkParticipants)
	for _, p := range participants {
		if !isPolicyEnforceableMember(p) {
			continue
		}
		canMic, canCam, canShare, _ := resolveMemberPublish(p.Role, target, p.CanScreenShare)
		// (a) revoke the publish sources first so the client cannot re-publish or
		// re-unmute a denied source.
		if err := s.updateLiveKitParticipantPermission(ctx, callID, p.UserID, p.Role, target, p.CanScreenShare); err != nil {
			return err
		}
		// (b) stop any live track of a now-denied source.
		for _, track := range tracksByIdentity[p.UserID.String()] {
			if track.GetMuted() || !sourceDenied(track.GetSource(), canMic, canCam, canShare) {
				continue
			}
			if err := s.livekitRooms.MutePublishedTrack(ctx, callID.String(), p.UserID.String(), track.GetSid()); err != nil {
				return err
			}
		}
	}
	return nil
}

// reconcileMemberPublishPolicy re-applies the persisted policy to connected
// members best-effort (errors logged, not returned). Used after a successful
// commit to close the join-during-enforcement window and to apply a permissive
// flip. It performs the same revoke-and-stop as the strict path, so a member who
// joined mid-flip and started publishing a denied track has it stopped too.
func (s *Service) reconcileMemberPublishPolicy(ctx context.Context, callID uuid.UUID, target entity.CallSettings) {
	if s.livekitRooms == nil {
		return
	}
	if err := s.enforceMemberPublishPolicy(ctx, callID, target); err != nil {
		slog.WarnContext(ctx, "best-effort member-publish reconcile failed",
			"call_id", callID, "error", err)
	}
}

// indexLiveKitTracks maps each LiveKit participant identity (the user id string)
// to its published tracks, for the per-member denied-track lookup.
func indexLiveKitTracks(participants []*livekitpb.ParticipantInfo) map[string][]*livekitpb.TrackInfo {
	out := make(map[string][]*livekitpb.TrackInfo, len(participants))
	for _, p := range participants {
		out[p.GetIdentity()] = p.GetTracks()
	}
	return out
}

// sourceDenied reports whether a track source is disallowed by the resolved
// per-source gates (mic / camera / screen — screen covers both the video and the
// audio screen sources).
func sourceDenied(source livekitpb.TrackSource, canMic, canCam, canShare bool) bool {
	switch source {
	case livekitpb.TrackSource_MICROPHONE:
		return !canMic
	case livekitpb.TrackSource_CAMERA:
		return !canCam
	case livekitpb.TrackSource_SCREEN_SHARE, livekitpb.TrackSource_SCREEN_SHARE_AUDIO:
		return !canShare
	default:
		return false
	}
}

func (s *Service) publishCallSettingsChanged(ctx context.Context, call *entity.Call) {
	channelID := uuid.Nil
	if call.ChannelID != nil {
		channelID = *call.ChannelID
	}

	subject := fmt.Sprintf("aloqa.ws.%s", call.WorkspaceID)
	s.doPublish(ctx, event.TypeCallSettingsChanged, subject, call.WorkspaceID, channelID, call.CreatedBy, event.CallSettingsChangedPayload{
		CallID:   call.ID,
		Settings: call.Settings,
	})
}
