package call

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/domain/repository"
	"aloqa/internal/media/sfu"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/platform/txscope"
	"aloqa/internal/security/collabaccess"
	"aloqa/internal/security/guestaccess"
)

// EventPublisher abstracts event publishing (e.g. NATS).
type EventPublisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

type CollaborationAccessAuthorizer interface {
	AuthorizeCall(ctx context.Context, channelID, userID uuid.UUID) (collabaccess.Decision, error)
}

type MediaControlPlane interface {
	EnsurePlacement(ctx context.Context, call *entity.Call, opts sfu.RoomOptions) (*entity.MediaRoomPlacement, error)
	ResolveParticipantPlacement(ctx context.Context, call *entity.Call, participant *entity.CallParticipant, preferredNodeID string) (*entity.MediaRoomPlacement, error)
	CanServeNode(ctx context.Context, call *entity.Call, nodeID string) (bool, error)
	PolicyForCall(call *entity.Call) entity.MediaCallPolicy
	LocalNodeID() string
	IsLocalNode(nodeID string) bool
	GetCallQualityPolicy(ctx context.Context, workspaceID, callID uuid.UUID) (*entity.MediaQualityPolicy, error)
	RecordQualitySnapshot(ctx context.Context, sample entity.MediaQoSSample) error
}

type recentCallRepository interface {
	ListRecentByWorkspace(ctx context.Context, workspaceID uuid.UUID, limit int, before *time.Time) ([]entity.Call, error)
}

// activeCallSummaryRepository is the optional interface that exposes the
// enriched Live Now projection. The default postgres CallRepo satisfies it;
// fakes used in unit tests may skip it and the handler will fall back to a
// "not available" error.
type activeCallSummaryRepository interface {
	ListActiveSummariesByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]entity.ActiveCallSummary, error)
}

// Service handles call lifecycle, participant management, and WebRTC signaling.
type Service struct {
	calls         repository.CallRepository
	callMessages  repository.CallMessageRepository
	breakoutRooms repository.BreakoutRoomRepository
	channels      repository.ChannelRepository
	members       repository.WorkspaceRepository
	pubsub        EventPublisher
	sfu           *sfu.SFU
	media         MediaConfig
	livekit       LiveKitSettings
	livekitRooms  LiveKitRoomClient
	livekitDedupe *livekitWebhookDedupe
	guests        *guestaccess.Checker
	collab        CollaborationAccessAuthorizer
	control       MediaControlPlane
	tx            txscope.Manager
}

type MediaConfig struct {
	TokenSecret              []byte
	TokenTTL                 time.Duration
	TURNURLs                 []string
	TURNUsername             string
	TURNCredential           string
	TURNCredentialsTTL       time.Duration
	MaxPresentersPerCall     int
	MaxViewersPerCall        int
	MaxScreenSharesPerCall   int
	MaxTracksPerPresenter    int
	DefaultWebinarPresenters int
	Adaptive                 sfu.AdaptiveOptions
}

type RecentCallsResult struct {
	Calls      []entity.Call `json:"calls"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// NewService creates a new call service.
func NewService(
	calls repository.CallRepository,
	breakoutRooms repository.BreakoutRoomRepository,
	channels repository.ChannelRepository,
	members repository.WorkspaceRepository,
	pubsub EventPublisher,
	sfuServer *sfu.SFU,
	media MediaConfig,
	guests *guestaccess.Checker,
	collab CollaborationAccessAuthorizer,
) *Service {
	if media.TokenTTL <= 0 {
		media.TokenTTL = 5 * time.Minute
	}
	if media.DefaultWebinarPresenters <= 0 {
		media.DefaultWebinarPresenters = sfu.DefaultMaxPresenters
	}
	return &Service{
		calls:         calls,
		breakoutRooms: breakoutRooms,
		channels:      channels,
		members:       members,
		pubsub:        pubsub,
		sfu:           sfuServer,
		media:         media,
		guests:        guests,
		collab:        collab,
	}
}

func (s *Service) SetMediaControlPlane(control MediaControlPlane) {
	s.control = control
}

func (s *Service) SetCallMessageRepo(repo repository.CallMessageRepository) {
	s.callMessages = repo
}

func (s *Service) SetTransactionManager(manager txscope.Manager) {
	s.tx = manager
}

func (s *Service) requireWorkspaceMember(ctx context.Context, workspaceID, userID uuid.UUID) error {
	if _, err := s.members.GetMember(ctx, workspaceID, userID); err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.Forbidden("user is not a member of this workspace")
		}
		slog.ErrorContext(ctx, "failed to check workspace membership", "workspace_id", workspaceID, "user_id", userID, "error", err)
		return cerrors.Internal("failed to verify workspace membership", err)
	}
	return nil
}

func (s *Service) canAccessWorkspaceContent(ctx context.Context, workspaceID, userID uuid.UUID) error {
	if err := s.requireWorkspaceMember(ctx, workspaceID, userID); err == nil {
		return nil
	}
	if s.guests != nil {
		allowed, err := s.guests.HasWorkspaceAccess(ctx, workspaceID, userID)
		if err != nil {
			return err
		}
		if allowed {
			return nil
		}
	}
	return cerrors.Forbidden("user does not have access to this workspace")
}

func (s *Service) requireChannelAccess(ctx context.Context, ch *entity.Channel, userID uuid.UUID) error {
	if ch.WorkspaceID == nil {
		return cerrors.NotFound("channel not found")
	}
	workspaceID := *ch.WorkspaceID
	if err := s.requireWorkspaceMember(ctx, workspaceID, userID); err == nil {
		if ch.Type == entity.ChannelTypePublic {
			return nil
		}
		if _, err := s.channels.GetMember(ctx, ch.ID, userID); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
				return cerrors.Forbidden("you do not have access to this channel")
			}
			return cerrors.Internal("failed to check channel membership", err)
		}
		if allowed, err := s.ensureCollaborationChannelAccess(ctx, ch, userID); err != nil {
			return err
		} else if !allowed {
			return cerrors.Forbidden("you do not have access to this collaboration channel")
		}
		return nil
	}
	if s.guests != nil {
		allowed, err := s.guests.HasChannelAccess(ctx, workspaceID, ch.ID, userID)
		if err != nil {
			return err
		}
		if allowed {
			return nil
		}
	}

	if ch.Type == entity.ChannelTypeDM || ch.Type == entity.ChannelTypeGroupDM {
		if _, err := s.channels.GetMember(ctx, ch.ID, userID); err == nil {
			allowed, err := s.ensureCollaborationChannelAccess(ctx, ch, userID)
			if err != nil {
				return err
			}
			if allowed {
				return nil
			}
		} else if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
			return cerrors.Internal("failed to check channel membership", err)
		}
	}
	return cerrors.Forbidden("you do not have access to this channel")
}

func (s *Service) getCallForWorkspace(ctx context.Context, workspaceID, callID uuid.UUID) (*entity.Call, error) {
	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		return nil, s.wrapCallError(ctx, err, callID, "get call")
	}
	if call.WorkspaceID != workspaceID {
		return nil, cerrors.NotFound("call not found")
	}
	return call, nil
}

func (s *Service) requireCallAccess(ctx context.Context, workspaceID, callID, userID uuid.UUID) (*entity.Call, error) {
	call, err := s.getCallForWorkspace(ctx, workspaceID, callID)
	if err != nil {
		return nil, err
	}
	if call.ChannelID != nil {
		ch, err := s.channels.GetByID(ctx, *call.ChannelID)
		if err != nil {
			return nil, cerrors.Internal("failed to get channel", err)
		}
		if err := s.requireChannelAccess(ctx, ch, userID); err != nil {
			return nil, err
		}
		return call, nil
	}
	if err := s.canAccessWorkspaceContent(ctx, call.WorkspaceID, userID); err != nil {
		return nil, err
	}
	return call, nil
}

// CanAccessCall verifies that a user can access a call in a workspace.
func (s *Service) CanAccessCall(ctx context.Context, workspaceID, callID, userID uuid.UUID) error {
	_, err := s.requireCallAccess(ctx, workspaceID, callID, userID)
	return err
}

func (s *Service) ensureCollaborationChannelAccess(ctx context.Context, ch *entity.Channel, userID uuid.UUID) (bool, error) {
	if ch.Type != entity.ChannelTypeDM && ch.Type != entity.ChannelTypeGroupDM {
		return true, nil
	}
	if s.collab == nil {
		return true, nil
	}
	decision, err := s.collab.AuthorizeCall(ctx, ch.ID, userID)
	if err != nil {
		return false, err
	}
	if !decision.Managed {
		return true, nil
	}
	return decision.Allowed, nil
}

func validateCallSettings(callType entity.CallType, settings entity.CallSettings) error {
	switch callType {
	case entity.CallTypeOneToOne, entity.CallTypeGroup, entity.CallTypeMeeting, entity.CallTypeWebinar, entity.CallTypeSelector:
	default:
		return cerrors.InvalidInput("invalid call type")
	}
	if settings.MaxParticipants < 0 {
		return cerrors.InvalidInput("max_participants cannot be negative")
	}
	return nil
}

func (s *Service) callPolicy(call *entity.Call) entity.MediaCallPolicy {
	if s.control != nil {
		return s.control.PolicyForCall(call)
	}

	maxParticipants := call.Settings.MaxParticipants
	if maxParticipants <= 0 {
		switch call.Type {
		case entity.CallTypeOneToOne:
			maxParticipants = 2
		case entity.CallTypeGroup:
			maxParticipants = 32
		case entity.CallTypeMeeting:
			maxParticipants = 500
		case entity.CallTypeWebinar, entity.CallTypeSelector:
			maxParticipants = s.media.MaxViewersPerCall
		default:
			maxParticipants = 500
		}
	}

	policy := entity.MediaCallPolicy{
		MaxParticipants:     maxParticipants,
		MaxPresenters:       s.media.MaxPresentersPerCall,
		MaxViewers:          s.media.MaxViewersPerCall,
		RoutingMode:         entity.MediaRoutingStickyEdge,
		FanoutStrategy:      entity.MediaFanoutSingleNode,
		OverflowPolicy:      entity.MediaOverflowReject,
		ScreenSharePriority: entity.MediaScreenShareBalanced,
		TURNStrategy:        "regional_turn_pool",
		Sticky:              true,
	}
	if call.Type == entity.CallTypeWebinar || call.Type == entity.CallTypeSelector {
		policy.MaxPresenters = s.media.DefaultWebinarPresenters
		policy.MaxViewers = maxParticipants
		policy.FanoutStrategy = entity.MediaFanoutRegionalCascade
		policy.OverflowPolicy = entity.MediaOverflowWebinarEdge
		policy.ScreenSharePriority = entity.MediaScreenShareProtected
	}
	return policy
}

func (s *Service) effectiveParticipantCap(call *entity.Call) int {
	policy := s.callPolicy(call)
	if policy.MaxParticipants > 0 {
		return policy.MaxParticipants
	}
	return call.Settings.MaxParticipants
}

// StartCall creates a new call and adds the creator as the host participant.
func (s *Service) StartCall(
	ctx context.Context,
	workspaceID, userID uuid.UUID,
	callType entity.CallType,
	title string,
	channelID *uuid.UUID,
	settings entity.CallSettings,
) (*entity.Call, error) {
	if err := validateCallSettings(callType, settings); err != nil {
		return nil, err
	}
	if err := s.requireWorkspaceMember(ctx, workspaceID, userID); err != nil {
		return nil, err
	}

	// If linked to a channel, verify it exists.
	if channelID != nil {
		ch, err := s.channels.GetByID(ctx, *channelID)
		if err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
				return nil, cerrors.NotFound("channel not found")
			}
			slog.ErrorContext(ctx, "failed to get channel for call", "channel_id", *channelID, "error", err)
			return nil, cerrors.Internal("failed to get channel", err)
		}
		if ch.WorkspaceID == nil || *ch.WorkspaceID != workspaceID {
			return nil, cerrors.NotFound("channel not found")
		}
		if err := s.requireChannelAccess(ctx, ch, userID); err != nil {
			return nil, err
		}
	}

	now := time.Now()
	call := &entity.Call{
		ID:          id.New(),
		WorkspaceID: workspaceID,
		ChannelID:   channelID,
		Type:        callType,
		Status:      entity.CallStatusRinging,
		Title:       title,
		CreatedBy:   userID,
		Settings:    settings,
		StartedAt:   &now,
		CreatedAt:   now,
	}

	// Add creator as host participant.
	participant := &entity.CallParticipant{
		ID:       id.New(),
		CallID:   call.ID,
		UserID:   userID,
		Role:     entity.CallRoleHost,
		Status:   entity.ParticipantStatusConnected,
		JoinedAt: &now,
	}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Calls() == nil {
				return cerrors.Unavailable("call transaction scope is not configured")
			}
			if err := scope.Calls().Create(ctx, call); err != nil {
				return err
			}
			if err := scope.Calls().AddParticipant(ctx, participant); err != nil {
				return err
			}
			return s.enqueueCallEventTx(ctx, scope, event.TypeCallStarted, call, userID)
		}); err != nil {
			slog.ErrorContext(ctx, "failed to create call transaction", "call_id", call.ID, "error", err)
			return nil, cerrors.Internal("failed to create call", err)
		}
	} else {
		if err := s.calls.Create(ctx, call); err != nil {
			slog.ErrorContext(ctx, "failed to create call", "error", err)
			return nil, cerrors.Internal("failed to create call", err)
		}
		if err := s.calls.AddParticipant(ctx, participant); err != nil {
			slog.ErrorContext(ctx, "failed to add host participant", "call_id", call.ID, "user_id", userID, "error", err)
			return nil, cerrors.Internal("failed to add host participant", err)
		}
		s.publishCallEvent(ctx, event.TypeCallStarted, call, userID)
	}

	s.ensureLiveKitRoomBestEffort(ctx, call)

	placement, placementErr := s.ensureMediaPlacement(ctx, call)
	if placementErr != nil {
		slog.ErrorContext(ctx, "failed to ensure media placement", "call_id", call.ID, "error", placementErr)
	}

	if s.sfu != nil && s.shouldServePlacementLocally(placement) {
		if _, err := s.sfu.CreateRoom(call.ID.String(), s.roomOptions(call)); err != nil {
			slog.ErrorContext(ctx, "failed to create SFU room", "call_id", call.ID, "error", err)
			// Non-fatal: call is created in DB, SFU room can be created on first media join.
		}
	}

	slog.InfoContext(ctx, "call started", "call_id", call.ID, "type", callType, "user_id", userID)
	return call, nil
}

// JoinCall adds a user as a participant to an active call.
func (s *Service) JoinCall(ctx context.Context, workspaceID, callID, userID uuid.UUID) (*entity.CallParticipant, error) {
	call, err := s.requireCallAccess(ctx, workspaceID, callID, userID)
	if err != nil {
		return nil, err
	}

	if call.Status == entity.CallStatusEnded {
		return nil, cerrors.Forbidden("call has already ended")
	}

	// Check capacity if max participants is set.
	effectiveCap := s.effectiveParticipantCap(call)
	if effectiveCap > 0 {
		participants, err := s.calls.ListParticipants(ctx, callID)
		if err != nil {
			slog.ErrorContext(ctx, "failed to list participants for capacity check", "call_id", callID, "error", err)
			return nil, cerrors.Internal("failed to check call capacity", err)
		}

		activeCount := 0
		for _, p := range participants {
			if p.Status == entity.ParticipantStatusConnected || p.Status == entity.ParticipantStatusJoining {
				activeCount++
			}
		}
		if activeCount >= effectiveCap {
			return nil, cerrors.Conflict("call has reached maximum participant capacity")
		}
	}

	// Check if user is already a participant (maybe reconnecting).
	existing, err := s.calls.GetParticipant(ctx, callID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
			slog.ErrorContext(ctx, "failed to check existing participant", "call_id", callID, "user_id", userID, "error", err)
			return nil, cerrors.Internal("failed to check existing participant", err)
		}
	}
	if existing != nil {
		// Reconnecting: update status back to connected.
		if s.tx != nil {
			if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
				if scope.Calls() == nil {
					return cerrors.Unavailable("call transaction scope is not configured")
				}
				if err := scope.Calls().UpdateParticipantStatus(ctx, existing.ID, entity.ParticipantStatusConnected); err != nil {
					return err
				}
				existing.Status = entity.ParticipantStatusConnected
				return s.enqueueParticipantEventTx(ctx, scope, event.TypeCallParticipantJoined, call, existing)
			}); err != nil {
				slog.ErrorContext(ctx, "failed to update participant status on rejoin", "participant_id", existing.ID, "error", err)
				return nil, cerrors.Internal("failed to update participant status", err)
			}
		} else {
			if err := s.calls.UpdateParticipantStatus(ctx, existing.ID, entity.ParticipantStatusConnected); err != nil {
				slog.ErrorContext(ctx, "failed to update participant status on rejoin", "participant_id", existing.ID, "error", err)
				return nil, cerrors.Internal("failed to update participant status", err)
			}
		}
		existing.Status = entity.ParticipantStatusConnected
		now := time.Now()
		existing.JoinedAt = &now
		if s.tx == nil {
			s.publishParticipantEvent(ctx, event.TypeCallParticipantJoined, call, existing)
		}
		s.ensureLiveKitRoomBestEffort(ctx, call)
		slog.InfoContext(ctx, "participant rejoined call", "call_id", callID, "user_id", userID)
		return existing, nil
	}

	now := time.Now()

	// Determine initial status: if waiting room is enabled, place in waiting.
	initialStatus := entity.ParticipantStatusConnected
	if call.Settings.WaitingRoom {
		initialStatus = entity.ParticipantStatusWaiting
	}
	role := entity.CallRoleParticipant
	if call.Type == entity.CallTypeWebinar || call.Type == entity.CallTypeSelector {
		role = entity.CallRoleViewer
	}

	participant := &entity.CallParticipant{
		ID:       id.New(),
		CallID:   callID,
		UserID:   userID,
		Role:     role,
		Status:   initialStatus,
		JoinedAt: &now,
	}

	if call.Settings.MuteOnJoin {
		participant.AudioMuted = true
	}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Calls() == nil {
				return cerrors.Unavailable("call transaction scope is not configured")
			}
			if err := scope.Calls().AddParticipant(ctx, participant); err != nil {
				return err
			}
			if initialStatus == entity.ParticipantStatusWaiting {
				return s.enqueueParticipantEventTx(ctx, scope, event.TypeWaitingRoomJoined, call, participant)
			}
			if call.Status == entity.CallStatusRinging {
				if err := scope.Calls().UpdateStatus(ctx, callID, entity.CallStatusActive); err != nil {
					return err
				}
				call.Status = entity.CallStatusActive
			}
			return s.enqueueParticipantEventTx(ctx, scope, event.TypeCallParticipantJoined, call, participant)
		}); err != nil {
			slog.ErrorContext(ctx, "failed to join call transaction", "call_id", callID, "user_id", userID, "error", err)
			return nil, cerrors.Internal("failed to add participant", err)
		}
	} else {
		if err := s.calls.AddParticipant(ctx, participant); err != nil {
			slog.ErrorContext(ctx, "failed to add participant", "call_id", callID, "user_id", userID, "error", err)
			return nil, cerrors.Internal("failed to add participant", err)
		}

		if initialStatus == entity.ParticipantStatusWaiting {
			s.publishParticipantEvent(ctx, event.TypeWaitingRoomJoined, call, participant)
			slog.InfoContext(ctx, "participant placed in waiting room", "call_id", callID, "user_id", userID)
			return participant, nil
		}

		// Transition call from ringing to active if needed.
		if call.Status == entity.CallStatusRinging {
			if err := s.calls.UpdateStatus(ctx, callID, entity.CallStatusActive); err != nil {
				slog.ErrorContext(ctx, "failed to update call status to active", "call_id", callID, "error", err)
			}
		}

		s.publishParticipantEvent(ctx, event.TypeCallParticipantJoined, call, participant)
	}

	if initialStatus == entity.ParticipantStatusWaiting {
		s.ensureLiveKitRoomBestEffort(ctx, call)
		slog.InfoContext(ctx, "participant placed in waiting room", "call_id", callID, "user_id", userID)
		return participant, nil
	}

	s.ensureLiveKitRoomBestEffort(ctx, call)
	slog.InfoContext(ctx, "participant joined call", "call_id", callID, "user_id", userID)
	return participant, nil
}

// AdmitParticipant moves a participant from the waiting room to the call.
// Only host or co-host can admit participants.
func (s *Service) AdmitParticipant(ctx context.Context, workspaceID, callID, userID, targetUserID uuid.UUID) error {
	call, err := s.requireCallAccess(ctx, workspaceID, callID, userID)
	if err != nil {
		return err
	}

	if err := s.requireHostOrCoHost(ctx, callID, userID); err != nil {
		return err
	}

	participant, err := s.calls.GetParticipant(ctx, callID, targetUserID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("participant not found")
		}
		return cerrors.Internal("failed to get participant", err)
	}

	if participant.Status != entity.ParticipantStatusWaiting {
		return cerrors.Conflict("participant is not in the waiting room")
	}

	if err := s.calls.UpdateParticipantStatus(ctx, participant.ID, entity.ParticipantStatusConnected); err != nil {
		slog.ErrorContext(ctx, "failed to admit participant", "call_id", callID, "target_user_id", targetUserID, "error", err)
		return cerrors.Internal("failed to admit participant", err)
	}

	participant.Status = entity.ParticipantStatusConnected
	s.publishParticipantEvent(ctx, event.TypeWaitingRoomAdmitted, call, participant)

	// Transition call from ringing to active if needed.
	if call.Status == entity.CallStatusRinging {
		if err := s.calls.UpdateStatus(ctx, callID, entity.CallStatusActive); err != nil {
			slog.ErrorContext(ctx, "failed to update call status to active after admission", "call_id", callID, "error", err)
			return cerrors.Internal("failed to activate call", err)
		}
	}

	slog.InfoContext(ctx, "participant admitted from waiting room", "call_id", callID, "target_user_id", targetUserID)
	return nil
}

// AdmitAll moves all waiting participants into the call.
func (s *Service) AdmitAll(ctx context.Context, workspaceID, callID, userID uuid.UUID) error {
	call, err := s.requireCallAccess(ctx, workspaceID, callID, userID)
	if err != nil {
		return err
	}

	if err := s.requireHostOrCoHost(ctx, callID, userID); err != nil {
		return err
	}

	participants, err := s.calls.ListParticipants(ctx, callID)
	if err != nil {
		return cerrors.Internal("failed to list participants", err)
	}

	for _, p := range participants {
		if p.Status != entity.ParticipantStatusWaiting {
			continue
		}
		if err := s.calls.UpdateParticipantStatus(ctx, p.ID, entity.ParticipantStatusConnected); err != nil {
			slog.ErrorContext(ctx, "failed to admit participant", "participant_id", p.ID, "error", err)
			continue
		}
		p.Status = entity.ParticipantStatusConnected
		s.publishParticipantEvent(ctx, event.TypeWaitingRoomAdmitted, call, &p)
	}

	if call.Status == entity.CallStatusRinging {
		if err := s.calls.UpdateStatus(ctx, callID, entity.CallStatusActive); err != nil {
			slog.ErrorContext(ctx, "failed to update call status to active after admitting all", "call_id", callID, "error", err)
			return cerrors.Internal("failed to activate call", err)
		}
	}

	slog.InfoContext(ctx, "all waiting participants admitted", "call_id", callID)
	return nil
}

// RejectParticipant removes a participant from the waiting room.
func (s *Service) RejectParticipant(ctx context.Context, workspaceID, callID, userID, targetUserID uuid.UUID) error {
	call, err := s.requireCallAccess(ctx, workspaceID, callID, userID)
	if err != nil {
		return err
	}

	if err := s.requireHostOrCoHost(ctx, callID, userID); err != nil {
		return err
	}

	participant, err := s.calls.GetParticipant(ctx, callID, targetUserID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("participant not found")
		}
		return cerrors.Internal("failed to get participant", err)
	}

	if participant.Status != entity.ParticipantStatusWaiting {
		return cerrors.Conflict("participant is not in the waiting room")
	}

	if err := s.calls.RemoveParticipant(ctx, callID, targetUserID); err != nil {
		slog.ErrorContext(ctx, "failed to reject participant", "call_id", callID, "target_user_id", targetUserID, "error", err)
		return cerrors.Internal("failed to reject participant", err)
	}

	participant.Status = entity.ParticipantStatusDisconnected
	s.publishParticipantEvent(ctx, event.TypeWaitingRoomRejected, call, participant)
	s.removeLiveKitParticipantBestEffort(ctx, callID, targetUserID)

	slog.InfoContext(ctx, "participant rejected from waiting room", "call_id", callID, "target_user_id", targetUserID)
	return nil
}

// ListWaiting returns participants currently in the waiting room.
func (s *Service) ListWaiting(ctx context.Context, workspaceID, callID, userID uuid.UUID) ([]entity.CallParticipant, error) {
	if _, err := s.requireCallAccess(ctx, workspaceID, callID, userID); err != nil {
		return nil, err
	}
	if err := s.requireHostOrCoHost(ctx, callID, userID); err != nil {
		return nil, err
	}

	participants, err := s.calls.ListParticipants(ctx, callID)
	if err != nil {
		return nil, cerrors.Internal("failed to list participants", err)
	}

	var waiting []entity.CallParticipant
	for _, p := range participants {
		if p.Status == entity.ParticipantStatusWaiting {
			waiting = append(waiting, p)
		}
	}
	return waiting, nil
}

// LeaveCall removes a participant from a call. If the last participant leaves, the call ends.
func (s *Service) LeaveCall(ctx context.Context, workspaceID, callID, userID uuid.UUID) error {
	call, err := s.requireCallAccess(ctx, workspaceID, callID, userID)
	if err != nil {
		return err
	}

	participant, err := s.calls.GetParticipant(ctx, callID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("participant not found in this call")
		}
		slog.ErrorContext(ctx, "failed to get participant for leave", "call_id", callID, "user_id", userID, "error", err)
		return cerrors.Internal("failed to get participant", err)
	}

	autoEnded := false
	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Calls() == nil {
				return cerrors.Unavailable("call transaction scope is not configured")
			}
			if err := scope.Calls().UpdateParticipantStatus(ctx, participant.ID, entity.ParticipantStatusDisconnected); err != nil {
				return err
			}
			participant.Status = entity.ParticipantStatusDisconnected
			if err := s.enqueueParticipantEventTx(ctx, scope, event.TypeCallParticipantLeft, call, participant); err != nil {
				return err
			}
			participants, err := scope.Calls().ListParticipants(ctx, callID)
			if err != nil {
				return err
			}
			hasConnected := false
			for _, p := range participants {
				if p.ID == participant.ID {
					continue
				}
				if p.Status == entity.ParticipantStatusConnected || p.Status == entity.ParticipantStatusJoining {
					hasConnected = true
					break
				}
			}
			if !hasConnected && call.Status != entity.CallStatusEnded {
				if err := scope.Calls().End(ctx, callID); err != nil {
					return err
				}
				call.Status = entity.CallStatusEnded
				if err := s.enqueueCallEventTx(ctx, scope, event.TypeCallEnded, call, userID); err != nil {
					return err
				}
				autoEnded = true
			}
			return nil
		}); err != nil {
			slog.ErrorContext(ctx, "failed to leave call transaction", "call_id", callID, "user_id", userID, "error", err)
			return cerrors.Internal("failed to update participant status", err)
		}
	} else {
		if err := s.calls.UpdateParticipantStatus(ctx, participant.ID, entity.ParticipantStatusDisconnected); err != nil {
			slog.ErrorContext(ctx, "failed to update participant status on leave", "participant_id", participant.ID, "error", err)
			return cerrors.Internal("failed to update participant status", err)
		}

		participant.Status = entity.ParticipantStatusDisconnected
		s.publishParticipantEvent(ctx, event.TypeCallParticipantLeft, call, participant)

		// Check if this was the last connected participant.
		participants, err := s.calls.ListParticipants(ctx, callID)
		if err != nil {
			slog.ErrorContext(ctx, "failed to list participants after leave", "call_id", callID, "error", err)
			return nil // Non-fatal: the leave itself succeeded.
		}

		hasConnected := false
		for _, p := range participants {
			if p.Status == entity.ParticipantStatusConnected || p.Status == entity.ParticipantStatusJoining {
				hasConnected = true
				break
			}
		}

		if !hasConnected && call.Status != entity.CallStatusEnded {
			if err := s.calls.End(ctx, callID); err != nil {
				slog.ErrorContext(ctx, "failed to auto-end call after last participant left", "call_id", callID, "error", err)
			} else {
				autoEnded = true
				call.Status = entity.CallStatusEnded
				s.publishCallEvent(ctx, event.TypeCallEnded, call, userID)
			}
		}
	}

	s.removeLiveKitParticipantBestEffort(ctx, callID, userID)
	if autoEnded {
		s.closeAllBreakoutSFURooms(ctx, callID)
		s.deleteLiveKitRoomBestEffort(ctx, callID)
		s.sfu.CloseRoom(callID.String())
		slog.InfoContext(ctx, "call auto-ended (last participant left)", "call_id", callID)
	}

	slog.InfoContext(ctx, "participant left call", "call_id", callID, "user_id", userID)
	return nil
}

// EndCall ends a call. Only host or co-host can end a call.
func (s *Service) EndCall(ctx context.Context, workspaceID, callID, userID uuid.UUID) error {
	call, err := s.requireCallAccess(ctx, workspaceID, callID, userID)
	if err != nil {
		return err
	}

	if call.Status == entity.CallStatusEnded {
		return cerrors.Conflict("call has already ended")
	}

	// Verify user is host or co-host.
	participant, err := s.calls.GetParticipant(ctx, callID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.Forbidden("user is not a participant in this call")
		}
		slog.ErrorContext(ctx, "failed to get participant for end call", "call_id", callID, "user_id", userID, "error", err)
		return cerrors.Internal("failed to get participant", err)
	}

	if participant.Role != entity.CallRoleHost && participant.Role != entity.CallRoleCoHost {
		return cerrors.Forbidden("only host or co-host can end a call")
	}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Calls() == nil {
				return cerrors.Unavailable("call transaction scope is not configured")
			}
			if err := scope.Calls().End(ctx, callID); err != nil {
				return err
			}
			call.Status = entity.CallStatusEnded
			return s.enqueueCallEventTx(ctx, scope, event.TypeCallEnded, call, userID)
		}); err != nil {
			slog.ErrorContext(ctx, "failed to end call transaction", "call_id", callID, "error", err)
			return cerrors.Internal("failed to end call", err)
		}
	} else {
		if err := s.calls.End(ctx, callID); err != nil {
			slog.ErrorContext(ctx, "failed to end call", "call_id", callID, "error", err)
			return cerrors.Internal("failed to end call", err)
		}
		call.Status = entity.CallStatusEnded
		s.publishCallEvent(ctx, event.TypeCallEnded, call, userID)
	}

	// Close all breakout rooms and their SFU rooms.
	s.closeAllBreakoutSFURooms(ctx, callID)

	s.deleteLiveKitRoomBestEffort(ctx, callID)

	// Close the main SFU room, disconnecting all media peers.
	s.sfu.CloseRoom(callID.String())

	slog.InfoContext(ctx, "call ended", "call_id", callID, "user_id", userID)
	return nil
}

// GetCall retrieves a call by ID after checking workspace and channel access.
func (s *Service) GetCall(ctx context.Context, workspaceID, callID, userID uuid.UUID) (*entity.Call, error) {
	return s.requireCallAccess(ctx, workspaceID, callID, userID)
}

// ListActiveCalls returns all active calls in a workspace.
func (s *Service) ListActiveCalls(ctx context.Context, workspaceID, userID uuid.UUID) ([]entity.Call, error) {
	if err := s.requireWorkspaceMember(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	calls, err := s.calls.ListActiveByWorkspace(ctx, workspaceID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list active calls", "workspace_id", workspaceID, "error", err)
		return nil, cerrors.Internal("failed to list active calls", err)
	}
	return calls, nil
}

// ListActiveSummaries returns the enriched ActiveCallSummary projection used
// by GET /calls/active for the Calls Home Live Now section.
func (s *Service) ListActiveSummaries(ctx context.Context, workspaceID, userID uuid.UUID) ([]entity.ActiveCallSummary, error) {
	if err := s.requireWorkspaceMember(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	repo, ok := s.calls.(activeCallSummaryRepository)
	if !ok {
		return nil, cerrors.Unavailable("active call summaries are not available")
	}
	summaries, err := repo.ListActiveSummariesByWorkspace(ctx, workspaceID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list active call summaries", "workspace_id", workspaceID, "error", err)
		return nil, cerrors.Internal("failed to list active call summaries", err)
	}
	return summaries, nil
}

func (s *Service) ListRecentCalls(ctx context.Context, workspaceID, userID uuid.UUID, limit int, cursor string) (*RecentCallsResult, error) {
	if err := s.requireWorkspaceMember(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	var before *time.Time
	if cursor != "" {
		parsed, err := time.Parse(time.RFC3339Nano, cursor)
		if err != nil {
			return nil, cerrors.InvalidInput("invalid cursor")
		}
		before = &parsed
	}

	repo, ok := s.calls.(recentCallRepository)
	if !ok {
		return nil, cerrors.Unavailable("recent calls are not available")
	}
	calls, err := repo.ListRecentByWorkspace(ctx, workspaceID, limit, before)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list recent calls", "workspace_id", workspaceID, "error", err)
		return nil, cerrors.Internal("failed to list recent calls", err)
	}

	result := &RecentCallsResult{Calls: calls}
	if len(calls) > limit {
		result.NextCursor = calls[limit-1].CreatedAt.UTC().Format(time.RFC3339Nano)
		result.Calls = calls[:limit]
	}
	return result, nil
}

// GetParticipants returns all participants in a call.
func (s *Service) GetParticipants(ctx context.Context, workspaceID, callID, userID uuid.UUID) ([]entity.CallParticipant, error) {
	if _, err := s.requireCallAccess(ctx, workspaceID, callID, userID); err != nil {
		return nil, err
	}

	participants, err := s.calls.ListParticipants(ctx, callID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list call participants", "call_id", callID, "error", err)
		return nil, cerrors.Internal("failed to list participants", err)
	}
	return participants, nil
}

// UpdateMedia patches a participant's media state. Each pointer is optional —
// nil means "leave the existing column alone". Callers that touch only one
// chip (mic mute, screen share start, …) MUST send only that field; passing
// all three with old values would fight the WS event ordering and surface as
// flicker on remote clients.
func (s *Service) UpdateMedia(ctx context.Context, workspaceID, callID, userID uuid.UUID, audioMuted, videoMuted, screenSharing *bool) error {
	call, err := s.requireCallAccess(ctx, workspaceID, callID, userID)
	if err != nil {
		return err
	}

	if call.Status == entity.CallStatusEnded {
		return cerrors.Forbidden("call has already ended")
	}
	if screenSharing != nil && *screenSharing && !call.Settings.ScreenSharing {
		return cerrors.Forbidden("screen sharing is disabled for this call")
	}

	participant, err := s.calls.GetParticipant(ctx, callID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("participant not found in this call")
		}
		slog.ErrorContext(ctx, "failed to get participant for media update", "call_id", callID, "user_id", userID, "error", err)
		return cerrors.Internal("failed to get participant", err)
	}

	// Resolve the effective new state by overlaying the patch on top of the
	// existing participant row. Anything the caller didn't send keeps its
	// current value — this is what makes the API a real PATCH.
	nextAudio := participant.AudioMuted
	nextVideo := participant.VideoMuted
	nextScreen := participant.ScreenSharing
	if audioMuted != nil {
		nextAudio = *audioMuted
	}
	if videoMuted != nil {
		nextVideo = *videoMuted
	}
	if screenSharing != nil {
		nextScreen = *screenSharing
	}

	// Viewers can only ever be fully-muted with no screen — same rule as
	// before, but evaluated against the effective state, not raw inputs.
	if participant.Role == entity.CallRoleViewer && (!nextAudio || !nextVideo || nextScreen) {
		return cerrors.Forbidden("viewers cannot publish media")
	}
	// Capacity check only when the patch flips screen-sharing ON. Toggling
	// off, or leaving it untouched, never trips capacity.
	if screenSharing != nil && *screenSharing && !participant.ScreenSharing {
		if err := s.checkScreenShareCapacity(ctx, callID, participant.ID); err != nil {
			return err
		}
	}

	if err := s.calls.UpdateParticipantMedia(ctx, participant.ID, nextAudio, nextVideo, nextScreen); err != nil {
		slog.ErrorContext(ctx, "failed to update participant media", "participant_id", participant.ID, "error", err)
		return cerrors.Internal("failed to update media state", err)
	}

	participant.AudioMuted = nextAudio
	participant.VideoMuted = nextVideo
	participant.ScreenSharing = nextScreen

	s.publishParticipantEvent(ctx, event.TypeCallParticipantUpdated, call, participant)

	slog.InfoContext(ctx, "participant media updated", "call_id", callID, "user_id", userID,
		"audio_muted", nextAudio, "video_muted", nextVideo, "screen_sharing", nextScreen)
	return nil
}

func (s *Service) UpdateParticipantRole(ctx context.Context, workspaceID, callID, actorUserID, targetUserID uuid.UUID, role entity.CallRole) error {
	call, err := s.requireCallAccess(ctx, workspaceID, callID, actorUserID)
	if err != nil {
		return err
	}
	if call.Status == entity.CallStatusEnded {
		return cerrors.Forbidden("call has already ended")
	}
	if err := validateAssignableRole(call.Type, role); err != nil {
		return err
	}

	actor, err := s.calls.GetParticipant(ctx, callID, actorUserID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.Forbidden("actor is not a participant")
		}
		return cerrors.Internal("failed to get actor participant", err)
	}
	if actor.Role != entity.CallRoleHost && actor.Role != entity.CallRoleCoHost {
		return cerrors.Forbidden("only host or co-host can change participant roles")
	}

	target, err := s.calls.GetParticipant(ctx, callID, targetUserID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("participant not found")
		}
		return cerrors.Internal("failed to get target participant", err)
	}
	if target.Role == entity.CallRoleHost && target.UserID != actor.UserID {
		return cerrors.Forbidden("host role cannot be changed by another participant")
	}
	if actor.Role == entity.CallRoleCoHost && (role == entity.CallRoleHost || role == entity.CallRoleCoHost) {
		return cerrors.Forbidden("co-hosts cannot assign host or co-host roles")
	}
	if actor.UserID == target.UserID && actor.Role == entity.CallRoleHost && role != entity.CallRoleHost {
		return cerrors.Forbidden("host cannot demote themselves")
	}

	if err := s.calls.UpdateParticipantRole(ctx, target.ID, role); err != nil {
		return cerrors.Internal("failed to update participant role", err)
	}
	target.Role = role

	if role == entity.CallRoleViewer {
		if err := s.calls.UpdateParticipantMedia(ctx, target.ID, true, true, false); err != nil {
			return cerrors.Internal("failed to disable viewer media", err)
		}
		target.AudioMuted = true
		target.VideoMuted = true
		target.ScreenSharing = false
	}

	s.publishParticipantEvent(ctx, event.TypeCallParticipantUpdated, call, target)
	return nil
}

func validateAssignableRole(callType entity.CallType, role entity.CallRole) error {
	switch role {
	case entity.CallRoleHost, entity.CallRoleCoHost, entity.CallRolePresenter, entity.CallRoleParticipant:
	case entity.CallRoleViewer:
		if callType != entity.CallTypeWebinar && callType != entity.CallTypeSelector {
			return cerrors.InvalidInput("viewer role is only valid for webinars and selector calls")
		}
	default:
		return cerrors.InvalidInput("invalid call role")
	}
	return nil
}

func (s *Service) checkScreenShareCapacity(ctx context.Context, callID, currentParticipantID uuid.UUID) error {
	limit := s.media.MaxScreenSharesPerCall
	if limit <= 0 {
		limit = 1
	}
	participants, err := s.calls.ListParticipants(ctx, callID)
	if err != nil {
		return cerrors.Internal("failed to check screen share capacity", err)
	}
	activeShares := 0
	for _, p := range participants {
		if p.ID == currentParticipantID {
			continue
		}
		if p.ScreenSharing && p.Status == entity.ParticipantStatusConnected {
			activeShares++
		}
	}
	if activeShares >= limit {
		return cerrors.Conflict("screen share limit reached for this call")
	}
	return nil
}

// SetQuality switches a participant's video quality for a specific simulcast
// stream. This enables per-viewer adaptive quality: clients can request lower
// quality when bandwidth is limited or when a video tile is small.
func (s *Service) SetQuality(
	ctx context.Context,
	workspaceID, callID, userID uuid.UUID,
	streamID string,
	quality string,
) error {
	call, err := s.requireCallAccess(ctx, workspaceID, callID, userID)
	if err != nil {
		return err
	}

	if call.Status == entity.CallStatusEnded {
		return cerrors.Forbidden("call has already ended")
	}

	// Verify participant is in the call.
	if _, err := s.calls.GetParticipant(ctx, callID, userID); err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.Forbidden("user is not a participant in this call")
		}
		return cerrors.Internal("failed to get participant", err)
	}

	if s.sfu == nil {
		return cerrors.Unavailable("media server is not available")
	}
	sfuRoom, ok := s.sfu.GetRoom(callID.String())
	if !ok {
		return cerrors.NotFound("media room not found")
	}

	layer := sfu.QualityLayerFromRID(quality)
	if err := sfuRoom.SetSubscriberQuality(userID.String(), streamID, layer); err != nil {
		slog.ErrorContext(ctx, "failed to set subscriber quality",
			"call_id", callID, "user_id", userID, "stream_id", streamID, "quality", quality, "error", err)
		return cerrors.Internal("failed to set quality", err)
	}

	slog.InfoContext(ctx, "subscriber quality set",
		"call_id", callID, "user_id", userID, "stream_id", streamID, "quality", quality)
	return nil
}

// ForwardSignal relays a WebRTC signaling message to a specific user.
func (s *Service) ForwardSignal(
	ctx context.Context,
	callID, fromUser, toUser uuid.UUID,
	signalType string,
	payload event.SignalPayload,
) error {
	// Verify the call exists and is active.
	call, err := s.calls.GetByID(ctx, callID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("call not found")
		}
		slog.ErrorContext(ctx, "failed to get call for signal", "call_id", callID, "error", err)
		return cerrors.Internal("failed to get call", err)
	}

	if call.Status == entity.CallStatusEnded {
		return cerrors.Forbidden("call has already ended")
	}

	// Verify both users are participants.
	if _, err := s.calls.GetParticipant(ctx, callID, fromUser); err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.Forbidden("sender is not a participant in this call")
		}
		slog.ErrorContext(ctx, "failed to verify sender participant", "call_id", callID, "from_user", fromUser, "error", err)
		return cerrors.Internal("failed to verify sender", err)
	}

	if _, err := s.calls.GetParticipant(ctx, callID, toUser); err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("recipient is not a participant in this call")
		}
		slog.ErrorContext(ctx, "failed to verify recipient participant", "call_id", callID, "to_user", toUser, "error", err)
		return cerrors.Internal("failed to verify recipient", err)
	}

	// Determine event type from signal type.
	var evtType event.Type
	switch signalType {
	case "offer":
		evtType = event.TypeSignalOffer
	case "answer":
		evtType = event.TypeSignalAnswer
	case "candidate":
		evtType = event.TypeSignalCandidate
	default:
		return cerrors.InvalidInput(fmt.Sprintf("unknown signal type: %s", signalType))
	}

	// Ensure payload fields are populated.
	payload.CallID = callID
	payload.FromUser = fromUser
	payload.ToUser = toUser

	// Publish directly to the target user's signaling subject.
	subject := fmt.Sprintf("aloqa.signal.%s", toUser)
	s.doPublish(ctx, evtType, subject, call.WorkspaceID, uuid.Nil, fromUser, payload)

	slog.DebugContext(ctx, "signal forwarded", "call_id", callID, "from", fromUser, "to", toUser, "type", signalType)
	return nil
}

// --- Event helpers ---

func (s *Service) publishCallEvent(ctx context.Context, evtType event.Type, call *entity.Call, userID uuid.UUID) {
	channelID := uuid.Nil
	if call.ChannelID != nil {
		channelID = *call.ChannelID
	}

	subject := fmt.Sprintf("aloqa.ws.%s", call.WorkspaceID)
	s.doPublish(ctx, evtType, subject, call.WorkspaceID, channelID, userID, event.CallPayload{Call: call})
}

func (s *Service) publishParticipantEvent(ctx context.Context, evtType event.Type, call *entity.Call, p *entity.CallParticipant) {
	channelID := uuid.Nil
	if call.ChannelID != nil {
		channelID = *call.ChannelID
	}

	subject := fmt.Sprintf("aloqa.ws.%s", call.WorkspaceID)
	s.doPublish(ctx, evtType, subject, call.WorkspaceID, channelID, p.UserID, event.CallParticipantPayload{
		CallID:      call.ID,
		Participant: p,
	})
}

func (s *Service) publishCallMessageEvent(ctx context.Context, evtType event.Type, call *entity.Call, msg *entity.CallMessage) {
	channelID := uuid.Nil
	if call.ChannelID != nil {
		channelID = *call.ChannelID
	}
	subject := fmt.Sprintf("aloqa.ws.%s", call.WorkspaceID)
	payload := callMessagePayloadFor(evtType, call, msg)
	s.doPublish(ctx, evtType, subject, call.WorkspaceID, channelID, msg.SenderID, payload)
}

func (s *Service) enqueueCallEventTx(ctx context.Context, scope txscope.Scope, evtType event.Type, call *entity.Call, userID uuid.UUID) error {
	channelID := uuid.Nil
	if call != nil && call.ChannelID != nil {
		channelID = *call.ChannelID
	}
	return s.enqueueRealtimeTx(ctx, scope, evtType, fmt.Sprintf("aloqa.ws.%s", call.WorkspaceID), call.WorkspaceID, channelID, userID, event.CallPayload{Call: call})
}

func (s *Service) enqueueParticipantEventTx(ctx context.Context, scope txscope.Scope, evtType event.Type, call *entity.Call, p *entity.CallParticipant) error {
	channelID := uuid.Nil
	if call != nil && call.ChannelID != nil {
		channelID = *call.ChannelID
	}
	return s.enqueueRealtimeTx(ctx, scope, evtType, fmt.Sprintf("aloqa.ws.%s", call.WorkspaceID), call.WorkspaceID, channelID, p.UserID, event.CallParticipantPayload{
		CallID:      call.ID,
		Participant: p,
	})
}

func (s *Service) enqueueCallMessageEventTx(ctx context.Context, scope txscope.Scope, evtType event.Type, call *entity.Call, msg *entity.CallMessage) error {
	channelID := uuid.Nil
	if call.ChannelID != nil {
		channelID = *call.ChannelID
	}
	subject := fmt.Sprintf("aloqa.ws.%s", call.WorkspaceID)
	payload := callMessagePayloadFor(evtType, call, msg)
	return s.enqueueRealtimeTx(ctx, scope, evtType, subject, call.WorkspaceID, channelID, msg.SenderID, payload)
}

func callMessagePayloadFor(evtType event.Type, call *entity.Call, msg *entity.CallMessage) any {
	if evtType == event.TypeCallMessageDeleted {
		return event.CallMessageDeletedPayload{CallID: call.ID, MessageID: msg.ID}
	}
	return event.CallMessagePayload{CallID: call.ID, Message: *msg}
}

func (s *Service) enqueueRealtimeTx(ctx context.Context, scope txscope.Scope, evtType event.Type, subject string, workspaceID, channelID, userID uuid.UUID, payload any) error {
	if scope == nil {
		return cerrors.Unavailable("transaction scope is not configured")
	}
	evt, body, durable, err := event.Prepare(subject, event.Event{
		Type:        evtType,
		WorkspaceID: workspaceID,
		ChannelID:   channelID,
		UserID:      userID,
		Timestamp:   time.Now(),
		Payload:     payload,
	})
	if err != nil {
		return err
	}
	if !durable {
		slog.WarnContext(ctx, "enqueueRealtimeTx invoked for non-durable event; skipping outbox enqueue",
			"type", evtType, "subject", subject)
		return nil
	}
	return scope.EnqueueRealtime(ctx, evt, body)
}

func (s *Service) doPublish(ctx context.Context, evtType event.Type, subject string, workspaceID, channelID, userID uuid.UUID, payload any) {
	evt := event.Event{
		ID:          id.New(),
		Type:        evtType,
		WorkspaceID: workspaceID,
		ChannelID:   channelID,
		UserID:      userID,
		Timestamp:   time.Now(),
		Payload:     payload,
	}

	data, err := json.Marshal(evt)
	if err != nil {
		slog.ErrorContext(ctx, "failed to marshal event", "type", evtType, "error", err)
		return
	}

	if err := s.pubsub.Publish(ctx, subject, data); err != nil {
		slog.ErrorContext(ctx, "failed to publish event", "type", evtType, "subject", subject, "error", err)
	}
}

// closeAllBreakoutSFURooms closes all SFU rooms for breakout rooms of a call.
// Called when the main call ends (manually or auto-end).
func (s *Service) closeAllBreakoutSFURooms(ctx context.Context, callID uuid.UUID) {
	rooms, err := s.breakoutRooms.ListByCall(ctx, callID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list breakout rooms for cleanup", "call_id", callID, "error", err)
		return
	}

	for _, room := range rooms {
		if room.Status != entity.BreakoutRoomStatusActive {
			continue
		}
		sfuRoomID := fmt.Sprintf("%s:breakout:%s", callID, room.ID)
		s.sfu.CloseRoom(sfuRoomID)
	}

	if err := s.breakoutRooms.CloseAllByCall(ctx, callID); err != nil {
		slog.ErrorContext(ctx, "failed to close all breakout rooms on call end", "call_id", callID, "error", err)
	}
}
