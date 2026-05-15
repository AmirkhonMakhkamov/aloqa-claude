package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/pkg/pagination"
	"aloqa/internal/pkg/validate"
	"aloqa/internal/platform/txscope"
	"aloqa/internal/security/accesspolicy"
	"aloqa/internal/security/collabaccess"
	"aloqa/internal/security/guestaccess"
	searchsvc "aloqa/internal/service/search"
)

// EventPublisher abstracts event publishing (e.g. NATS).
type EventPublisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

type CollaborationAccessAuthorizer interface {
	AuthorizeChannel(ctx context.Context, channelID, userID uuid.UUID) (collabaccess.Decision, error)
}

type SearchIndexer interface {
	IndexMessage(ctx context.Context, workspaceID, channelID, messageID uuid.UUID, content string, createdAt time.Time) error
	DeleteMessage(ctx context.Context, workspaceID, messageID uuid.UUID) error
	DeleteFile(ctx context.Context, workspaceID, attachmentID uuid.UUID) error
	IndexChannel(ctx context.Context, workspaceID, channelID uuid.UUID, name, topic string, createdAt, updatedAt time.Time) error
}

// Service handles chat channels, messaging, and real-time event distribution.
type Service struct {
	channels      repository.ChannelRepository
	messages      repository.MessageRepository
	members       repository.WorkspaceRepository
	channelGrants repository.ChannelAccessGrantRepository
	readStates    repository.ChannelAccessStateRepository
	pubsub        EventPublisher
	guests        *guestaccess.Checker
	collab        CollaborationAccessAuthorizer
	access        *accesspolicy.Checker
	search        SearchIndexer
	tx            txscope.Manager
	contacts      interface {
		CanShareChannel(ctx context.Context, sourceWorkspaceID, targetWorkspaceID, sourceUserID, targetUserID uuid.UUID) error
	}
}

// NewService creates a new chat service.
func NewService(
	channels repository.ChannelRepository,
	messages repository.MessageRepository,
	members repository.WorkspaceRepository,
	channelGrants repository.ChannelAccessGrantRepository,
	pubsub EventPublisher,
	guests *guestaccess.Checker,
	collab CollaborationAccessAuthorizer,
	search SearchIndexer,
	contacts interface {
		CanShareChannel(ctx context.Context, sourceWorkspaceID, targetWorkspaceID, sourceUserID, targetUserID uuid.UUID) error
	},
) *Service {
	return &Service{
		channels:      channels,
		messages:      messages,
		members:       members,
		channelGrants: channelGrants,
		pubsub:        pubsub,
		guests:        guests,
		collab:        collab,
		search:        search,
		contacts:      contacts,
	}
}

func (s *Service) SetAccessPolicy(access *accesspolicy.Checker) {
	s.access = access
}

func (s *Service) SetChannelAccessStates(states repository.ChannelAccessStateRepository) {
	s.readStates = states
}

func (s *Service) SetTransactionManager(manager txscope.Manager) {
	s.tx = manager
}

// CanAccessWorkspace verifies that the user belongs to the workspace.
func (s *Service) CanAccessWorkspace(ctx context.Context, workspaceID, userID uuid.UUID) error {
	if _, err := s.members.GetMember(ctx, workspaceID, userID); err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.Forbidden("user is not a member of this workspace")
		}
		slog.ErrorContext(ctx, "failed to check workspace membership", "workspace_id", workspaceID, "user_id", userID, "error", err)
		return cerrors.Internal("failed to verify workspace membership", err)
	}
	return nil
}

// GetAccessibleChannel returns a channel only if the user can access it.
func (s *Service) GetAccessibleChannel(ctx context.Context, channelID, userID uuid.UUID) (*entity.Channel, error) {
	if s.access != nil {
		decision, err := s.access.Channel(ctx, channelID, userID, accesspolicy.CapabilityView)
		if err != nil {
			return nil, err
		}
		return decision.Channel, nil
	}

	ch, err := s.channels.GetByID(ctx, channelID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.NotFound("channel not found")
		}
		slog.ErrorContext(ctx, "failed to get channel", "channel_id", channelID, "error", err)
		return nil, cerrors.Internal("failed to get channel", err)
	}

	if err := s.CanAccessWorkspace(ctx, ch.WorkspaceID, userID); err == nil {
		if ch.Type != entity.ChannelTypePublic {
			if _, err := s.channels.GetMember(ctx, channelID, userID); err != nil {
				if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
					return nil, cerrors.Forbidden("you do not have access to this channel")
				}
				slog.ErrorContext(ctx, "failed to check channel membership", "channel_id", channelID, "user_id", userID, "error", err)
				return nil, cerrors.Internal("failed to check channel membership", err)
			}
		}
		if allowed, err := s.ensureCollaborationChannelAccess(ctx, ch, userID); err != nil {
			return nil, err
		} else if !allowed {
			return nil, cerrors.Forbidden("you do not have access to this collaboration channel")
		}
		return ch, nil
	}

	if s.guests != nil {
		allowed, err := s.guests.HasChannelAccess(ctx, ch.WorkspaceID, channelID, userID)
		if err != nil {
			return nil, err
		}
		if allowed {
			return ch, nil
		}
	}

	if ch.Type == entity.ChannelTypeDM || ch.Type == entity.ChannelTypeGroupDM {
		if _, err := s.channels.GetMember(ctx, channelID, userID); err == nil {
			allowed, err := s.ensureCollaborationChannelAccess(ctx, ch, userID)
			if err != nil {
				return nil, err
			}
			if allowed {
				return ch, nil
			}
		} else if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
			slog.ErrorContext(ctx, "failed to check collaboration channel membership", "channel_id", channelID, "user_id", userID, "error", err)
			return nil, cerrors.Internal("failed to check channel membership", err)
		}
	}

	return nil, cerrors.Forbidden("you do not have access to this channel")
}

// CanAccessChannel verifies channel access without returning the channel.
func (s *Service) CanAccessChannel(ctx context.Context, channelID, userID uuid.UUID) error {
	_, err := s.GetAccessibleChannel(ctx, channelID, userID)
	return err
}

func (s *Service) requireMessageAccess(ctx context.Context, messageID, userID uuid.UUID) (*entity.Message, *entity.Channel, error) {
	return s.requireMessageAccessWithCapability(ctx, messageID, userID, accesspolicy.CapabilityView)
}

func (s *Service) requireMessageAccessWithCapability(
	ctx context.Context,
	messageID, userID uuid.UUID,
	capability accesspolicy.Capability,
) (*entity.Message, *entity.Channel, error) {
	msg, err := s.messages.GetByID(ctx, messageID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, nil, cerrors.NotFound("message not found")
		}
		slog.ErrorContext(ctx, "failed to get message", "message_id", messageID, "error", err)
		return nil, nil, cerrors.Internal("failed to get message", err)
	}
	if msg.DeletedAt != nil {
		return nil, nil, cerrors.NotFound("message has been deleted")
	}

	decision, err := s.authorizeChannel(ctx, msg.ChannelID, userID, capability)
	if err != nil {
		return nil, nil, err
	}

	return msg, decision.Channel, nil
}

func (s *Service) requireOwnMessageAccess(
	ctx context.Context,
	messageID, userID uuid.UUID,
	capability accesspolicy.Capability,
) (*entity.Message, *entity.Channel, error) {
	msg, ch, err := s.requireMessageAccessWithCapability(ctx, messageID, userID, capability)
	if err != nil {
		return nil, nil, err
	}
	if msg.UserID != userID {
		return nil, nil, cerrors.Forbidden("can only modify your own messages")
	}
	return msg, ch, nil
}

// --- Input validation structs ---

// CreateChannelInput validates channel creation parameters.
type CreateChannelInput struct {
	Name  string `validate:"required,min=1,max=80"`
	Topic string `validate:"max=250"`
}

// SendMessageInput validates message content.
type SendMessageInput struct {
	Content string `validate:"required,min=1,max=40000"`
}

// CreateChannel creates a new channel and adds the creator as owner.
func (s *Service) CreateChannel(
	ctx context.Context,
	workspaceID, userID uuid.UUID,
	name, topic string,
	chType entity.ChannelType,
) (*entity.Channel, error) {
	input := CreateChannelInput{Name: name, Topic: topic}
	if err := validate.Struct(input); err != nil {
		return nil, err
	}

	if err := s.CanAccessWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}

	now := time.Now()
	ch := &entity.Channel{
		ID:          id.New(),
		WorkspaceID: workspaceID,
		Name:        name,
		Topic:       topic,
		Type:        chType,
		CreatedBy:   userID,
		Archived:    false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	member := &entity.ChannelMember{
		ID:         id.New(),
		ChannelID:  ch.ID,
		UserID:     userID,
		Role:       entity.ChannelRoleOwner,
		LastReadAt: now,
		JoinedAt:   now,
	}
	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Channels() == nil {
				return cerrors.Unavailable("channel transaction scope is not configured")
			}
			if err := scope.Channels().Create(ctx, ch); err != nil {
				return err
			}
			if err := scope.Channels().AddMember(ctx, member); err != nil {
				return err
			}
			if err := s.enqueueChannelSearchTx(ctx, scope, ch); err != nil {
				return err
			}
			return s.enqueueEventTx(ctx, scope, event.TypeChannelCreated, fmt.Sprintf("aloqa.chat.%s", ch.ID), workspaceID, ch.ID, userID, event.ChannelPayload{Channel: ch})
		}); err != nil {
			slog.ErrorContext(ctx, "failed to create channel transaction", "name", name, "error", err)
			return nil, cerrors.Internal("failed to create channel", err)
		}
	} else {
		if err := s.channels.Create(ctx, ch); err != nil {
			slog.ErrorContext(ctx, "failed to create channel", "name", name, "error", err)
			return nil, cerrors.Internal("failed to create channel", err)
		}
		if err := s.channels.AddMember(ctx, member); err != nil {
			slog.ErrorContext(ctx, "failed to add channel owner", "channel_id", ch.ID, "user_id", userID, "error", err)
			return nil, cerrors.Internal("failed to add channel owner", err)
		}

		s.enqueueSearch(ctx, "index channel", func() error {
			return s.search.IndexChannel(ctx, workspaceID, ch.ID, ch.Name, ch.Topic, ch.CreatedAt, ch.UpdatedAt)
		})
		s.publishEvent(ctx, event.TypeChannelCreated, workspaceID, ch.ID, userID, event.ChannelPayload{Channel: ch})
	}

	slog.InfoContext(ctx, "channel created", "channel_id", ch.ID, "name", name, "type", chType)
	return ch, nil
}

func (s *Service) UpdateChannel(ctx context.Context, channelID, userID uuid.UUID, name, topic string) (*entity.Channel, error) {
	input := CreateChannelInput{Name: name, Topic: topic}
	if err := validate.Struct(input); err != nil {
		return nil, err
	}

	ch, err := s.GetAccessibleChannel(ctx, channelID, userID)
	if err != nil {
		return nil, err
	}
	if ch.Type == entity.ChannelTypeDM {
		return nil, cerrors.Forbidden("direct messages cannot be renamed")
	}
	if ch.Archived {
		return nil, cerrors.Forbidden("cannot update an archived channel")
	}

	channelMember, err := s.channels.GetMember(ctx, channelID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.Forbidden("user is not a member of this channel")
		}
		return nil, cerrors.Internal("failed to verify channel membership", err)
	}
	if channelMember.Role != entity.ChannelRoleOwner && channelMember.Role != entity.ChannelRoleAdmin {
		workspaceMember, err := s.members.GetMember(ctx, ch.WorkspaceID, userID)
		if err != nil {
			return nil, cerrors.Internal("failed to verify workspace membership", err)
		}
		if workspaceMember.Role != entity.WorkspaceRoleOwner && workspaceMember.Role != entity.WorkspaceRoleAdmin {
			return nil, cerrors.Forbidden("insufficient permission to update channel")
		}
	}

	ch.Name = name
	ch.Topic = topic
	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Channels() == nil {
				return cerrors.Unavailable("channel transaction scope is not configured")
			}
			if err := scope.Channels().Update(ctx, ch); err != nil {
				return err
			}
			if err := s.enqueueChannelSearchTx(ctx, scope, ch); err != nil {
				return err
			}
			return s.enqueueEventTx(ctx, scope, event.TypeChannelUpdated, fmt.Sprintf("aloqa.chat.%s", ch.ID), ch.WorkspaceID, ch.ID, userID, event.ChannelPayload{Channel: ch})
		}); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return nil, appErr
			}
			return nil, cerrors.Internal("failed to update channel", err)
		}
	} else {
		if err := s.channels.Update(ctx, ch); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return nil, appErr
			}
			return nil, cerrors.Internal("failed to update channel", err)
		}

		s.enqueueSearch(ctx, "index channel", func() error {
			return s.search.IndexChannel(ctx, ch.WorkspaceID, ch.ID, ch.Name, ch.Topic, ch.CreatedAt, ch.UpdatedAt)
		})
		s.publishEvent(ctx, event.TypeChannelUpdated, ch.WorkspaceID, ch.ID, userID, event.ChannelPayload{Channel: ch})
	}
	return ch, nil
}

// GetChannel retrieves a channel by ID. Public channels are visible to all
// workspace members; private/DM channels require membership.
func (s *Service) GetChannel(ctx context.Context, channelID, userID uuid.UUID) (*entity.Channel, error) {
	ch, err := s.GetAccessibleChannel(ctx, channelID, userID)
	if err != nil {
		return nil, err
	}
	if err := s.hydrateDMMembers(ctx, ch); err != nil {
		return nil, err
	}
	return ch, nil
}

// hydrateDMMembers populates Channel.Members with the channel's member user
// IDs when the channel is a DM or group DM. Other channel types are left
// untouched so the response shape is unchanged for them. Errors in fetching
// members are surfaced; the field is left nil on error rather than partially
// populated.
func (s *Service) hydrateDMMembers(ctx context.Context, channels ...*entity.Channel) error {
	for _, ch := range channels {
		if ch == nil {
			continue
		}
		if ch.Type != entity.ChannelTypeDM && ch.Type != entity.ChannelTypeGroupDM {
			continue
		}
		members, err := s.channels.ListMembers(ctx, ch.ID)
		if err != nil {
			slog.ErrorContext(ctx, "failed to list DM channel members", "channel_id", ch.ID, "error", err)
			return cerrors.Internal("failed to list DM channel members", err)
		}
		userIDs := make([]uuid.UUID, 0, len(members))
		for _, m := range members {
			userIDs = append(userIDs, m.UserID)
		}
		ch.Members = userIDs
	}
	return nil
}

// ListChannels returns channels the user is a member of in a workspace.
// When an access policy is configured it enforces richer rules (guest access,
// suspension, etc.); otherwise membership from channels.ListByUser is
// authoritative.
func (s *Service) ListChannels(ctx context.Context, workspaceID, userID uuid.UUID) ([]entity.Channel, error) {
	var channels []entity.Channel
	if s.access != nil {
		result, err := s.access.ListChannels(ctx, workspaceID, userID, accesspolicy.CapabilityView)
		if err != nil {
			return nil, err
		}
		channels = result
	} else {
		result, err := s.channels.ListByUser(ctx, workspaceID, userID)
		if err != nil {
			slog.ErrorContext(ctx, "failed to list user channels", "workspace_id", workspaceID, "user_id", userID, "error", err)
			return nil, cerrors.Internal("failed to list channels", err)
		}
		channels = result
	}

	// Lazy DM visibility: enforce at the service layer so both the
	// access-policy path (CapabilityView via accesspolicy.ListChannels →
	// channels.ListByWorkspace, which does not carry the filter) and the
	// no-access-policy fallback (channels.ListByUser, which already filters
	// in SQL) converge on the same visibility contract. Duplicate work on
	// the fallback path is a no-op because all empty-recipient DMs were
	// already removed before they reached us.
	channels = s.filterAbandonedRecipientDMs(ctx, userID, channels)

	ptrs := make([]*entity.Channel, len(channels))
	for i := range channels {
		ptrs[i] = &channels[i]
	}
	if err := s.hydrateDMMembers(ctx, ptrs...); err != nil {
		return nil, err
	}
	return channels, nil
}

// filterAbandonedRecipientDMs drops DM channels where the caller is not the
// creator and no message has ever been sent. Empty DMs the caller created
// stay visible so they can come back to a recipient they searched for; non-DM
// channels are unaffected.
func (s *Service) filterAbandonedRecipientDMs(ctx context.Context, userID uuid.UUID, channels []entity.Channel) []entity.Channel {
	if len(channels) == 0 || s.messages == nil {
		return channels
	}
	out := make([]entity.Channel, 0, len(channels))
	for _, ch := range channels {
		if ch.Type != entity.ChannelTypeDM || ch.CreatedBy == userID {
			out = append(out, ch)
			continue
		}
		hasActiveMessage, err := s.messages.HasActiveMessage(ctx, ch.ID)
		if err != nil {
			// Fail open — surface the channel rather than hide a real DM. The
			// concrete error is already logged by the caller's request log.
			slog.WarnContext(ctx, "failed to probe DM messages for visibility filter", "channel_id", ch.ID, "error", err)
			out = append(out, ch)
			continue
		}
		if !hasActiveMessage {
			continue
		}
		out = append(out, ch)
	}
	return out
}

// JoinChannel adds a user to a public channel.
func (s *Service) JoinChannel(ctx context.Context, channelID, userID uuid.UUID) error {
	ch, err := s.channels.GetByID(ctx, channelID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("channel not found")
		}
		slog.ErrorContext(ctx, "failed to get channel for join", "channel_id", channelID, "error", err)
		return cerrors.Internal("failed to get channel", err)
	}

	if ch.Type != entity.ChannelTypePublic {
		return cerrors.Forbidden("can only join public channels")
	}

	if err := s.CanAccessWorkspace(ctx, ch.WorkspaceID, userID); err != nil {
		return err
	}

	if ch.Archived {
		return cerrors.Forbidden("cannot join an archived channel")
	}

	// Check if already a member.
	existing, err := s.channels.GetMember(ctx, channelID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
			slog.ErrorContext(ctx, "failed to check channel membership", "channel_id", channelID, "user_id", userID, "error", err)
			return cerrors.Internal("failed to check channel membership", err)
		}
	}
	if existing != nil {
		return cerrors.AlreadyExists("user is already a member of this channel")
	}

	now := time.Now()
	member := &entity.ChannelMember{
		ID:         id.New(),
		ChannelID:  channelID,
		UserID:     userID,
		Role:       entity.ChannelRoleMember,
		LastReadAt: now,
		JoinedAt:   now,
	}
	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Channels() == nil {
				return cerrors.Unavailable("channel transaction scope is not configured")
			}
			if err := scope.Channels().AddMember(ctx, member); err != nil {
				return err
			}
			return s.enqueueEventTx(ctx, scope, event.TypeMemberJoined, fmt.Sprintf("aloqa.chat.%s", channelID), ch.WorkspaceID, channelID, userID, event.MemberPayload{
				ChannelID: channelID,
				UserID:    userID,
			})
		}); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return appErr
			}
			slog.ErrorContext(ctx, "failed to join channel transaction", "channel_id", channelID, "user_id", userID, "error", err)
			return cerrors.Internal("failed to add channel member", err)
		}
	} else {
		if err := s.channels.AddMember(ctx, member); err != nil {
			slog.ErrorContext(ctx, "failed to add channel member", "channel_id", channelID, "user_id", userID, "error", err)
			return cerrors.Internal("failed to add channel member", err)
		}

		s.publishEvent(ctx, event.TypeMemberJoined, ch.WorkspaceID, channelID, userID, event.MemberPayload{
			ChannelID: channelID,
			UserID:    userID,
		})
	}

	slog.InfoContext(ctx, "user joined channel", "channel_id", channelID, "user_id", userID)
	return nil
}

// LeaveChannel removes a user from a channel.
func (s *Service) LeaveChannel(ctx context.Context, channelID, userID uuid.UUID) error {
	ch, err := s.channels.GetByID(ctx, channelID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("channel not found")
		}
		slog.ErrorContext(ctx, "failed to get channel for leave", "channel_id", channelID, "error", err)
		return cerrors.Internal("failed to get channel", err)
	}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Channels() == nil {
				return cerrors.Unavailable("channel transaction scope is not configured")
			}
			if err := scope.Channels().RemoveMember(ctx, channelID, userID); err != nil {
				return err
			}
			return s.enqueueEventTx(ctx, scope, event.TypeMemberLeft, fmt.Sprintf("aloqa.chat.%s", channelID), ch.WorkspaceID, channelID, userID, event.MemberPayload{
				ChannelID: channelID,
				UserID:    userID,
			})
		}); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
				return cerrors.NotFound("user is not a member of this channel")
			}
			slog.ErrorContext(ctx, "failed to leave channel transaction", "channel_id", channelID, "user_id", userID, "error", err)
			return cerrors.Internal("failed to remove channel member", err)
		}
	} else {
		if err := s.channels.RemoveMember(ctx, channelID, userID); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
				return cerrors.NotFound("user is not a member of this channel")
			}
			slog.ErrorContext(ctx, "failed to remove channel member", "channel_id", channelID, "user_id", userID, "error", err)
			return cerrors.Internal("failed to remove channel member", err)
		}

		s.publishEvent(ctx, event.TypeMemberLeft, ch.WorkspaceID, channelID, userID, event.MemberPayload{
			ChannelID: channelID,
			UserID:    userID,
		})
	}

	slog.InfoContext(ctx, "user left channel", "channel_id", channelID, "user_id", userID)
	return nil
}

// SendMessage creates a new message in a channel after verifying membership.
func (s *Service) SendMessage(
	ctx context.Context,
	channelID, userID uuid.UUID,
	content string,
	parentID *uuid.UUID,
) (*entity.Message, error) {
	input := SendMessageInput{Content: content}
	if err := validate.Struct(input); err != nil {
		return nil, err
	}

	// Verify channel exists.
	decision, err := s.authorizeChannel(ctx, channelID, userID, accesspolicy.CapabilityParticipate)
	if err != nil {
		return nil, err
	}
	ch := decision.Channel

	if ch.Archived {
		return nil, cerrors.Forbidden("cannot send messages to an archived channel")
	}

	// If replying to a thread, verify parent message exists in the same channel.
	if parentID != nil {
		parent, err := s.messages.GetByID(ctx, *parentID)
		if err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
				return nil, cerrors.NotFound("parent message not found")
			}
			slog.ErrorContext(ctx, "failed to get parent message", "parent_id", *parentID, "error", err)
			return nil, cerrors.Internal("failed to get parent message", err)
		}
		if parent.ChannelID != channelID {
			return nil, cerrors.InvalidInput("parent message does not belong to this channel")
		}
	}

	now := time.Now()
	msg := &entity.Message{
		ID:        id.New(),
		ChannelID: channelID,
		UserID:    userID,
		ParentID:  parentID,
		Content:   content,
		Type:      entity.MessageTypeText,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Messages() == nil {
				return cerrors.Unavailable("message transaction scope is not configured")
			}
			if err := scope.Messages().Create(ctx, msg); err != nil {
				return err
			}
			if err := s.enqueueMessageSearchTx(ctx, scope, ch.WorkspaceID, channelID, msg); err != nil {
				return err
			}
			if err := s.enqueueEventTx(ctx, scope, event.TypeMessageCreated, fmt.Sprintf("aloqa.chat.%s", channelID), ch.WorkspaceID, channelID, userID, event.MessagePayload{Message: msg}); err != nil {
				return err
			}
			return s.enqueueEventTx(ctx, scope, event.TypeMessageCreated, fmt.Sprintf("aloqa.ws.%s", ch.WorkspaceID), ch.WorkspaceID, channelID, userID, event.MessagePayload{Message: msg})
		}); err != nil {
			slog.ErrorContext(ctx, "failed to create message transaction", "channel_id", channelID, "error", err)
			return nil, cerrors.Internal("failed to create message", err)
		}
	} else {
		if err := s.messages.Create(ctx, msg); err != nil {
			slog.ErrorContext(ctx, "failed to create message", "channel_id", channelID, "error", err)
			return nil, cerrors.Internal("failed to create message", err)
		}

		s.enqueueSearch(ctx, "index message", func() error {
			return s.search.IndexMessage(ctx, ch.WorkspaceID, channelID, msg.ID, msg.Content, msg.CreatedAt)
		})

		// Publish to channel-specific subject.
		s.publishEvent(ctx, event.TypeMessageCreated, ch.WorkspaceID, channelID, userID, event.MessagePayload{Message: msg})
		// Also publish to workspace subject for WebSocket distribution.
		s.publishToWorkspace(ctx, event.TypeMessageCreated, ch.WorkspaceID, channelID, userID, event.MessagePayload{Message: msg})
	}

	slog.InfoContext(ctx, "message sent", "message_id", msg.ID, "channel_id", channelID, "user_id", userID)
	return msg, nil
}

// GetMessage returns a single message by ID after verifying the caller can view its channel.
func (s *Service) GetMessage(ctx context.Context, messageID, userID uuid.UUID) (*entity.Message, error) {
	msg, _, err := s.requireMessageAccess(ctx, messageID, userID)
	if err != nil {
		return nil, err
	}
	return msg, nil
}

// GetMessages returns paginated messages for a channel after verifying membership.
func (s *Service) GetMessages(ctx context.Context, channelID, userID uuid.UUID, p pagination.Params) (pagination.Page[entity.Message], error) {
	p.Normalize()

	if _, err := s.GetAccessibleChannel(ctx, channelID, userID); err != nil {
		return pagination.Page[entity.Message]{}, err
	}

	// Fetch limit+1 to determine if there are more results.
	fetchParams := pagination.Params{Cursor: p.Cursor, Limit: p.Limit + 1}
	items, err := s.messages.ListByChannel(ctx, channelID, fetchParams)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list messages", "channel_id", channelID, "error", err)
		return pagination.Page[entity.Message]{}, cerrors.Internal("failed to list messages", err)
	}

	redactDeletedMessages(items)
	return buildMessagePage(items, p.Limit), nil
}

// GetPinnedMessages returns all pinned messages in a channel after verifying
// membership. Unpaginated by design — pin sets are expected to stay small.
func (s *Service) GetPinnedMessages(ctx context.Context, channelID, userID uuid.UUID) ([]entity.Message, error) {
	if _, err := s.GetAccessibleChannel(ctx, channelID, userID); err != nil {
		return nil, err
	}

	items, err := s.messages.ListPinned(ctx, channelID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list pinned messages", "channel_id", channelID, "error", err)
		return nil, cerrors.Internal("failed to list pinned messages", err)
	}

	return items, nil
}

// GetThreadReplies returns paginated replies to a parent message.
func (s *Service) GetThreadReplies(ctx context.Context, parentID, userID uuid.UUID, p pagination.Params) (pagination.Page[entity.Message], error) {
	p.Normalize()

	if _, _, err := s.requireMessageAccess(ctx, parentID, userID); err != nil {
		return pagination.Page[entity.Message]{}, err
	}

	fetchParams := pagination.Params{Cursor: p.Cursor, Limit: p.Limit + 1}
	items, err := s.messages.ListThreadReplies(ctx, parentID, fetchParams)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list thread replies", "parent_id", parentID, "error", err)
		return pagination.Page[entity.Message]{}, cerrors.Internal("failed to list thread replies", err)
	}

	redactDeletedMessages(items)
	return buildMessagePage(items, p.Limit), nil
}

// EditMessage updates message content after verifying ownership.
func (s *Service) EditMessage(ctx context.Context, messageID, userID uuid.UUID, content string) (*entity.Message, error) {
	input := SendMessageInput{Content: content}
	if err := validate.Struct(input); err != nil {
		return nil, err
	}

	msg, ch, err := s.requireOwnMessageAccess(ctx, messageID, userID, accesspolicy.CapabilityParticipate)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	msg.Content = content
	msg.Edited = true
	msg.EditedAt = &now
	msg.UpdatedAt = now

	workspaceID := uuid.Nil
	if ch != nil {
		workspaceID = ch.WorkspaceID
	}
	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Messages() == nil {
				return cerrors.Unavailable("message transaction scope is not configured")
			}
			if err := scope.Messages().Update(ctx, msg); err != nil {
				return err
			}
			if workspaceID != uuid.Nil {
				if err := s.enqueueMessageSearchTx(ctx, scope, workspaceID, msg.ChannelID, msg); err != nil {
					return err
				}
			}
			return s.enqueueEventTx(ctx, scope, event.TypeMessageUpdated, fmt.Sprintf("aloqa.chat.%s", msg.ChannelID), workspaceID, msg.ChannelID, userID, event.MessagePayload{Message: msg})
		}); err != nil {
			slog.ErrorContext(ctx, "failed to update message transaction", "message_id", messageID, "error", err)
			return nil, cerrors.Internal("failed to update message", err)
		}
	} else {
		if err := s.messages.Update(ctx, msg); err != nil {
			slog.ErrorContext(ctx, "failed to update message", "message_id", messageID, "error", err)
			return nil, cerrors.Internal("failed to update message", err)
		}

		s.enqueueSearch(ctx, "index message", func() error {
			if workspaceID == uuid.Nil {
				return nil
			}
			return s.search.IndexMessage(ctx, workspaceID, msg.ChannelID, msg.ID, msg.Content, msg.CreatedAt)
		})
		s.publishEvent(ctx, event.TypeMessageUpdated, workspaceID, msg.ChannelID, userID, event.MessagePayload{Message: msg})
	}

	slog.InfoContext(ctx, "message edited", "message_id", messageID, "user_id", userID)
	return msg, nil
}

// DeleteMessage soft-deletes a message after verifying ownership.
func (s *Service) DeleteMessage(ctx context.Context, messageID, userID uuid.UUID) error {
	msg, ch, err := s.requireOwnMessageAccess(ctx, messageID, userID, accesspolicy.CapabilityParticipate)
	if err != nil {
		return err
	}

	workspaceID := uuid.Nil
	if ch != nil {
		workspaceID = ch.WorkspaceID
	}
	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Messages() == nil {
				return cerrors.Unavailable("message transaction scope is not configured")
			}
			if err := scope.Messages().SoftDelete(ctx, messageID); err != nil {
				return err
			}
			deletedMsg, err := scope.Messages().GetByID(ctx, messageID)
			if err != nil {
				return err
			}
			tombstone := redactDeletedMessage(*deletedMsg)
			msg = &tombstone
			if workspaceID != uuid.Nil {
				if err := s.enqueueMessageDeleteSearchTx(ctx, scope, workspaceID, messageID); err != nil {
					return err
				}
				attachments, err := scope.Messages().ListAttachments(ctx, messageID)
				if err != nil {
					return err
				}
				for _, attachment := range attachments {
					if err := s.enqueueFileDeleteSearchTx(ctx, scope, workspaceID, attachment.ID); err != nil {
						return err
					}
				}
			}
			return s.enqueueEventTx(ctx, scope, event.TypeMessageDeleted, fmt.Sprintf("aloqa.chat.%s", msg.ChannelID), workspaceID, msg.ChannelID, userID, event.MessagePayload{Message: msg})
		}); err != nil {
			slog.ErrorContext(ctx, "failed to delete message transaction", "message_id", messageID, "error", err)
			return cerrors.Internal("failed to delete message", err)
		}
	} else {
		if err := s.messages.SoftDelete(ctx, messageID); err != nil {
			slog.ErrorContext(ctx, "failed to soft-delete message", "message_id", messageID, "error", err)
			return cerrors.Internal("failed to delete message", err)
		}
		deletedMsg, err := s.messages.GetByID(ctx, messageID)
		if err != nil {
			slog.ErrorContext(ctx, "failed to reload deleted message", "message_id", messageID, "error", err)
			return cerrors.Internal("failed to delete message", err)
		}
		tombstone := redactDeletedMessage(*deletedMsg)
		msg = &tombstone

		s.enqueueSearch(ctx, "delete message from search", func() error {
			if workspaceID == uuid.Nil {
				return nil
			}
			return s.search.DeleteMessage(ctx, workspaceID, messageID)
		})
		if workspaceID != uuid.Nil {
			attachments, err := s.messages.ListAttachments(ctx, messageID)
			if err != nil {
				slog.ErrorContext(ctx, "failed to list message attachments for search cleanup", "message_id", messageID, "error", err)
			} else {
				for _, attachment := range attachments {
					attachmentID := attachment.ID
					s.enqueueSearch(ctx, "delete attachment from search", func() error {
						return s.search.DeleteFile(ctx, workspaceID, attachmentID)
					})
				}
			}
		}
		s.publishEvent(ctx, event.TypeMessageDeleted, workspaceID, msg.ChannelID, userID, event.MessagePayload{Message: msg})
	}

	slog.InfoContext(ctx, "message deleted", "message_id", messageID, "user_id", userID)
	return nil
}

// validateEmoji checks that the emoji string is valid: non-empty, at most 32
// bytes, and consists only of valid UTF-8 runes.
func validateEmoji(emoji string) error {
	if emoji == "" {
		return cerrors.InvalidInput("emoji is required")
	}
	if len(emoji) > 32 {
		return cerrors.InvalidInput("emoji must be at most 32 bytes")
	}
	if !utf8.ValidString(emoji) {
		return cerrors.InvalidInput("emoji must be valid UTF-8")
	}
	return nil
}

// AddReaction adds an emoji reaction to a message.
func (s *Service) AddReaction(ctx context.Context, messageID, userID uuid.UUID, emoji string) error {
	if err := validateEmoji(emoji); err != nil {
		return err
	}

	msg, ch, err := s.requireMessageAccessWithCapability(ctx, messageID, userID, accesspolicy.CapabilityParticipate)
	if err != nil {
		return err
	}

	reaction := &entity.Reaction{
		ID:        id.New(),
		MessageID: messageID,
		UserID:    userID,
		Emoji:     emoji,
		CreatedAt: time.Now(),
	}

	if err := s.messages.AddReaction(ctx, reaction); err != nil {
		slog.ErrorContext(ctx, "failed to add reaction", "message_id", messageID, "emoji", emoji, "error", err)
		return cerrors.Internal("failed to add reaction", err)
	}

	s.publishEvent(ctx, event.TypeReactionAdded, ch.WorkspaceID, msg.ChannelID, userID, event.ReactionPayload{
		MessageID: messageID,
		ChannelID: msg.ChannelID,
		UserID:    userID,
		Emoji:     emoji,
	})

	return nil
}

// RemoveReaction removes an emoji reaction from a message.
func (s *Service) RemoveReaction(ctx context.Context, messageID, userID uuid.UUID, emoji string) error {
	msg, ch, err := s.requireMessageAccessWithCapability(ctx, messageID, userID, accesspolicy.CapabilityParticipate)
	if err != nil {
		return err
	}

	if err := s.messages.RemoveReaction(ctx, messageID, userID, emoji); err != nil {
		slog.ErrorContext(ctx, "failed to remove reaction", "message_id", messageID, "emoji", emoji, "error", err)
		return cerrors.Internal("failed to remove reaction", err)
	}

	s.publishEvent(ctx, event.TypeReactionRemoved, ch.WorkspaceID, msg.ChannelID, userID, event.ReactionPayload{
		MessageID: messageID,
		ChannelID: msg.ChannelID,
		UserID:    userID,
		Emoji:     emoji,
	})

	return nil
}

// PinMessage pins a message in its channel.
func (s *Service) PinMessage(ctx context.Context, messageID, userID uuid.UUID) error {
	msg, err := s.messages.GetByID(ctx, messageID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("message not found")
		}
		slog.ErrorContext(ctx, "failed to get message for pin", "message_id", messageID, "error", err)
		return cerrors.Internal("failed to get message", err)
	}

	if msg.Pinned {
		return cerrors.AlreadyExists("message is already pinned")
	}

	if _, err := s.authorizeChannel(ctx, msg.ChannelID, userID, accesspolicy.CapabilityParticipate); err != nil {
		return err
	}

	if err := s.messages.Pin(ctx, messageID, userID); err != nil {
		slog.ErrorContext(ctx, "failed to pin message", "message_id", messageID, "error", err)
		return cerrors.Internal("failed to pin message", err)
	}

	ch, _ := s.channels.GetByID(ctx, msg.ChannelID)
	workspaceID := uuid.Nil
	if ch != nil {
		workspaceID = ch.WorkspaceID
	}
	s.publishEvent(ctx, event.TypeMessagePinned, workspaceID, msg.ChannelID, userID, event.PinPayload{
		MessageID: messageID,
		ChannelID: msg.ChannelID,
		UserID:    userID,
	})

	slog.InfoContext(ctx, "message pinned", "message_id", messageID, "user_id", userID)
	return nil
}

// UnpinMessage unpins a message in its channel.
func (s *Service) UnpinMessage(ctx context.Context, messageID, userID uuid.UUID) error {
	msg, err := s.messages.GetByID(ctx, messageID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("message not found")
		}
		slog.ErrorContext(ctx, "failed to get message for unpin", "message_id", messageID, "error", err)
		return cerrors.Internal("failed to get message", err)
	}

	if !msg.Pinned {
		return cerrors.InvalidInput("message is not pinned")
	}

	if _, err := s.authorizeChannel(ctx, msg.ChannelID, userID, accesspolicy.CapabilityParticipate); err != nil {
		return err
	}

	if err := s.messages.Unpin(ctx, messageID); err != nil {
		slog.ErrorContext(ctx, "failed to unpin message", "message_id", messageID, "error", err)
		return cerrors.Internal("failed to unpin message", err)
	}

	ch, _ := s.channels.GetByID(ctx, msg.ChannelID)
	workspaceID := uuid.Nil
	if ch != nil {
		workspaceID = ch.WorkspaceID
	}
	s.publishEvent(ctx, event.TypeMessageUnpinned, workspaceID, msg.ChannelID, userID, event.PinPayload{
		MessageID: messageID,
		ChannelID: msg.ChannelID,
		UserID:    userID,
	})

	slog.InfoContext(ctx, "message unpinned", "message_id", messageID, "user_id", userID)
	return nil
}

// GetOrCreateDM finds an existing DM channel between two users or creates a new one.
func (s *Service) GetOrCreateDM(ctx context.Context, workspaceID, userA, userB uuid.UUID, targetWorkspaceID *uuid.UUID) (*entity.Channel, error) {
	if userA == userB {
		return nil, cerrors.InvalidInput("cannot create a DM with yourself")
	}

	if err := s.CanAccessWorkspace(ctx, workspaceID, userA); err != nil {
		return nil, err
	}

	crossWorkspace := false
	remoteWorkspaceID := workspaceID
	if targetWorkspaceID != nil && *targetWorkspaceID != uuid.Nil {
		remoteWorkspaceID = *targetWorkspaceID
	}
	if remoteWorkspaceID != workspaceID {
		crossWorkspace = true
	}

	if !crossWorkspace {
		if err := s.CanAccessWorkspace(ctx, workspaceID, userB); err != nil {
			return nil, cerrors.Forbidden("target user is not a member of this workspace")
		}
	} else {
		if err := s.CanAccessWorkspace(ctx, remoteWorkspaceID, userB); err != nil {
			return nil, cerrors.Forbidden("target user is not a member of the target workspace")
		}
		if s.contacts == nil || s.channelGrants == nil {
			return nil, cerrors.Unavailable("cross-workspace collaboration is unavailable")
		}
		if err := s.contacts.CanShareChannel(ctx, workspaceID, remoteWorkspaceID, userA, userB); err != nil {
			return nil, err
		}
	}

	// Try to find an existing DM channel.
	ch, err := s.channels.GetDMChannel(ctx, workspaceID, userA, userB)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
			slog.ErrorContext(ctx, "failed to look up DM channel", "user_a", userA, "user_b", userB, "error", err)
			return nil, cerrors.Internal("failed to look up DM channel", err)
		}
	}
	if ch != nil {
		if crossWorkspace {
			if err := s.ensureChannelAccessGrant(ctx, ch.ID, workspaceID, remoteWorkspaceID, userA, userB, true); err != nil {
				return nil, err
			}
		}
		if err := s.hydrateDMMembers(ctx, ch); err != nil {
			return nil, err
		}
		return ch, nil
	}

	now := time.Now()
	ch = &entity.Channel{
		ID:          id.New(),
		WorkspaceID: workspaceID,
		Name:        "",
		Type:        entity.ChannelTypeDM,
		CreatedBy:   userA,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	members := []*entity.ChannelMember{
		{
			ID:         id.New(),
			ChannelID:  ch.ID,
			UserID:     userA,
			Role:       entity.ChannelRoleMember,
			LastReadAt: now,
			JoinedAt:   now,
		},
		{
			ID:         id.New(),
			ChannelID:  ch.ID,
			UserID:     userB,
			Role:       entity.ChannelRoleMember,
			LastReadAt: now,
			JoinedAt:   now,
		},
	}

	ch.Members = []uuid.UUID{userA, userB}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Channels() == nil {
				return cerrors.Unavailable("channel transaction scope is not configured")
			}
			if err := scope.Channels().Create(ctx, ch); err != nil {
				return err
			}
			for _, member := range members {
				if err := scope.Channels().AddMember(ctx, member); err != nil {
					return err
				}
			}
			if crossWorkspace {
				if err := s.ensureChannelAccessGrantTx(ctx, scope, ch.ID, workspaceID, remoteWorkspaceID, userA, userB, true); err != nil {
					return err
				}
			}
			// Lazy DM visibility for the recipient: no channel.created
			// broadcast on the workspace subject. The creator already has the
			// channel via the API response (useOpenDm.onSuccess invalidates
			// their sidebar); the recipient discovers the DM only when the
			// first message.created event arrives on the workspace subject,
			// triggering a sidebar refresh on their side. An empty just-
			// opened DM stays invisible to the recipient until something is
			// actually said in it.
			return s.enqueueChannelSearchTx(ctx, scope, ch)
		}); err != nil {
			slog.ErrorContext(ctx, "failed to create DM channel transaction", "error", err)
			return nil, cerrors.Internal("failed to create DM channel", err)
		}
	} else {
		if err := s.channels.Create(ctx, ch); err != nil {
			slog.ErrorContext(ctx, "failed to create DM channel", "error", err)
			return nil, cerrors.Internal("failed to create DM channel", err)
		}

		for _, member := range members {
			if err := s.channels.AddMember(ctx, member); err != nil {
				slog.ErrorContext(ctx, "failed to add DM member", "channel_id", ch.ID, "user_id", member.UserID, "error", err)
				return nil, cerrors.Internal("failed to add DM member", err)
			}
		}

		if crossWorkspace {
			if err := s.ensureChannelAccessGrant(ctx, ch.ID, workspaceID, remoteWorkspaceID, userA, userB, true); err != nil {
				return nil, err
			}
		}

		s.enqueueSearch(ctx, "index channel", func() error {
			return s.search.IndexChannel(ctx, ch.WorkspaceID, ch.ID, ch.Name, ch.Topic, ch.CreatedAt, ch.UpdatedAt)
		})
		// Lazy DM visibility: see the matching comment in the tx path above.
	}
	slog.InfoContext(ctx, "DM channel created", "channel_id", ch.ID, "user_a", userA, "user_b", userB)
	return ch, nil
}

// --- Event helpers ---

func (s *Service) publishEvent(ctx context.Context, evtType event.Type, workspaceID, channelID, userID uuid.UUID, payload any) {
	subject := fmt.Sprintf("aloqa.chat.%s", channelID)
	s.doPublish(ctx, evtType, subject, workspaceID, channelID, userID, payload)
}

func (s *Service) publishToWorkspace(ctx context.Context, evtType event.Type, workspaceID, channelID, userID uuid.UUID, payload any) {
	subject := fmt.Sprintf("aloqa.ws.%s", workspaceID)
	s.doPublish(ctx, evtType, subject, workspaceID, channelID, userID, payload)
}

func (s *Service) enqueueSearch(ctx context.Context, action string, fn func() error) {
	if s.search == nil || fn == nil {
		return
	}
	if err := fn(); err != nil {
		slog.ErrorContext(ctx, "search enqueue failed", "action", action, "error", err)
	}
}

func (s *Service) enqueueMessageSearchTx(ctx context.Context, scope txscope.Scope, workspaceID, channelID uuid.UUID, msg *entity.Message) error {
	if scope == nil || scope.SearchIndexer() == nil || msg == nil {
		return nil
	}
	return scope.SearchIndexer().EnqueueUpsert(ctx, searchsvc.Document{
		WorkspaceID: workspaceID,
		ResourceID:  msg.ID,
		ChannelID:   &channelID,
		Type:        searchsvc.ResourceTypeMessage,
		Content:     msg.Content,
		CreatedAt:   msg.CreatedAt,
		UpdatedAt:   msg.UpdatedAt,
	})
}

func (s *Service) enqueueChannelSearchTx(ctx context.Context, scope txscope.Scope, ch *entity.Channel) error {
	if scope == nil || scope.SearchIndexer() == nil || ch == nil {
		return nil
	}
	return scope.SearchIndexer().EnqueueUpsert(ctx, searchsvc.Document{
		WorkspaceID: ch.WorkspaceID,
		ResourceID:  ch.ID,
		ChannelID:   &ch.ID,
		Type:        searchsvc.ResourceTypeChannel,
		Title:       ch.Name,
		Content:     ch.Topic,
		CreatedAt:   ch.CreatedAt,
		UpdatedAt:   ch.UpdatedAt,
		Metadata: map[string]any{
			"type":     string(ch.Type),
			"archived": ch.Archived,
		},
	})
}

func (s *Service) enqueueMessageDeleteSearchTx(ctx context.Context, scope txscope.Scope, workspaceID, messageID uuid.UUID) error {
	if scope == nil || scope.SearchIndexer() == nil {
		return nil
	}
	return scope.SearchIndexer().EnqueueDelete(ctx, workspaceID, searchsvc.ResourceTypeMessage, messageID)
}

func (s *Service) ensureChannelAccessGrantTx(ctx context.Context, scope txscope.Scope, channelID, workspaceID, remoteWorkspaceID, sourceUserID, targetUserID uuid.UUID, allowCalls bool) error {
	if scope == nil || scope.ChannelGrants() == nil {
		return s.ensureChannelAccessGrant(ctx, channelID, workspaceID, remoteWorkspaceID, sourceUserID, targetUserID, allowCalls)
	}
	_, err := scope.ChannelGrants().GetGrant(ctx, channelID, targetUserID)
	if err == nil {
		return nil
	}
	if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
		return err
	}
	grant := &entity.ChannelAccessGrant{
		ID:                id.New(),
		ChannelID:         channelID,
		WorkspaceID:       workspaceID,
		UserID:            targetUserID,
		SourceUserID:      sourceUserID,
		RemoteWorkspaceID: remoteWorkspaceID,
		Kind:              entity.ChannelAccessGrantKindCollaborationDM,
		AllowCalls:        allowCalls,
		CreatedAt:         time.Now().UTC(),
	}
	return scope.ChannelGrants().CreateGrant(ctx, grant)
}

func (s *Service) enqueueFileDeleteSearchTx(ctx context.Context, scope txscope.Scope, workspaceID, attachmentID uuid.UUID) error {
	if scope == nil || scope.SearchIndexer() == nil {
		return nil
	}
	return scope.SearchIndexer().EnqueueDelete(ctx, workspaceID, searchsvc.ResourceTypeFile, attachmentID)
}

func (s *Service) enqueueEventTx(ctx context.Context, scope txscope.Scope, evtType event.Type, subject string, workspaceID, channelID, userID uuid.UUID, payload any) error {
	if scope == nil {
		return cerrors.Unavailable("transaction scope is not configured")
	}
	evt, body, _, err := event.Prepare(subject, event.Event{
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

// --- Pagination helper ---

// MarkRead updates a user's last-read timestamp on a channel. This is used
// for read receipts and unread counting.
func (s *Service) MarkRead(ctx context.Context, channelID, userID uuid.UUID) error {
	decision, err := s.authorizeChannel(ctx, channelID, userID, accesspolicy.CapabilityParticipate)
	if err != nil {
		return err
	}

	if err := s.updateLastRead(ctx, decision, userID); err != nil {
		slog.ErrorContext(ctx, "failed to update last read", "channel_id", channelID, "user_id", userID, "error", err)
		return cerrors.Internal("failed to mark as read", err)
	}

	return nil
}

// UnreadCount represents the unread state for a single channel.
type UnreadCount struct {
	ChannelID   uuid.UUID `json:"channel_id"`
	UnreadCount int       `json:"unread_count"`
	LastReadAt  time.Time `json:"last_read_at"`
}

// GetUnreadCounts returns unread message counts for all channels the user
// belongs to in a workspace. The common case (users in channel_members) is
// served by a single batched SQL query; guest-only channels, whose read
// state lives in channel_access_state, fall back to the per-channel path.
func (s *Service) GetUnreadCounts(ctx context.Context, workspaceID, userID uuid.UUID) ([]UnreadCount, error) {
	channels, err := s.listChannelsForUnread(ctx, workspaceID, userID)
	if err != nil {
		return nil, err
	}

	summaries, err := s.messages.BatchUnreadCounts(ctx, workspaceID, userID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to batch unread counts", "workspace_id", workspaceID, "user_id", userID, "error", err)
		return nil, cerrors.Internal("failed to count unread", err)
	}
	covered := make(map[uuid.UUID]repository.UnreadSummary, len(summaries))
	for _, sum := range summaries {
		covered[sum.ChannelID] = sum
	}

	counts := make([]UnreadCount, 0, len(channels))
	for _, ch := range channels {
		if sum, ok := covered[ch.ID]; ok {
			if sum.Unread > 0 {
				counts = append(counts, UnreadCount{
					ChannelID:   ch.ID,
					UnreadCount: sum.Unread,
					LastReadAt:  sum.LastReadAt,
				})
			}
			continue
		}
		lastReadAt, err := s.lastReadAt(ctx, ch.ID, userID)
		if err != nil {
			continue
		}
		unread, err := s.messages.CountUnread(ctx, ch.ID, userID, lastReadAt)
		if err != nil {
			slog.ErrorContext(ctx, "failed to count unread", "channel_id", ch.ID, "error", err)
			continue
		}
		if unread > 0 {
			counts = append(counts, UnreadCount{
				ChannelID:   ch.ID,
				UnreadCount: unread,
				LastReadAt:  lastReadAt,
			})
		}
	}
	return counts, nil
}

func (s *Service) authorizeChannel(ctx context.Context, channelID, userID uuid.UUID, capability accesspolicy.Capability) (*accesspolicy.ChannelDecision, error) {
	if s.access != nil {
		return s.access.Channel(ctx, channelID, userID, capability)
	}
	ch, err := s.GetAccessibleChannel(ctx, channelID, userID)
	if err != nil {
		return nil, err
	}
	member, err := s.channels.GetMember(ctx, channelID, userID)
	if capability == accesspolicy.CapabilityParticipate {
		if err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
				return nil, cerrors.Forbidden("user is not a member of this channel")
			}
			return nil, cerrors.Internal("failed to check membership", err)
		}
	}
	if err != nil {
		member = nil
	}
	return &accesspolicy.ChannelDecision{
		Subject:       accesspolicy.SubjectMember,
		Channel:       ch,
		ChannelMember: member,
	}, nil
}

func (s *Service) updateLastRead(ctx context.Context, decision *accesspolicy.ChannelDecision, userID uuid.UUID) error {
	if decision == nil || decision.Channel == nil {
		return cerrors.NotFound("channel not found")
	}
	if decision.Subject == accesspolicy.SubjectMember && decision.ChannelMember != nil {
		return s.channels.UpdateLastRead(ctx, decision.Channel.ID, userID)
	}
	if s.readStates == nil {
		return nil
	}
	return s.readStates.UpsertState(ctx, &entity.ChannelAccessState{
		ChannelID:  decision.Channel.ID,
		UserID:     userID,
		LastReadAt: time.Now().UTC(),
	})
}

func (s *Service) lastReadAt(ctx context.Context, channelID, userID uuid.UUID) (time.Time, error) {
	member, err := s.channels.GetMember(ctx, channelID, userID)
	if err == nil {
		return member.LastReadAt, nil
	}
	if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
		return time.Time{}, err
	}
	if s.readStates == nil {
		return time.Time{}, nil
	}
	state, err := s.readStates.GetState(ctx, channelID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	return state.LastReadAt, nil
}

func (s *Service) listChannelsForUnread(ctx context.Context, workspaceID, userID uuid.UUID) ([]entity.Channel, error) {
	if s.access != nil {
		return s.access.ListChannels(ctx, workspaceID, userID, accesspolicy.CapabilityParticipate)
	}
	return s.ListChannels(ctx, workspaceID, userID)
}

func (s *Service) ensureCollaborationChannelAccess(ctx context.Context, ch *entity.Channel, userID uuid.UUID) (bool, error) {
	if ch.Type != entity.ChannelTypeDM && ch.Type != entity.ChannelTypeGroupDM {
		return true, nil
	}
	if s.collab == nil {
		return true, nil
	}

	decision, err := s.collab.AuthorizeChannel(ctx, ch.ID, userID)
	if err != nil {
		return false, err
	}
	if !decision.Managed {
		return true, nil
	}
	return decision.Allowed, nil
}

func (s *Service) ensureChannelAccessGrant(ctx context.Context, channelID, workspaceID, remoteWorkspaceID, sourceUserID, targetUserID uuid.UUID, allowCalls bool) error {
	_, err := s.channelGrants.GetGrant(ctx, channelID, targetUserID)
	if err == nil {
		return nil
	}
	if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
		slog.ErrorContext(ctx, "failed to look up channel access grant", "channel_id", channelID, "user_id", targetUserID, "error", err)
		return cerrors.Internal("failed to verify collaboration access", err)
	}

	grant := &entity.ChannelAccessGrant{
		ID:                id.New(),
		ChannelID:         channelID,
		WorkspaceID:       workspaceID,
		UserID:            targetUserID,
		SourceUserID:      sourceUserID,
		RemoteWorkspaceID: remoteWorkspaceID,
		Kind:              entity.ChannelAccessGrantKindCollaborationDM,
		AllowCalls:        allowCalls,
		CreatedAt:         time.Now().UTC(),
	}
	if err := s.channelGrants.CreateGrant(ctx, grant); err != nil {
		slog.ErrorContext(ctx, "failed to create channel access grant", "channel_id", channelID, "user_id", targetUserID, "error", err)
		return cerrors.Internal("failed to create collaboration access", err)
	}
	return nil
}

func redactDeletedMessages(items []entity.Message) {
	for i := range items {
		items[i] = redactDeletedMessage(items[i])
	}
}

func redactDeletedMessage(msg entity.Message) entity.Message {
	if msg.DeletedAt == nil {
		return msg
	}

	msg.Content = ""
	msg.Edited = false
	msg.EditedAt = nil
	msg.Pinned = false
	msg.PinnedBy = nil
	msg.PinnedAt = nil
	msg.Reactions = nil
	msg.Attachments = nil
	return msg
}

// buildMessagePage constructs a pagination.Page from a message slice fetched with limit+1.
func buildMessagePage(items []entity.Message, limit int) pagination.Page[entity.Message] {
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}

	var nextCursor string
	if hasMore && len(items) > 0 {
		nextCursor = pagination.EncodeCursor(items[len(items)-1].ID)
	}

	return pagination.Page[entity.Message]{
		Items:      items,
		NextCursor: nextCursor,
		HasMore:    hasMore,
	}
}
