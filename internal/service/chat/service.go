package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/event"
	"aloqa/internal/domain/repository"
	"aloqa/internal/middleware"
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

type messageMoveRepository interface {
	Move(ctx context.Context, msg *entity.Message) error
}

type dmRequestStatusRepository interface {
	UpdateDMRequestStatus(ctx context.Context, channelID, userID uuid.UUID, status entity.DMRequestStatus) error
}

type CollaborationAccessAuthorizer interface {
	AuthorizeChannel(ctx context.Context, channelID, userID uuid.UUID) (collabaccess.Decision, error)
}

type SearchIndexer interface {
	IndexMessage(ctx context.Context, workspaceID, channelID, messageID uuid.UUID, content string, createdAt time.Time) error
	DeleteMessage(ctx context.Context, workspaceID, messageID uuid.UUID) error
	DeleteFile(ctx context.Context, workspaceID, attachmentID uuid.UUID) error
	IndexChannel(ctx context.Context, workspaceID, channelID uuid.UUID, name, topic string, createdAt, updatedAt time.Time) error
	DeleteChannel(ctx context.Context, workspaceID, channelID uuid.UUID) error
}

type DirectoryChannelAction string

const (
	DirectoryChannelActionOpen    DirectoryChannelAction = "open"
	DirectoryChannelActionJoin    DirectoryChannelAction = "join"
	DirectoryChannelActionRequest DirectoryChannelAction = "request"
)

type DirectoryPerson struct {
	UserID      uuid.UUID            `json:"user_id"`
	DisplayName string               `json:"display_name"`
	Email       string               `json:"email"`
	AvatarURL   string               `json:"avatar_url,omitempty"`
	AvatarColor string               `json:"avatar_color"`
	Role        entity.WorkspaceRole `json:"role"`
	Position    *string              `json:"position"`
	Department  *string              `json:"department"`
}

type DirectoryChannel struct {
	ChannelID uuid.UUID              `json:"channel_id"`
	Name      string                 `json:"name"`
	Topic     *string                `json:"topic,omitempty"`
	Type      entity.ChannelType     `json:"type"`
	Action    DirectoryChannelAction `json:"action"`
}

type Directory struct {
	People   []DirectoryPerson  `json:"people"`
	Channels []DirectoryChannel `json:"channels"`
}

// Service handles chat channels, messaging, and real-time event distribution.
type Service struct {
	channels      repository.ChannelRepository
	messages      repository.MessageRepository
	files         repository.FileRepository
	members       repository.WorkspaceRepository
	channelGrants repository.ChannelAccessGrantRepository
	readStates    repository.ChannelAccessStateRepository
	pubsub        EventPublisher
	guests        *guestaccess.Checker
	collab        CollaborationAccessAuthorizer
	access        *accesspolicy.Checker
	search        SearchIndexer
	tx            txscope.Manager
	presence      presenceLister
	contacts      interface {
		CanShareChannel(ctx context.Context, sourceWorkspaceID, targetWorkspaceID, sourceUserID, targetUserID uuid.UUID) error
	}
}

// presenceLister resolves the currently-online members of a workspace, used to
// scope @here broadcast mentions. Optional (set via SetPresenceLister); when
// absent, @here adds no recipients (ALK broadcast mentions).
type presenceLister interface {
	OnlineMemberIDs(ctx context.Context, workspaceID uuid.UUID) ([]uuid.UUID, error)
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

// SetPresenceLister wires the presence source used to scope @here broadcast
// mentions to currently-online members.
func (s *Service) SetPresenceLister(presence presenceLister) {
	s.presence = presence
}

func requireChannelWorkspaceID(ch *entity.Channel) (uuid.UUID, error) {
	if ch == nil || ch.WorkspaceID == nil {
		return uuid.Nil, cerrors.NotFound("channel not found")
	}
	return *ch.WorkspaceID, nil
}

func channelWorkspaceIDOrNil(ch *entity.Channel) uuid.UUID {
	if ch == nil || ch.WorkspaceID == nil {
		return uuid.Nil
	}
	return *ch.WorkspaceID
}

func requestWorkspaceIDForMessage(ctx context.Context, ch *entity.Channel) (uuid.UUID, error) {
	if ch != nil && ch.WorkspaceID != nil {
		return *ch.WorkspaceID, nil
	}
	if ch != nil && ch.Type == entity.ChannelTypeSavedGlobal {
		workspaceID := middleware.WorkspaceIDFromContext(ctx)
		if workspaceID != uuid.Nil {
			return workspaceID, nil
		}
	}
	return requireChannelWorkspaceID(ch)
}

func (s *Service) SetChannelAccessStates(states repository.ChannelAccessStateRepository) {
	s.readStates = states
}

func (s *Service) SetFileRepository(files repository.FileRepository) {
	s.files = files
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
	workspaceID, err := requireChannelWorkspaceID(ch)
	if err != nil {
		return nil, err
	}

	if err := s.CanAccessWorkspace(ctx, workspaceID, userID); err == nil {
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
		allowed, err := s.guests.HasChannelAccess(ctx, workspaceID, channelID, userID)
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

// SendMessageInput validates message creation parameters. Content validation is
// conditional in SendMessage because comment-less forwards may be empty.
type SendMessageInput struct {
	Content string
	// Optional client-declared message kind. Only "file" is honored (an
	// attachment/media message whose text body may be empty); any other value
	// falls back to text. System messages are server-authored, never settable
	// by clients.
	Type            string
	ParentID        *uuid.UUID
	ForwardedFrom   *json.RawMessage
	QuotedMessageID *uuid.UUID
	QuotedSnapshot  *ParsedQuotedSnapshotInput
	ProfileShare    *ProfileShareInput
	FileIDs         []uuid.UUID
	// Optional client-supplied id, echoed on the created message for dedup (ALK-440).
	ClientMessageID *string
}

type ProfileShareInput struct {
	UserID uuid.UUID
}

type QuotedSnapshotInput struct {
	UserID          string  `json:"user_id"`
	ContentExcerpt  string  `json:"content_excerpt"`
	CreatedAt       string  `json:"created_at"`
	ParentMessageID *string `json:"parent_message_id,omitempty"`
}

type ParsedQuotedSnapshotInput struct {
	UserID          uuid.UUID
	ContentExcerpt  string
	CreatedAt       time.Time
	ParentMessageID *uuid.UUID
}

// EditMessageInput preserves the legacy content validation tag for edit flows.
type EditMessageInput struct {
	Content string `validate:"required,min=1,max=40000"`
}

// CreateChannel creates a new channel and adds the creator as owner.
func (s *Service) CreateChannel(
	ctx context.Context,
	workspaceID, userID uuid.UUID,
	name, topic string,
	chType entity.ChannelType,
	extraMemberIDs []uuid.UUID,
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
		WorkspaceID: &workspaceID,
		Name:        name,
		Topic:       &topic,
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
	extraMembers, err := s.buildInitialChannelMembers(ctx, workspaceID, userID, ch.ID, now, extraMemberIDs)
	if err != nil {
		return nil, err
	}
	initialMemberIDs := initialChannelMemberIDs(member, extraMembers)
	channelCreatedPayload := channelPayloadWithMembers(ch, initialMemberIDs)
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
			for _, extraMember := range extraMembers {
				if err := scope.Channels().AddMember(ctx, extraMember); err != nil {
					return err
				}
			}
			if err := s.enqueueChannelSearchTx(ctx, scope, ch); err != nil {
				return err
			}
			if err := s.enqueueEventTx(ctx, scope, event.TypeChannelCreated, fmt.Sprintf("aloqa.chat.%s", ch.ID), workspaceID, ch.ID, userID, channelCreatedPayload); err != nil {
				return err
			}
			return s.enqueueChannelCreatedVisibilityEventsTx(ctx, scope, workspaceID, userID, ch, initialMemberIDs, channelCreatedPayload)
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
		for _, extraMember := range extraMembers {
			if err := s.channels.AddMember(ctx, extraMember); err != nil {
				slog.ErrorContext(ctx, "failed to add initial channel member", "channel_id", ch.ID, "user_id", extraMember.UserID, "error", err)
				return nil, cerrors.Internal("failed to add initial channel member", err)
			}
		}

		if isSearchableChannel(ch) {
			s.enqueueSearch(ctx, "index channel", func() error {
				return s.search.IndexChannel(ctx, workspaceID, ch.ID, ch.Name, derefTopicOrEmpty(ch.Topic), ch.CreatedAt, ch.UpdatedAt)
			})
		}
		s.publishEvent(ctx, event.TypeChannelCreated, workspaceID, ch.ID, userID, channelCreatedPayload)
		s.publishChannelCreatedVisibilityEvents(ctx, workspaceID, userID, ch, initialMemberIDs, channelCreatedPayload)
	}

	slog.InfoContext(ctx, "channel created", "channel_id", ch.ID, "name", name, "type", chType)
	return ch, nil
}

func derefTopicOrEmpty(topic *string) string {
	if topic == nil {
		return ""
	}
	return *topic
}

func initialChannelMemberIDs(owner *entity.ChannelMember, extraMembers []*entity.ChannelMember) []uuid.UUID {
	memberIDs := make([]uuid.UUID, 0, 1+len(extraMembers))
	if owner != nil {
		memberIDs = append(memberIDs, owner.UserID)
	}
	for _, member := range extraMembers {
		if member != nil {
			memberIDs = append(memberIDs, member.UserID)
		}
	}
	return memberIDs
}

func channelMemberUserIDs(members []entity.ChannelMember) []uuid.UUID {
	memberIDs := make([]uuid.UUID, 0, len(members))
	for _, member := range members {
		memberIDs = append(memberIDs, member.UserID)
	}
	return memberIDs
}

func channelPayloadWithMembers(ch *entity.Channel, memberIDs []uuid.UUID) event.ChannelPayload {
	if ch == nil || len(memberIDs) == 0 {
		return event.ChannelPayload{Channel: ch}
	}
	clone := *ch
	clone.Members = append([]uuid.UUID(nil), memberIDs...)
	return event.ChannelPayload{Channel: &clone}
}

func channelPayloadWithoutMembers(ch *entity.Channel) event.ChannelPayload {
	if ch == nil {
		return event.ChannelPayload{Channel: nil}
	}
	clone := *ch
	clone.Members = nil
	return event.ChannelPayload{Channel: &clone}
}

func isSearchableChannel(ch *entity.Channel) bool {
	if ch == nil || ch.WorkspaceID == nil || ch.Archived {
		return false
	}
	if ch.Type != entity.ChannelTypePublic && ch.Type != entity.ChannelTypePrivate {
		return false
	}
	return strings.TrimSpace(ch.Name) != ""
}

func (s *Service) buildInitialChannelMembers(
	ctx context.Context,
	workspaceID, creatorID, channelID uuid.UUID,
	joinedAt time.Time,
	extraMemberIDs []uuid.UUID,
) ([]*entity.ChannelMember, error) {
	seen := map[uuid.UUID]struct{}{creatorID: {}}
	members := make([]*entity.ChannelMember, 0, len(extraMemberIDs))

	for _, memberID := range extraMemberIDs {
		if memberID == uuid.Nil {
			continue
		}
		if _, ok := seen[memberID]; ok {
			continue
		}
		if _, err := s.members.GetMember(ctx, workspaceID, memberID); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
				return nil, cerrors.Forbidden("selected user is not a member of this workspace")
			}
			return nil, cerrors.Internal("failed to verify selected channel member", err)
		}
		seen[memberID] = struct{}{}
		members = append(members, &entity.ChannelMember{
			ID:         id.New(),
			ChannelID:  channelID,
			UserID:     memberID,
			Role:       entity.ChannelRoleMember,
			LastReadAt: joinedAt,
			JoinedAt:   joinedAt,
		})
	}

	return members, nil
}

func (s *Service) UpdateChannel(ctx context.Context, channelID, userID uuid.UUID, name, topic string, archived *bool) (*entity.Channel, error) {
	// Unarchive bypass (ALK-617): an unarchive request (archived=false against
	// a currently-archived channel) must be allowed through the archived guard
	// below so users can recover channels from the Archived list view.
	// Name/topic edits remain forbidden until the channel is unarchived first.
	// Resolve with the management capability so an archived channel can still
	// be fetched here: the view/participate access guard hides archived
	// channels, which would 403 the unarchive request before the bypass below
	// runs. Owner/admin role is still enforced further down (ALK-1050).
	decision, err := s.authorizeChannel(ctx, channelID, userID, accesspolicy.CapabilityManage)
	if err != nil {
		return nil, err
	}
	ch := decision.Channel
	isUnarchive := archived != nil && !*archived && ch.Archived

	if !isUnarchive {
		input := CreateChannelInput{Name: name, Topic: topic}
		if err := validate.Struct(input); err != nil {
			return nil, err
		}
	}

	if ch.Type == entity.ChannelTypeDM {
		return nil, cerrors.Forbidden("direct messages cannot be renamed")
	}
	if ch.Archived && !isUnarchive {
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
		workspaceID, err := requireChannelWorkspaceID(ch)
		if err != nil {
			return nil, err
		}
		workspaceMember, err := s.members.GetMember(ctx, workspaceID, userID)
		if err != nil {
			return nil, cerrors.Internal("failed to verify workspace membership", err)
		}
		if workspaceMember.Role != entity.WorkspaceRoleOwner && workspaceMember.Role != entity.WorkspaceRoleAdmin {
			return nil, cerrors.Forbidden("insufficient permission to update channel")
		}
	}

	// Unarchive bypass preserves the existing Name and Topic instead of
	// copying the (validation-skipped) request fields onto the entity. This
	// closes the data-corruption hole flagged by review where a caller could
	// POST `{archived: false}` with no name/topic and land an empty name
	// (validation was skipped above to allow the unarchive flow).
	if !isUnarchive {
		ch.Name = name
		ch.Topic = &topic
	}
	if archived != nil {
		ch.Archived = *archived
	}
	workspaceID, err := requireChannelWorkspaceID(ch)
	if err != nil {
		return nil, err
	}
	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Channels() == nil {
				return cerrors.Unavailable("channel transaction scope is not configured")
			}
			if err := scope.Channels().Update(ctx, ch); err != nil {
				return err
			}
			if err := s.enqueueChannelSearchSyncTx(ctx, scope, ch); err != nil {
				return err
			}
			return s.enqueueEventTx(ctx, scope, event.TypeChannelUpdated, fmt.Sprintf("aloqa.chat.%s", ch.ID), workspaceID, ch.ID, userID, event.ChannelPayload{Channel: ch})
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

		s.enqueueChannelSearchSync(ctx, workspaceID, ch)
		s.publishEvent(ctx, event.TypeChannelUpdated, workspaceID, ch.ID, userID, event.ChannelPayload{Channel: ch})
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
		subject, accessErr := s.access.WorkspaceAccess(ctx, workspaceID, userID)
		if accessErr == nil && subject == accesspolicy.SubjectMember {
			result, err := s.channels.ListByUser(ctx, workspaceID, userID)
			if err != nil {
				slog.ErrorContext(ctx, "failed to list user channels", "workspace_id", workspaceID, "user_id", userID, "error", err)
				return nil, cerrors.Internal("failed to list channels", err)
			}
			filtered := make([]entity.Channel, 0, len(result))
			for _, ch := range result {
				if _, err := s.access.Channel(ctx, ch.ID, userID, accesspolicy.CapabilityView); err == nil {
					filtered = append(filtered, ch)
				} else if appErr, ok := cerrors.AsAppError(err); !ok || (appErr.Code != cerrors.CodeForbidden && appErr.Code != cerrors.CodeNotFound) {
					return nil, err
				}
			}
			channels = filtered
		} else {
			if accessErr != nil {
				if appErr, ok := cerrors.AsAppError(accessErr); !ok || appErr.Code != cerrors.CodeForbidden {
					return nil, accessErr
				}
			}
			result, err := s.access.ListChannels(ctx, workspaceID, userID, accesspolicy.CapabilityView)
			if err != nil {
				return nil, err
			}
			channels = result
		}
	} else {
		result, err := s.channels.ListByUser(ctx, workspaceID, userID)
		if err != nil {
			slog.ErrorContext(ctx, "failed to list user channels", "workspace_id", workspaceID, "user_id", userID, "error", err)
			return nil, cerrors.Internal("failed to list channels", err)
		}
		channels = result
	}

	// Archived visibility: hide archived channels from the main list (ALK-617).
	// They are surfaced exclusively via ListArchivedChannels so the sidebar
	// only renders live channels.
	visible := channels[:0]
	for _, ch := range channels {
		if !ch.Archived {
			visible = append(visible, ch)
		}
	}
	channels = visible

	ptrs := make([]*entity.Channel, len(channels))
	for i := range channels {
		ptrs[i] = &channels[i]
	}
	if err := s.hydrateDMMembers(ctx, ptrs...); err != nil {
		return nil, err
	}
	s.stampLastActivity(ctx, channels)
	return channels, nil
}

// lastActivityLister is implemented by the messages repository to fetch the
// most recent message timestamp per channel. Declared here (and consumed via a
// type assertion) so the sidebar last-activity ordering (ALK-837) adds no
// method to the broad MessageRepository interface.
type lastActivityLister interface {
	LastActivityByChannels(ctx context.Context, channelIDs []uuid.UUID) (map[uuid.UUID]time.Time, error)
}

// stampLastActivity fills each channel's LastActivityAt with the created_at of
// its most recent message so the client can order the sidebar by last activity
// (ALK-837). Best-effort: a lookup failure leaves the timestamps unset and the
// list still renders.
func (s *Service) stampLastActivity(ctx context.Context, channels []entity.Channel) {
	lister, ok := s.messages.(lastActivityLister)
	if !ok || len(channels) == 0 {
		return
	}

	ids := make([]uuid.UUID, len(channels))
	for i := range channels {
		ids[i] = channels[i].ID
	}

	activity, err := lister.LastActivityByChannels(ctx, ids)
	if err != nil {
		slog.WarnContext(ctx, "failed to load channel last activity", "error", err)
		return
	}

	for i := range channels {
		if at, found := activity[channels[i].ID]; found {
			stamped := at
			channels[i].LastActivityAt = &stamped
		}
	}
}

// ListArchivedChannels returns the archived channels the user is a member of
// in the given workspace, enriched with member count and last-activity
// timestamp so the Archived Channels list view (ALK-617) can render
// meaningful rows without per-row round-trips.
func (s *Service) ListArchivedChannels(ctx context.Context, workspaceID, userID uuid.UUID) ([]entity.ArchivedChannelInfo, error) {
	if err := s.CanAccessWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	infos, err := s.channels.ListArchivedByUser(ctx, workspaceID, userID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list archived channels", "workspace_id", workspaceID, "user_id", userID, "error", err)
		return nil, cerrors.Internal("failed to list archived channels", err)
	}
	if infos == nil {
		return []entity.ArchivedChannelInfo{}, nil
	}
	return infos, nil
}

func (s *Service) ListDirectory(ctx context.Context, workspaceID, userID uuid.UUID) (*Directory, error) {
	if err := s.CanAccessWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}

	members, err := s.listDirectoryMembers(ctx, workspaceID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list workspace directory members", "workspace_id", workspaceID, "user_id", userID, "error", err)
		return nil, cerrors.Internal("failed to list directory members", err)
	}

	channels, err := s.listDirectoryChannels(ctx, workspaceID, userID)
	if err != nil {
		return nil, err
	}

	directory := &Directory{
		People:   make([]DirectoryPerson, 0, len(members)),
		Channels: make([]DirectoryChannel, 0, len(channels)),
	}

	for _, member := range members {
		if member.UserID == userID || member.User == nil || member.User.Status != entity.UserStatusActive {
			continue
		}

		directory.People = append(directory.People, DirectoryPerson{
			UserID:      member.UserID,
			DisplayName: member.User.DisplayName,
			Email:       member.User.Email,
			AvatarURL:   member.User.AvatarURL,
			AvatarColor: member.User.AvatarColor,
			Role:        member.Role,
			Position:    member.User.Position,
			Department:  member.User.Department,
		})
	}

	for _, ch := range channels {
		if ch.Archived || (ch.Type != entity.ChannelTypePublic && ch.Type != entity.ChannelTypePrivate) {
			continue
		}

		action, err := s.directoryChannelAction(ctx, ch, userID)
		if err != nil {
			return nil, err
		}

		directory.Channels = append(directory.Channels, DirectoryChannel{
			ChannelID: ch.ID,
			Name:      ch.Name,
			Topic:     ch.Topic,
			Type:      ch.Type,
			Action:    action,
		})
	}

	return directory, nil
}

func (s *Service) listDirectoryMembers(ctx context.Context, workspaceID uuid.UUID) ([]entity.WorkspaceMember, error) {
	var (
		cursor  uuid.UUID
		members []entity.WorkspaceMember
	)

	for {
		page, err := s.members.ListMembers(ctx, workspaceID, pagination.Params{Cursor: cursor, Limit: pagination.MaxLimit}, "")
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return members, nil
		}

		members = append(members, page...)
		if len(page) <= pagination.MaxLimit {
			return members, nil
		}

		nextCursor := page[len(page)-1].ID
		if nextCursor == uuid.Nil || nextCursor == cursor {
			return members, nil
		}
		cursor = nextCursor
	}
}

func (s *Service) listDirectoryChannels(ctx context.Context, workspaceID, userID uuid.UUID) ([]entity.Channel, error) {
	if s.access != nil {
		channels, err := s.access.ListChannels(ctx, workspaceID, userID, accesspolicy.CapabilityView)
		if err != nil {
			return nil, err
		}
		return channels, nil
	}

	var (
		cursor   uuid.UUID
		channels []entity.Channel
	)

	for {
		page, err := s.channels.ListByWorkspace(ctx, workspaceID, pagination.Params{Cursor: cursor, Limit: pagination.MaxLimit})
		if err != nil {
			slog.ErrorContext(ctx, "failed to list workspace directory channels", "workspace_id", workspaceID, "user_id", userID, "error", err)
			return nil, cerrors.Internal("failed to list directory channels", err)
		}
		if len(page) == 0 {
			return channels, nil
		}

		channels = append(channels, page...)
		if len(page) <= pagination.MaxLimit {
			return channels, nil
		}

		nextCursor := page[len(page)-1].ID
		if nextCursor == uuid.Nil || nextCursor == cursor {
			return channels, nil
		}
		cursor = nextCursor
	}
}

func (s *Service) directoryChannelAction(ctx context.Context, ch entity.Channel, userID uuid.UUID) (DirectoryChannelAction, error) {
	_, err := s.channels.GetMember(ctx, ch.ID, userID)
	if err == nil {
		return DirectoryChannelActionOpen, nil
	}
	if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
		slog.ErrorContext(ctx, "failed to check directory channel membership", "channel_id", ch.ID, "user_id", userID, "error", err)
		return "", cerrors.Internal("failed to check directory channel membership", err)
	}

	if ch.Type == entity.ChannelTypePublic {
		return DirectoryChannelActionJoin, nil
	}
	return DirectoryChannelActionRequest, nil
}

// filterAbandonedRecipientDMs is kept for legacy callers that still need the
// pre-request-tab behavior. ListChannels now relies on explicit per-member DM
// request status instead.
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

	workspaceID, err := requireChannelWorkspaceID(ch)
	if err != nil {
		return err
	}
	if err := s.CanAccessWorkspace(ctx, workspaceID, userID); err != nil {
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
			return s.enqueueEventTx(ctx, scope, event.TypeMemberJoined, fmt.Sprintf("aloqa.chat.%s", channelID), workspaceID, channelID, userID, event.MemberPayload{
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

		s.publishEvent(ctx, event.TypeMemberJoined, workspaceID, channelID, userID, event.MemberPayload{
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
	workspaceID, err := requireChannelWorkspaceID(ch)
	if err != nil {
		return err
	}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Channels() == nil {
				return cerrors.Unavailable("channel transaction scope is not configured")
			}
			if err := scope.Channels().RemoveMember(ctx, channelID, userID); err != nil {
				return err
			}
			payload := event.MemberPayload{
				ChannelID: channelID,
				UserID:    userID,
			}
			if err := s.enqueueEventTx(ctx, scope, event.TypeMemberLeft, fmt.Sprintf("aloqa.chat.%s", channelID), workspaceID, channelID, userID, payload); err != nil {
				return err
			}
			return s.enqueueEventTx(ctx, scope, event.TypeMemberLeft, workspaceUserEventsSubject(workspaceID, userID), workspaceID, channelID, userID, payload)
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

		s.publishEvent(ctx, event.TypeMemberLeft, workspaceID, channelID, userID, event.MemberPayload{
			ChannelID: channelID,
			UserID:    userID,
		})
		s.publishToUserEvents(ctx, event.TypeMemberLeft, workspaceID, channelID, userID, event.MemberPayload{
			ChannelID: channelID,
			UserID:    userID,
		})
	}

	slog.InfoContext(ctx, "user left channel", "channel_id", channelID, "user_id", userID)
	return nil
}

func canManageChannelMembers(member *entity.ChannelMember) bool {
	return member != nil && (member.Role == entity.ChannelRoleOwner || member.Role == entity.ChannelRoleAdmin)
}

// ListChannelMembers returns channel membership for a channel the user can view.
func (s *Service) ListChannelMembers(ctx context.Context, channelID, userID uuid.UUID) ([]entity.ChannelMember, error) {
	if _, err := s.authorizeChannel(ctx, channelID, userID, accesspolicy.CapabilityView); err != nil {
		return nil, err
	}

	members, err := s.channels.ListMembers(ctx, channelID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list channel members", "channel_id", channelID, "user_id", userID, "error", err)
		return nil, cerrors.Internal("failed to list channel members", err)
	}

	return members, nil
}

const (
	defaultMentionLimit = 8
	maxMentionLimit     = 50
)

// mentionableMemberSearcher is implemented by the channels repository to search
// channel members for @mention autocomplete. Consumed via a type assertion so
// the broad ChannelRepository interface (and its many mocks) stays untouched
// (ALK-838).
type mentionableMemberSearcher interface {
	SearchMentionableMembers(
		ctx context.Context,
		channelID, excludeUserID uuid.UUID,
		query string,
		limit int,
	) ([]entity.MentionSuggestion, error)
}

type messageMentionResolver interface {
	ResolveMentions(ctx context.Context, channelID, authorID uuid.UUID, content string) ([]uuid.UUID, error)
}

type messageMentionBatchResolver interface {
	ResolveMentionsByMessageIDs(ctx context.Context, messageIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error)
}

// SearchChannelMentions returns members of the conversation matching the query
// for @mention autocomplete (ALK-838), excluding the requester. Works for both
// channels and direct messages (the DM's participants); the composer decides
// when to open the popup. Requires view access to the channel.
func (s *Service) SearchChannelMentions(
	ctx context.Context,
	channelID, userID uuid.UUID,
	query string,
	limit int,
) ([]entity.MentionSuggestion, error) {
	if _, err := s.authorizeChannel(ctx, channelID, userID, accesspolicy.CapabilityView); err != nil {
		return nil, err
	}

	if limit <= 0 {
		limit = defaultMentionLimit
	} else if limit > maxMentionLimit {
		limit = maxMentionLimit
	}

	searcher, ok := s.channels.(mentionableMemberSearcher)
	if !ok {
		return []entity.MentionSuggestion{}, nil
	}

	// Exclude the requesting user — you never @mention yourself.
	results, err := searcher.SearchMentionableMembers(ctx, channelID, userID, strings.TrimSpace(query), limit)
	if err != nil {
		slog.ErrorContext(ctx, "failed to search channel mentions", "channel_id", channelID, "error", err)
		return nil, cerrors.Internal("failed to search channel mentions", err)
	}

	return results, nil
}

func (s *Service) hydrateMessageMentions(ctx context.Context, msg *entity.Message, ch *entity.Channel) error {
	if msg == nil {
		return nil
	}
	msg.Mentions = []uuid.UUID{}
	if ch == nil || ch.Type == entity.ChannelTypeSaved || ch.Type == entity.ChannelTypeSavedGlobal || !strings.Contains(msg.Content, "@") {
		return nil
	}

	resolver, ok := s.messages.(messageMentionResolver)
	if !ok {
		return nil
	}

	mentions, err := resolver.ResolveMentions(ctx, msg.ChannelID, msg.UserID, msg.Content)
	if err != nil {
		slog.ErrorContext(ctx, "failed to resolve message mentions", "message_id", msg.ID, "channel_id", msg.ChannelID, "error", err)
		return cerrors.Internal("failed to resolve message mentions", err)
	}
	msg.Mentions = mentions

	// @here is scoped to members online at send time. Presence is transient, so
	// the recipient set is snapshotted on the message and merged into Mentions
	// (the realtime/feed mention contract) (ALK broadcast mentions).
	hereRecipients, err := s.resolveHereRecipients(ctx, msg, ch)
	if err != nil {
		return err
	}
	if len(hereRecipients) > 0 {
		msg.HereRecipients = hereRecipients
		msg.Mentions = mergeUniqueUUIDs(msg.Mentions, hereRecipients)
	}
	return nil
}

var hereMentionPattern = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_])@here([^A-Za-z0-9_.-]|$)`)

// resolveHereRecipients returns the channel members (excluding the author) who
// were online when an @here broadcast was sent. Best-effort: a presence lookup
// failure logs and yields no recipients rather than failing the send. Returns
// nil when there is no @here token or no presence source.
func (s *Service) resolveHereRecipients(
	ctx context.Context,
	msg *entity.Message,
	ch *entity.Channel,
) ([]uuid.UUID, error) {
	if s.presence == nil || ch == nil || ch.WorkspaceID == nil {
		return nil, nil
	}
	if !hereMentionPattern.MatchString(msg.Content) {
		return nil, nil
	}

	online, err := s.presence.OnlineMemberIDs(ctx, *ch.WorkspaceID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list online members for @here", "channel_id", msg.ChannelID, "error", err)
		return nil, nil
	}
	if len(online) == 0 {
		return nil, nil
	}
	onlineSet := make(map[uuid.UUID]struct{}, len(online))
	for _, id := range online {
		onlineSet[id] = struct{}{}
	}

	members, err := s.channels.ListMembers(ctx, msg.ChannelID)
	if err != nil {
		return nil, cerrors.Internal("failed to list channel members for @here", err)
	}

	recipients := make([]uuid.UUID, 0, len(members))
	for _, member := range members {
		if member.UserID == msg.UserID {
			continue
		}
		if _, ok := onlineSet[member.UserID]; ok {
			recipients = append(recipients, member.UserID)
		}
	}
	return recipients, nil
}

// mergeUniqueUUIDs appends extras to base, skipping IDs already present, and
// preserves order.
func mergeUniqueUUIDs(base, extras []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(base)+len(extras))
	for _, id := range base {
		seen[id] = struct{}{}
	}
	result := base
	for _, id := range extras {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

// AddChannelMembers adds workspace members to a channel when the actor can manage membership.
func (s *Service) AddChannelMembers(ctx context.Context, channelID, actorID uuid.UUID, targetIDs []uuid.UUID) ([]entity.ChannelMember, error) {
	decision, err := s.authorizeChannel(ctx, channelID, actorID, accesspolicy.CapabilityParticipate)
	if err != nil {
		return nil, err
	}
	if !canManageChannelMembers(decision.ChannelMember) {
		return nil, cerrors.Forbidden("only channel owners and admins can add members")
	}
	if decision.Channel.Type != entity.ChannelTypePublic && decision.Channel.Type != entity.ChannelTypePrivate {
		return nil, cerrors.Forbidden("members can only be managed for workspace channels")
	}
	workspaceID, err := requireChannelWorkspaceID(decision.Channel)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	members := make([]entity.ChannelMember, 0, len(targetIDs))
	seen := make(map[uuid.UUID]struct{}, len(targetIDs))
	for _, targetID := range targetIDs {
		if targetID == uuid.Nil {
			continue
		}
		if _, ok := seen[targetID]; ok {
			continue
		}
		seen[targetID] = struct{}{}
		if targetID == actorID {
			continue
		}
		if _, err := s.members.GetMember(ctx, workspaceID, targetID); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
				return nil, cerrors.Forbidden("selected user is not a member of this workspace")
			}
			slog.ErrorContext(ctx, "failed to verify selected channel member", "workspace_id", workspaceID, "user_id", targetID, "error", err)
			return nil, cerrors.Internal("failed to verify selected channel member", err)
		}
		if _, err := s.channels.GetMember(ctx, channelID, targetID); err == nil {
			continue
		} else if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
			slog.ErrorContext(ctx, "failed to check existing channel member", "channel_id", channelID, "user_id", targetID, "error", err)
			return nil, cerrors.Internal("failed to check existing channel member", err)
		}
		members = append(members, entity.ChannelMember{
			ID:         id.New(),
			ChannelID:  channelID,
			UserID:     targetID,
			Role:       entity.ChannelRoleMember,
			LastReadAt: now,
			JoinedAt:   now,
		})
	}

	if len(members) == 0 {
		return nil, nil
	}

	addMembers := func(ctx context.Context, channels repository.ChannelRepository) error {
		for index := range members {
			if err := channels.AddMember(ctx, &members[index]); err != nil {
				return err
			}
		}
		return nil
	}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Channels() == nil {
				return cerrors.Unavailable("channel transaction scope is not configured")
			}
			if err := addMembers(ctx, scope.Channels()); err != nil {
				return err
			}
			for _, member := range members {
				if err := s.enqueueEventTx(ctx, scope, event.TypeMemberJoined, fmt.Sprintf("aloqa.chat.%s", channelID), workspaceID, channelID, member.UserID, event.MemberPayload{
					ChannelID: channelID,
					UserID:    member.UserID,
				}); err != nil {
					return err
				}
			}
			memberIDs, err := currentChannelMemberIDs(ctx, scope.Channels(), channelID)
			if err != nil {
				return err
			}
			payload := channelPayloadWithMembers(decision.Channel, memberIDs)
			return s.enqueueChannelCreatedVisibilityEventsTx(ctx, scope, workspaceID, actorID, decision.Channel, channelMemberUserIDs(members), payload)
		}); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return nil, appErr
			}
			slog.ErrorContext(ctx, "failed to add channel members transaction", "channel_id", channelID, "actor_id", actorID, "error", err)
			return nil, cerrors.Internal("failed to add channel members", err)
		}
	} else if err := addMembers(ctx, s.channels); err != nil {
		slog.ErrorContext(ctx, "failed to add channel members", "channel_id", channelID, "actor_id", actorID, "error", err)
		return nil, cerrors.Internal("failed to add channel members", err)
	} else {
		memberIDs, err := currentChannelMemberIDs(ctx, s.channels, channelID)
		if err != nil {
			slog.ErrorContext(ctx, "failed to list channel members for realtime payload", "channel_id", channelID, "actor_id", actorID, "error", err)
			return nil, cerrors.Internal("failed to list channel members", err)
		}
		payload := channelPayloadWithMembers(decision.Channel, memberIDs)
		for _, member := range members {
			s.publishEvent(ctx, event.TypeMemberJoined, workspaceID, channelID, member.UserID, event.MemberPayload{
				ChannelID: channelID,
				UserID:    member.UserID,
			})
		}
		s.publishChannelCreatedVisibilityEvents(ctx, workspaceID, actorID, decision.Channel, channelMemberUserIDs(members), payload)
	}

	slog.InfoContext(ctx, "channel members added", "channel_id", channelID, "actor_id", actorID, "count", len(members))
	return members, nil
}

// RemoveChannelMember removes another member from a channel when the actor can manage membership.
func (s *Service) RemoveChannelMember(ctx context.Context, channelID, actorID, targetID uuid.UUID) error {
	if actorID == targetID {
		return cerrors.Forbidden("use leave channel to remove yourself")
	}
	decision, err := s.authorizeChannel(ctx, channelID, actorID, accesspolicy.CapabilityParticipate)
	if err != nil {
		return err
	}
	if !canManageChannelMembers(decision.ChannelMember) {
		return cerrors.Forbidden("only channel owners and admins can remove members")
	}
	if decision.Channel.Type != entity.ChannelTypePublic && decision.Channel.Type != entity.ChannelTypePrivate {
		return cerrors.Forbidden("members can only be managed for workspace channels")
	}
	workspaceID, err := requireChannelWorkspaceID(decision.Channel)
	if err != nil {
		return err
	}
	targetMember, err := s.channels.GetMember(ctx, channelID, targetID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("channel member not found")
		}
		slog.ErrorContext(ctx, "failed to get target channel member", "channel_id", channelID, "user_id", targetID, "error", err)
		return cerrors.Internal("failed to get channel member", err)
	}
	if targetMember.Role == entity.ChannelRoleOwner {
		return cerrors.Forbidden("channel owner cannot be removed")
	}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Channels() == nil {
				return cerrors.Unavailable("channel transaction scope is not configured")
			}
			if err := scope.Channels().RemoveMember(ctx, channelID, targetID); err != nil {
				return err
			}
			payload := event.MemberPayload{
				ChannelID: channelID,
				UserID:    targetID,
			}
			if err := s.enqueueEventTx(ctx, scope, event.TypeMemberLeft, fmt.Sprintf("aloqa.chat.%s", channelID), workspaceID, channelID, targetID, payload); err != nil {
				return err
			}
			return s.enqueueEventTx(ctx, scope, event.TypeMemberLeft, workspaceUserEventsSubject(workspaceID, targetID), workspaceID, channelID, targetID, payload)
		}); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return appErr
			}
			slog.ErrorContext(ctx, "failed to remove channel member transaction", "channel_id", channelID, "actor_id", actorID, "target_id", targetID, "error", err)
			return cerrors.Internal("failed to remove channel member", err)
		}
	} else if err := s.channels.RemoveMember(ctx, channelID, targetID); err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("channel member not found")
		}
		slog.ErrorContext(ctx, "failed to remove channel member", "channel_id", channelID, "actor_id", actorID, "target_id", targetID, "error", err)
		return cerrors.Internal("failed to remove channel member", err)
	} else {
		s.publishEvent(ctx, event.TypeMemberLeft, workspaceID, channelID, targetID, event.MemberPayload{
			ChannelID: channelID,
			UserID:    targetID,
		})
		s.publishToUserEvents(ctx, event.TypeMemberLeft, workspaceID, channelID, targetID, event.MemberPayload{
			ChannelID: channelID,
			UserID:    targetID,
		})
	}

	slog.InfoContext(ctx, "channel member removed", "channel_id", channelID, "actor_id", actorID, "target_id", targetID)
	return nil
}

func (s *Service) buildProfileShare(
	ctx context.Context,
	ch *entity.Channel,
	workspaceID uuid.UUID,
	input *ProfileShareInput,
) (*entity.ProfileShare, error) {
	if input == nil {
		return nil, nil
	}
	if input.UserID == uuid.Nil {
		return nil, cerrors.InvalidInput("profile_share.user_id is required")
	}
	if !canSendProfileShareToChannel(ch.Type) {
		return nil, cerrors.Forbidden("profile shares cannot be sent to this channel")
	}

	member, err := s.members.GetMember(ctx, workspaceID, input.UserID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.Forbidden("shared profile is not a member of this workspace")
		}
		slog.ErrorContext(ctx, "failed to verify shared profile workspace membership", "workspace_id", workspaceID, "user_id", input.UserID, "error", err)
		return nil, cerrors.Internal("failed to verify shared profile", err)
	}
	if member.User == nil {
		return nil, cerrors.Internal("failed to hydrate shared profile", fmt.Errorf("workspace member %s has no user", member.UserID))
	}
	if member.User.Status != entity.UserStatusActive {
		return nil, cerrors.Forbidden("shared profile is not active")
	}

	return &entity.ProfileShare{
		UserID:      member.UserID,
		WorkspaceID: workspaceID,
		Snapshot: entity.ProfileShareSnapshot{
			DisplayName: member.User.DisplayName,
			AvatarURL:   member.User.AvatarURL,
			AvatarColor: member.User.AvatarColor,
			Role:        member.Role,
			Position:    member.User.Position,
			Department:  member.User.Department,
		},
	}, nil
}

func canSendProfileShareToChannel(channelType entity.ChannelType) bool {
	switch channelType {
	case entity.ChannelTypeDM, entity.ChannelTypeGroupDM, entity.ChannelTypePublic, entity.ChannelTypePrivate:
		return true
	default:
		return false
	}
}

// resolveSendMessageType maps an optional client-declared message kind to the
// stored type. Only "file" is honored (a media/attachment message whose text
// body may be empty because the attachment is uploaded after the row exists);
// any unset or "text" value stays text. Clients may not author "system"
// messages, so any other value is rejected.
func resolveSendMessageType(raw string) (entity.MessageType, error) {
	switch raw {
	case "", string(entity.MessageTypeText):
		return entity.MessageTypeText, nil
	case string(entity.MessageTypeFile):
		return entity.MessageTypeFile, nil
	default:
		return "", cerrors.InvalidInput("type must be 'text' or 'file'")
	}
}

// SendMessage creates a new message in a channel after verifying membership.
func (s *Service) SendMessage(
	ctx context.Context,
	channelID, userID uuid.UUID,
	input SendMessageInput,
) (*entity.Message, error) {
	if err := validate.Struct(input); err != nil {
		return nil, err
	}
	msgType, err := resolveSendMessageType(input.Type)
	if err != nil {
		return nil, err
	}
	contentLen := utf8.RuneCountInString(input.Content)
	// Empty content is allowed when the message carries forwarded content
	// (ForwardedFrom) OR a quoted snapshot (Share message flow — the source
	// message becomes a quote and the author may omit their own text) OR a
	// profile share card (ALK-708) OR file references (FileIDs) OR the client
	// declares a file/media message (Type == file) whose attachment is uploaded
	// after the message row is created (pasted images, voice notes — ALK-905).
	if msgType != entity.MessageTypeFile && (input.ForwardedFrom == nil || len(*input.ForwardedFrom) == 0) && input.QuotedSnapshot == nil && input.ProfileShare == nil && len(input.FileIDs) == 0 && contentLen < 1 {
		return nil, cerrors.InvalidInput("content is required")
	}
	if contentLen > 40000 {
		return nil, cerrors.InvalidInput("content must be at most 40000 characters")
	}
	if input.ForwardedFrom != nil && len(*input.ForwardedFrom) > 0 {
		var probe interface{}
		if err := json.Unmarshal(*input.ForwardedFrom, &probe); err != nil {
			return nil, cerrors.InvalidInput("forwarded_from must be valid JSON")
		}
	}
	if (input.QuotedMessageID == nil) != (input.QuotedSnapshot == nil) {
		return nil, cerrors.InvalidInput("quoted_message_id and quoted_snapshot must be set together")
	}

	var quotedSnapshot *entity.QuotedSnapshot
	if input.QuotedSnapshot != nil {
		if utf8.RuneCountInString(input.QuotedSnapshot.ContentExcerpt) > 200 {
			return nil, cerrors.InvalidInput("quoted_snapshot.content_excerpt must be at most 200 characters")
		}
		quotedSnapshot = &entity.QuotedSnapshot{
			UserID:          input.QuotedSnapshot.UserID,
			ContentExcerpt:  input.QuotedSnapshot.ContentExcerpt,
			CreatedAt:       input.QuotedSnapshot.CreatedAt,
			Deleted:         nil,
			ParentMessageID: input.QuotedSnapshot.ParentMessageID,
		}
	}

	// Verify channel exists.
	decision, err := s.authorizeChannel(ctx, channelID, userID, accesspolicy.CapabilityParticipate)
	if err != nil {
		return nil, err
	}
	ch := decision.Channel
	workspaceID, err := requestWorkspaceIDForMessage(ctx, ch)
	if err != nil {
		return nil, err
	}

	if ch.Archived {
		return nil, cerrors.Forbidden("cannot send messages to an archived channel")
	}

	profileShare, err := s.buildProfileShare(ctx, ch, workspaceID, input.ProfileShare)
	if err != nil {
		return nil, err
	}

	// If replying to a thread, verify parent message exists in the same channel.
	if input.ParentID != nil {
		parent, err := s.messages.GetByID(ctx, *input.ParentID)
		if err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
				return nil, cerrors.NotFound("parent message not found")
			}
			slog.ErrorContext(ctx, "failed to get parent message", "parent_id", *input.ParentID, "error", err)
			return nil, cerrors.Internal("failed to get parent message", err)
		}
		if parent.ChannelID != channelID {
			return nil, cerrors.InvalidInput("parent message does not belong to this channel")
		}
	}

	now := time.Now()
	var forwardedFrom json.RawMessage
	if input.ForwardedFrom != nil {
		forwardedFrom = *input.ForwardedFrom
	}
	msg := &entity.Message{
		ID:              id.New(),
		ChannelID:       channelID,
		UserID:          userID,
		ParentID:        input.ParentID,
		Content:         input.Content,
		Type:            msgType,
		ForwardedFrom:   forwardedFrom,
		QuotedMessageID: input.QuotedMessageID,
		QuotedSnapshot:  quotedSnapshot,
		ProfileShare:    profileShare,
		FileIDs:         append([]uuid.UUID(nil), input.FileIDs...),
		CreatedAt:       now,
		UpdatedAt:       now,
		// Transient echo only — not a persisted column (see entity.Message).
		ClientMessageID: input.ClientMessageID,
	}

	if err := s.hydrateMessageMentions(ctx, msg, ch); err != nil {
		return nil, err
	}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Messages() == nil {
				return cerrors.Unavailable("message transaction scope is not configured")
			}
			if err := s.shareMessageFilesTx(ctx, scope, msg.FileIDs, workspaceID, channelID, userID); err != nil {
				return err
			}
			if err := s.hydrateMessageFilesTx(ctx, scope, msg); err != nil {
				return err
			}
			if err := scope.Messages().Create(ctx, msg); err != nil {
				return err
			}
			if err := s.enqueueMessageSearchTx(ctx, scope, workspaceID, channelID, msg); err != nil {
				return err
			}
			if err := s.enqueueEventTx(ctx, scope, event.TypeMessageCreated, fmt.Sprintf("aloqa.chat.%s", channelID), workspaceID, channelID, userID, event.NewMessagePayload(msg, ch)); err != nil {
				return err
			}
			return s.enqueueEventTx(ctx, scope, event.TypeMessageCreated, fmt.Sprintf("aloqa.ws.%s", workspaceID), workspaceID, channelID, userID, event.NewMessagePayload(msg, ch))
		}); err != nil {
			slog.ErrorContext(ctx, "failed to create message transaction", "channel_id", channelID, "error", err)
			if appErr, ok := cerrors.AsAppError(err); ok {
				return nil, appErr
			}
			return nil, cerrors.Internal("failed to create message", err)
		}
	} else {
		if err := s.shareMessageFiles(ctx, msg.FileIDs, workspaceID, channelID, userID); err != nil {
			return nil, err
		}
		if err := s.hydrateMessageFilesForSend(ctx, msg); err != nil {
			return nil, err
		}
		if err := s.messages.Create(ctx, msg); err != nil {
			s.revokeMessageFileSharesBestEffort(ctx, msg.FileIDs, workspaceID, channelID, userID)
			slog.ErrorContext(ctx, "failed to create message", "channel_id", channelID, "error", err)
			return nil, cerrors.Internal("failed to create message", err)
		}

		s.enqueueSearch(ctx, "index message", func() error {
			return s.search.IndexMessage(ctx, workspaceID, channelID, msg.ID, msg.Content, msg.CreatedAt)
		})

		// Publish to channel-specific subject.
		s.publishEvent(ctx, event.TypeMessageCreated, workspaceID, channelID, userID, event.NewMessagePayload(msg, ch))
		// Also publish to workspace subject for WebSocket distribution.
		s.publishToWorkspace(ctx, event.TypeMessageCreated, workspaceID, channelID, userID, event.NewMessagePayload(msg, ch))
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
	items := []entity.Message{*msg}
	if err := s.hydrateMessageReactions(ctx, items); err != nil {
		return nil, err
	}
	if err := s.hydrateMessageMentionsForRead(ctx, items); err != nil {
		return nil, err
	}
	if err := s.hydrateMessageAttachments(ctx, items); err != nil {
		return nil, err
	}
	if err := s.hydrateMessageFiles(ctx, items); err != nil {
		return nil, err
	}
	if err := s.hydrateMessageAttachments(ctx, items); err != nil {
		return nil, err
	}
	*msg = items[0]
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

	page := buildMessagePage(items, p.Limit)
	if err := s.hydrateMessageReactions(ctx, page.Items); err != nil {
		return pagination.Page[entity.Message]{}, err
	}
	if err := s.hydrateMessageMentionsForRead(ctx, page.Items); err != nil {
		return pagination.Page[entity.Message]{}, err
	}
	if err := s.hydrateMessageAttachments(ctx, page.Items); err != nil {
		return pagination.Page[entity.Message]{}, err
	}
	if err := s.hydrateMessageFiles(ctx, page.Items); err != nil {
		return pagination.Page[entity.Message]{}, err
	}
	if err := s.hydrateMessageAttachments(ctx, page.Items); err != nil {
		return pagination.Page[entity.Message]{}, err
	}
	redactDeletedMessages(page.Items)
	return page, nil
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

	if err := s.hydrateMessageReactions(ctx, items); err != nil {
		return nil, err
	}
	if err := s.hydrateMessageMentionsForRead(ctx, items); err != nil {
		return nil, err
	}
	if err := s.hydrateMessageAttachments(ctx, items); err != nil {
		return nil, err
	}
	if err := s.hydrateMessageFiles(ctx, items); err != nil {
		return nil, err
	}
	if err := s.hydrateMessageAttachments(ctx, items); err != nil {
		return nil, err
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

	page := buildMessagePage(items, p.Limit)
	if err := s.hydrateMessageReactions(ctx, page.Items); err != nil {
		return pagination.Page[entity.Message]{}, err
	}
	if err := s.hydrateMessageMentionsForRead(ctx, page.Items); err != nil {
		return pagination.Page[entity.Message]{}, err
	}
	if err := s.hydrateMessageAttachments(ctx, page.Items); err != nil {
		return pagination.Page[entity.Message]{}, err
	}
	if err := s.hydrateMessageFiles(ctx, page.Items); err != nil {
		return pagination.Page[entity.Message]{}, err
	}
	if err := s.hydrateMessageAttachments(ctx, page.Items); err != nil {
		return pagination.Page[entity.Message]{}, err
	}
	redactDeletedMessages(page.Items)
	return page, nil
}

// EditMessage updates message content after verifying ownership.
func (s *Service) EditMessage(ctx context.Context, messageID, userID uuid.UUID, content string) (*entity.Message, error) {
	input := EditMessageInput{Content: content}
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

	workspaceID := channelWorkspaceIDOrNil(ch)
	if err := s.hydrateMessageMentions(ctx, msg, ch); err != nil {
		return nil, err
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
			if err := s.enqueueEventTx(ctx, scope, event.TypeMessageUpdated, fmt.Sprintf("aloqa.chat.%s", msg.ChannelID), workspaceID, msg.ChannelID, userID, event.NewMessagePayload(msg, ch)); err != nil {
				return err
			}
			// Also publish to the workspace subject so clients viewing OTHER
			// channels receive the edit (they only join the active channel room).
			// Mirrors MessageCreated; FE apply handlers are idempotent (ALK-654).
			return s.enqueueEventTx(ctx, scope, event.TypeMessageUpdated, fmt.Sprintf("aloqa.ws.%s", workspaceID), workspaceID, msg.ChannelID, userID, event.NewMessagePayload(msg, ch))
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
		s.publishEvent(ctx, event.TypeMessageUpdated, workspaceID, msg.ChannelID, userID, event.NewMessagePayload(msg, ch))
		s.publishToWorkspace(ctx, event.TypeMessageUpdated, workspaceID, msg.ChannelID, userID, event.NewMessagePayload(msg, ch))
	}

	slog.InfoContext(ctx, "message edited", "message_id", messageID, "user_id", userID)
	return msg, nil
}

// RemoveMessageFile detaches ONE file reference from a message, leaving the
// other attachments intact (ALK-1114). Author-only. The underlying library file
// is not deleted — it stays in the user's File Manager; only this message's
// reference to it is dropped, and channel shares are intentionally left in place
// so a sibling message referencing the same file keeps working. If removing the
// reference would leave the message with no content and no files, the whole
// message is soft-deleted instead of leaving an empty bubble. Returns the
// updated message, or nil when the message was deleted.
func (s *Service) RemoveMessageFile(ctx context.Context, messageID, fileID, userID uuid.UUID) (*entity.Message, error) {
	msg, ch, err := s.requireOwnMessageAccess(ctx, messageID, userID, accesspolicy.CapabilityParticipate)
	if err != nil {
		return nil, err
	}

	remaining := make([]uuid.UUID, 0, len(msg.FileIDs))
	found := false
	for _, id := range msg.FileIDs {
		if id == fileID {
			found = true
			continue
		}
		remaining = append(remaining, id)
	}
	if !found {
		return nil, cerrors.NotFound("file is not attached to this message")
	}

	// Nothing meaningful would remain — drop the whole message rather than leave
	// an empty bubble.
	if len(remaining) == 0 && strings.TrimSpace(msg.Content) == "" {
		if err := s.DeleteMessage(ctx, messageID, userID); err != nil {
			return nil, err
		}
		return nil, nil
	}

	msg.FileIDs = remaining
	workspaceID := channelWorkspaceIDOrNil(ch)

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Messages() == nil {
				return cerrors.Unavailable("message transaction scope is not configured")
			}
			if err := scope.Messages().UpdateFileIDs(ctx, msg.ID, remaining); err != nil {
				return err
			}
			if err := s.hydrateMessageFilesTx(ctx, scope, msg); err != nil {
				return err
			}
			if err := s.enqueueEventTx(ctx, scope, event.TypeMessageUpdated, fmt.Sprintf("aloqa.chat.%s", msg.ChannelID), workspaceID, msg.ChannelID, userID, event.NewMessagePayload(msg, ch)); err != nil {
				return err
			}
			return s.enqueueEventTx(ctx, scope, event.TypeMessageUpdated, fmt.Sprintf("aloqa.ws.%s", workspaceID), workspaceID, msg.ChannelID, userID, event.NewMessagePayload(msg, ch))
		}); err != nil {
			slog.ErrorContext(ctx, "failed to remove message file transaction", "message_id", messageID, "file_id", fileID, "error", err)
			return nil, cerrors.Internal("failed to remove message file", err)
		}
	} else {
		if err := s.messages.UpdateFileIDs(ctx, msg.ID, remaining); err != nil {
			slog.ErrorContext(ctx, "failed to remove message file", "message_id", messageID, "file_id", fileID, "error", err)
			return nil, cerrors.Internal("failed to remove message file", err)
		}
		if err := s.hydrateMessageFilesForSend(ctx, msg); err != nil {
			return nil, err
		}
		s.publishEvent(ctx, event.TypeMessageUpdated, workspaceID, msg.ChannelID, userID, event.NewMessagePayload(msg, ch))
		s.publishToWorkspace(ctx, event.TypeMessageUpdated, workspaceID, msg.ChannelID, userID, event.NewMessagePayload(msg, ch))
	}

	slog.InfoContext(ctx, "message file removed", "message_id", messageID, "file_id", fileID, "user_id", userID)
	return msg, nil
}

type movedMessageDeleteRef struct {
	ID        uuid.UUID `json:"id"`
	ChannelID uuid.UUID `json:"channel_id"`
	DeletedAt time.Time `json:"deleted_at"`
}

func (s *Service) MoveMessage(
	ctx context.Context,
	sourceChannelID, messageID, userID, targetChannelID uuid.UUID,
	parentID *uuid.UUID,
) (*entity.Message, error) {
	msg, sourceCh, err := s.requireOwnMessageAccess(ctx, messageID, userID, accesspolicy.CapabilityParticipate)
	if err != nil {
		return nil, err
	}
	if msg.ChannelID != sourceChannelID {
		return nil, cerrors.NotFound("message not found")
	}
	if parentID != nil && *parentID == messageID {
		return nil, cerrors.InvalidInput("message cannot be moved under itself")
	}

	targetDecision, err := s.authorizeChannel(ctx, targetChannelID, userID, accesspolicy.CapabilityParticipate)
	if err != nil {
		return nil, err
	}
	targetCh := targetDecision.Channel
	if targetCh.Archived {
		return nil, cerrors.Forbidden("cannot move messages to an archived channel")
	}
	sourceWorkspaceID, err := requestWorkspaceIDForMessage(ctx, sourceCh)
	if err != nil {
		return nil, err
	}
	targetWorkspaceID, err := requestWorkspaceIDForMessage(ctx, targetCh)
	if err != nil {
		return nil, err
	}
	if sourceWorkspaceID != targetWorkspaceID {
		return nil, cerrors.InvalidInput("message cannot be moved across workspaces")
	}

	if parentID != nil {
		parent, err := s.messages.GetByID(ctx, *parentID)
		if err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
				return nil, cerrors.NotFound("parent message not found")
			}
			return nil, cerrors.Internal("failed to get parent message", err)
		}
		if parent.DeletedAt != nil {
			return nil, cerrors.NotFound("parent message has been deleted")
		}
		if parent.ChannelID != targetChannelID {
			return nil, cerrors.InvalidInput("parent message does not belong to target channel")
		}
	}

	originalChannelID := msg.ChannelID
	now := time.Now().UTC()
	msg.ChannelID = targetChannelID
	msg.ParentID = parentID
	msg.Pinned = false
	msg.PinnedBy = nil
	msg.PinnedAt = nil
	msg.UpdatedAt = now
	if err := s.hydrateMessageMentions(ctx, msg, targetCh); err != nil {
		return nil, err
	}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			mover, ok := scope.Messages().(messageMoveRepository)
			if !ok {
				return cerrors.Unavailable("message move repository is not configured")
			}
			if err := mover.Move(ctx, msg); err != nil {
				return err
			}
			if sourceWorkspaceID != uuid.Nil {
				if err := s.enqueueMessageDeleteSearchTx(ctx, scope, sourceWorkspaceID, msg.ID); err != nil {
					return err
				}
			}
			if targetWorkspaceID != uuid.Nil {
				if err := s.enqueueMessageSearchTx(ctx, scope, targetWorkspaceID, targetChannelID, msg); err != nil {
					return err
				}
			}
			return s.enqueueMoveEventsTx(ctx, scope, originalChannelID, sourceWorkspaceID, targetWorkspaceID, msg, targetCh, userID, now)
		}); err != nil {
			slog.ErrorContext(ctx, "failed to move message transaction", "message_id", messageID, "error", err)
			return nil, cerrors.Internal("failed to move message", err)
		}
	} else {
		mover, ok := s.messages.(messageMoveRepository)
		if !ok {
			return nil, cerrors.Unavailable("message move repository is not configured")
		}
		if err := mover.Move(ctx, msg); err != nil {
			slog.ErrorContext(ctx, "failed to move message", "message_id", messageID, "error", err)
			return nil, cerrors.Internal("failed to move message", err)
		}
		s.enqueueSearch(ctx, "move message delete old index", func() error {
			if sourceWorkspaceID == uuid.Nil {
				return nil
			}
			return s.search.DeleteMessage(ctx, sourceWorkspaceID, msg.ID)
		})
		s.enqueueSearch(ctx, "move message index target", func() error {
			if targetWorkspaceID == uuid.Nil {
				return nil
			}
			return s.search.IndexMessage(ctx, targetWorkspaceID, targetChannelID, msg.ID, msg.Content, msg.CreatedAt)
		})
		s.publishMoveEvents(ctx, originalChannelID, sourceWorkspaceID, targetWorkspaceID, msg, targetCh, userID, now)
	}

	slog.InfoContext(ctx, "message moved", "message_id", messageID, "source_channel_id", sourceChannelID, "target_channel_id", targetChannelID, "user_id", userID)
	return msg, nil
}

// DeleteMessage soft-deletes a message after verifying ownership.
func (s *Service) DeleteMessage(ctx context.Context, messageID, userID uuid.UUID) error {
	msg, ch, err := s.requireOwnMessageAccess(ctx, messageID, userID, accesspolicy.CapabilityParticipate)
	if err != nil {
		return err
	}

	workspaceID := channelWorkspaceIDOrNil(ch)
	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			if scope.Messages() == nil {
				return cerrors.Unavailable("message transaction scope is not configured")
			}
			affectedQuoteRows, err := scope.Messages().SoftDeleteWithCascade(ctx, messageID)
			if err != nil {
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
			if err := s.enqueueEventTx(ctx, scope, event.TypeMessageDeleted, fmt.Sprintf("aloqa.chat.%s", msg.ChannelID), workspaceID, msg.ChannelID, userID, event.NewMessagePayload(msg, ch)); err != nil {
				return err
			}
			// Also publish the delete to the workspace subject so clients in
			// other channels drop it too (active-channel room only otherwise).
			// Mirrors MessageCreated; FE apply handlers are idempotent (ALK-654).
			if err := s.enqueueEventTx(ctx, scope, event.TypeMessageDeleted, fmt.Sprintf("aloqa.ws.%s", workspaceID), workspaceID, msg.ChannelID, userID, event.NewMessagePayload(msg, ch)); err != nil {
				return err
			}
			return s.enqueueCascadeQuoteUpdateEventsTx(ctx, scope, affectedQuoteRows, userID)
		}); err != nil {
			slog.ErrorContext(ctx, "failed to delete message transaction", "message_id", messageID, "error", err)
			return cerrors.Internal("failed to delete message", err)
		}
	} else {
		affectedQuoteRows, err := s.messages.SoftDeleteWithCascade(ctx, messageID)
		if err != nil {
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
		s.publishEvent(ctx, event.TypeMessageDeleted, workspaceID, msg.ChannelID, userID, event.NewMessagePayload(msg, ch))
		s.publishToWorkspace(ctx, event.TypeMessageDeleted, workspaceID, msg.ChannelID, userID, event.NewMessagePayload(msg, ch))
		s.publishCascadeQuoteUpdateEvents(ctx, affectedQuoteRows, userID)
	}

	slog.InfoContext(ctx, "message deleted", "message_id", messageID, "user_id", userID)
	return nil
}

func (s *Service) enqueueCascadeQuoteUpdateEventsTx(ctx context.Context, scope txscope.Scope, affectedQuoteRows []uuid.UUID, userID uuid.UUID) error {
	if scope.Messages() == nil {
		return cerrors.Unavailable("message transaction scope is not configured")
	}
	if scope.Channels() == nil {
		return cerrors.Unavailable("channel transaction scope is not configured")
	}
	for _, rowID := range affectedQuoteRows {
		updated, err := scope.Messages().GetByID(ctx, rowID)
		if err != nil {
			return err
		}
		if updated == nil {
			continue
		}
		ch, err := scope.Channels().GetByID(ctx, updated.ChannelID)
		if err != nil {
			return err
		}
		if ch == nil {
			continue
		}
		workspaceID := channelWorkspaceIDOrNil(ch)
		if err := s.enqueueEventTx(ctx, scope, event.TypeMessageUpdated, fmt.Sprintf("aloqa.chat.%s", updated.ChannelID), workspaceID, updated.ChannelID, userID, event.NewMessagePayload(updated, ch)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) publishCascadeQuoteUpdateEvents(ctx context.Context, affectedQuoteRows []uuid.UUID, userID uuid.UUID) {
	for _, rowID := range affectedQuoteRows {
		updated, err := s.messages.GetByID(ctx, rowID)
		if err != nil || updated == nil {
			if err != nil {
				slog.ErrorContext(ctx, "failed to load cascade quote update", "message_id", rowID, "error", err)
			}
			continue
		}
		ch, err := s.channels.GetByID(ctx, updated.ChannelID)
		if err != nil || ch == nil {
			if err != nil {
				slog.ErrorContext(ctx, "failed to load cascade quote channel", "channel_id", updated.ChannelID, "error", err)
			}
			continue
		}
		s.publishEvent(ctx, event.TypeMessageUpdated, channelWorkspaceIDOrNil(ch), updated.ChannelID, userID, event.NewMessagePayload(updated, ch))
	}
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
func (s *Service) AddReaction(ctx context.Context, messageID, userID uuid.UUID, emoji string) (*entity.Reaction, error) {
	if err := validateEmoji(emoji); err != nil {
		return nil, err
	}

	msg, ch, err := s.requireMessageAccessWithCapability(ctx, messageID, userID, accesspolicy.CapabilityParticipate)
	if err != nil {
		return nil, err
	}

	reaction := &entity.Reaction{
		ID:        id.New(),
		MessageID: messageID,
		UserID:    userID,
		Emoji:     emoji,
		CreatedAt: time.Now(),
	}

	if err := s.messages.AddReaction(ctx, reaction); err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok {
			if appErr.Code == cerrors.CodeAlreadyExists {
				existing, getErr := s.messages.GetReactionByMessageUserEmoji(ctx, messageID, userID, emoji)
				if getErr != nil {
					if getAppErr, ok := cerrors.AsAppError(getErr); ok {
						return nil, getAppErr
					}
					slog.ErrorContext(ctx, "failed to get existing reaction", "message_id", messageID, "emoji", emoji, "error", getErr)
					return nil, cerrors.Internal("failed to get existing reaction", getErr)
				}
				return existing, nil
			}
			return nil, appErr
		}
		slog.ErrorContext(ctx, "failed to add reaction", "message_id", messageID, "emoji", emoji, "error", err)
		return nil, cerrors.Internal("failed to add reaction", err)
	}

	s.publishEvent(ctx, event.TypeReactionAdded, channelWorkspaceIDOrNil(ch), msg.ChannelID, userID, reaction)
	// Fan out to the workspace subject too so background channels update (ALK-654).
	s.publishToWorkspace(ctx, event.TypeReactionAdded, channelWorkspaceIDOrNil(ch), msg.ChannelID, userID, reaction)

	return reaction, nil
}

// RemoveReaction removes an emoji reaction from a message.
func (s *Service) RemoveReaction(ctx context.Context, messageID, userID uuid.UUID, emoji string) error {
	if err := validateEmoji(emoji); err != nil {
		return err
	}

	msg, ch, err := s.requireMessageAccessWithCapability(ctx, messageID, userID, accesspolicy.CapabilityParticipate)
	if err != nil {
		return err
	}

	reaction, err := s.messages.GetReactionByMessageUserEmoji(ctx, messageID, userID, emoji)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok {
			return appErr
		}
		slog.ErrorContext(ctx, "failed to get reaction", "message_id", messageID, "emoji", emoji, "error", err)
		return cerrors.Internal("failed to get reaction", err)
	}

	if err := s.messages.RemoveReaction(ctx, messageID, userID, emoji); err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok {
			return appErr
		}
		slog.ErrorContext(ctx, "failed to remove reaction", "message_id", messageID, "emoji", emoji, "error", err)
		return cerrors.Internal("failed to remove reaction", err)
	}

	s.publishEvent(ctx, event.TypeReactionRemoved, channelWorkspaceIDOrNil(ch), msg.ChannelID, userID, reaction)
	s.publishToWorkspace(ctx, event.TypeReactionRemoved, channelWorkspaceIDOrNil(ch), msg.ChannelID, userID, reaction)

	return nil
}

// RemoveReactionByID removes the caller's reaction by its stable reaction ID.
func (s *Service) RemoveReactionByID(ctx context.Context, reactionID, userID uuid.UUID) error {
	reaction, err := s.messages.GetReactionByID(ctx, reactionID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok {
			return appErr
		}
		slog.ErrorContext(ctx, "failed to get reaction", "reaction_id", reactionID, "error", err)
		return cerrors.Internal("failed to get reaction", err)
	}

	msg, ch, err := s.requireMessageAccessWithCapability(ctx, reaction.MessageID, userID, accesspolicy.CapabilityParticipate)
	if err != nil {
		return err
	}
	if reaction.UserID != userID {
		return cerrors.Forbidden("cannot remove another user's reaction")
	}

	if err := s.messages.RemoveReactionByID(ctx, reactionID); err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok {
			return appErr
		}
		slog.ErrorContext(ctx, "failed to remove reaction", "reaction_id", reactionID, "error", err)
		return cerrors.Internal("failed to remove reaction", err)
	}

	s.publishEvent(ctx, event.TypeReactionRemoved, channelWorkspaceIDOrNil(ch), msg.ChannelID, userID, reaction)
	s.publishToWorkspace(ctx, event.TypeReactionRemoved, channelWorkspaceIDOrNil(ch), msg.ChannelID, userID, reaction)

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
	workspaceID := channelWorkspaceIDOrNil(ch)
	s.publishEvent(ctx, event.TypeMessagePinned, workspaceID, msg.ChannelID, userID, event.PinPayload{
		MessageID: messageID,
		ChannelID: msg.ChannelID,
		UserID:    userID,
	})
	s.publishToWorkspace(ctx, event.TypeMessagePinned, workspaceID, msg.ChannelID, userID, event.PinPayload{
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
	workspaceID := channelWorkspaceIDOrNil(ch)
	s.publishEvent(ctx, event.TypeMessageUnpinned, workspaceID, msg.ChannelID, userID, event.PinPayload{
		MessageID: messageID,
		ChannelID: msg.ChannelID,
		UserID:    userID,
	})
	s.publishToWorkspace(ctx, event.TypeMessageUnpinned, workspaceID, msg.ChannelID, userID, event.PinPayload{
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
		if err := s.stampDMRequestStatus(ctx, ch, userA); err != nil {
			return nil, err
		}
		if err := s.hydrateDMMembers(ctx, ch); err != nil {
			return nil, err
		}
		return ch, nil
	}

	now := time.Now()
	ch = &entity.Channel{
		ID:          id.New(),
		WorkspaceID: &workspaceID,
		Name:        "",
		Type:        entity.ChannelTypeDM,
		CreatedBy:   userA,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	members := []*entity.ChannelMember{
		{
			ID:              id.New(),
			ChannelID:       ch.ID,
			UserID:          userA,
			Role:            entity.ChannelRoleMember,
			LastReadAt:      now,
			JoinedAt:        now,
			DMRequestStatus: entity.DMRequestStatusAccepted,
		},
		{
			ID:              id.New(),
			ChannelID:       ch.ID,
			UserID:          userB,
			Role:            entity.ChannelRoleMember,
			LastReadAt:      now,
			JoinedAt:        now,
			DMRequestStatus: entity.DMRequestStatusPending,
		},
	}

	ch.Members = []uuid.UUID{userA, userB}
	status := entity.DMRequestStatusAccepted
	ch.DMRequestStatus = &status
	recipientChannel := *ch
	recipientChannel.DMRequestStatus = dmRequestStatusPtr(entity.DMRequestStatusPending)
	recipientPayload := channelPayloadWithMembers(&recipientChannel, ch.Members)

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
			if err := s.enqueueChannelSearchTx(ctx, scope, ch); err != nil {
				return err
			}
			return s.enqueueEventTx(ctx, scope, event.TypeChannelCreated, workspaceUserEventsSubject(workspaceID, userB), workspaceID, ch.ID, userA, recipientPayload)
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
			return s.search.IndexChannel(ctx, workspaceID, ch.ID, ch.Name, derefTopicOrEmpty(ch.Topic), ch.CreatedAt, ch.UpdatedAt)
		})
		s.doPublish(ctx, event.TypeChannelCreated, workspaceUserEventsSubject(workspaceID, userB), workspaceID, ch.ID, userA, recipientPayload)
	}
	slog.InfoContext(ctx, "DM channel created", "channel_id", ch.ID, "user_a", userA, "user_b", userB)
	return ch, nil
}

func (s *Service) AcceptDMRequest(ctx context.Context, workspaceID, channelID, userID uuid.UUID) (*entity.Channel, error) {
	return s.updateDMRequestStatus(ctx, workspaceID, channelID, userID, entity.DMRequestStatusAccepted)
}

func (s *Service) BlockDMRequest(ctx context.Context, workspaceID, channelID, userID uuid.UUID) (*entity.Channel, error) {
	return s.updateDMRequestStatus(ctx, workspaceID, channelID, userID, entity.DMRequestStatusBlocked)
}

func (s *Service) updateDMRequestStatus(
	ctx context.Context,
	workspaceID, channelID, userID uuid.UUID,
	status entity.DMRequestStatus,
) (*entity.Channel, error) {
	decision, err := s.authorizeChannel(ctx, channelID, userID, accesspolicy.CapabilityView)
	if err != nil {
		return nil, err
	}
	if decision == nil || decision.Channel == nil || decision.ChannelMember == nil {
		return nil, cerrors.NotFound("dm request not found")
	}
	if decision.Channel.Type != entity.ChannelTypeDM {
		return nil, cerrors.Forbidden("only DM requests can be managed")
	}
	channelWorkspaceID, err := requireChannelWorkspaceID(decision.Channel)
	if err != nil {
		return nil, err
	}
	if channelWorkspaceID != workspaceID {
		return nil, cerrors.NotFound("channel not found")
	}

	updater, ok := s.channels.(dmRequestStatusRepository)
	if !ok {
		return nil, cerrors.Unavailable("DM request status persistence is unavailable")
	}

	current := normalizeDMRequestStatus(decision.ChannelMember.DMRequestStatus)
	if current != status {
		if err := updater.UpdateDMRequestStatus(ctx, channelID, userID, status); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return nil, appErr
			}
			slog.ErrorContext(ctx, "failed to update DM request status", "channel_id", channelID, "user_id", userID, "status", status, "error", err)
			return nil, cerrors.Internal("failed to update DM request status", err)
		}
	}

	channel := *decision.Channel
	channel.DMRequestStatus = dmRequestStatusPtr(status)
	if err := s.hydrateDMMembers(ctx, &channel); err != nil {
		return nil, err
	}
	payload := event.ChannelPayload{Channel: &channel}
	s.publishEvent(ctx, event.TypeChannelUpdated, workspaceID, channelID, userID, payload)
	s.publishToUserEvents(ctx, event.TypeChannelUpdated, workspaceID, channelID, userID, payload)
	return &channel, nil
}

func (s *Service) stampDMRequestStatus(ctx context.Context, ch *entity.Channel, userID uuid.UUID) error {
	if ch == nil || ch.Type != entity.ChannelTypeDM {
		return nil
	}
	member, err := s.channels.GetMember(ctx, ch.ID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil
		}
		slog.ErrorContext(ctx, "failed to load DM request status", "channel_id", ch.ID, "user_id", userID, "error", err)
		return cerrors.Internal("failed to load DM request status", err)
	}
	ch.DMRequestStatus = dmRequestStatusPtr(normalizeDMRequestStatus(member.DMRequestStatus))
	return nil
}

func normalizeDMRequestStatus(status entity.DMRequestStatus) entity.DMRequestStatus {
	switch status {
	case entity.DMRequestStatusPending, entity.DMRequestStatusBlocked:
		return status
	default:
		return entity.DMRequestStatusAccepted
	}
}

func dmRequestStatusPtr(status entity.DMRequestStatus) *entity.DMRequestStatus {
	normalized := normalizeDMRequestStatus(status)
	return &normalized
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

func workspaceUserEventsSubject(workspaceID, userID uuid.UUID) string {
	return fmt.Sprintf("aloqa.ws.%s.user.%s.events", workspaceID, userID)
}

func (s *Service) publishToUserEvents(ctx context.Context, evtType event.Type, workspaceID, channelID, userID uuid.UUID, payload any) {
	s.doPublish(ctx, evtType, workspaceUserEventsSubject(workspaceID, userID), workspaceID, channelID, userID, payload)
}

func uniqueUserIDs(userIDs []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(userIDs))
	unique := make([]uuid.UUID, 0, len(userIDs))
	for _, userID := range userIDs {
		if userID == uuid.Nil {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		unique = append(unique, userID)
	}
	return unique
}

func currentChannelMemberIDs(ctx context.Context, channels repository.ChannelRepository, channelID uuid.UUID) ([]uuid.UUID, error) {
	members, err := channels.ListMembers(ctx, channelID)
	if err != nil {
		return nil, err
	}
	memberIDs := make([]uuid.UUID, 0, len(members))
	for _, member := range members {
		memberIDs = append(memberIDs, member.UserID)
	}
	return memberIDs, nil
}

func (s *Service) enqueueChannelCreatedVisibilityEventsTx(
	ctx context.Context,
	scope txscope.Scope,
	workspaceID, actorID uuid.UUID,
	ch *entity.Channel,
	targetUserIDs []uuid.UUID,
	privatePayload event.ChannelPayload,
) error {
	if ch == nil {
		return nil
	}
	switch ch.Type {
	case entity.ChannelTypePublic:
		return s.enqueueEventTx(ctx, scope, event.TypeChannelCreated, fmt.Sprintf("aloqa.ws.%s", workspaceID), workspaceID, ch.ID, actorID, channelPayloadWithoutMembers(ch))
	case entity.ChannelTypePrivate:
		for _, targetUserID := range uniqueUserIDs(targetUserIDs) {
			if err := s.enqueueEventTx(ctx, scope, event.TypeChannelCreated, workspaceUserEventsSubject(workspaceID, targetUserID), workspaceID, ch.ID, actorID, privatePayload); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) publishChannelCreatedVisibilityEvents(
	ctx context.Context,
	workspaceID, actorID uuid.UUID,
	ch *entity.Channel,
	targetUserIDs []uuid.UUID,
	privatePayload event.ChannelPayload,
) {
	if ch == nil {
		return
	}
	switch ch.Type {
	case entity.ChannelTypePublic:
		s.publishToWorkspace(ctx, event.TypeChannelCreated, workspaceID, ch.ID, actorID, channelPayloadWithoutMembers(ch))
	case entity.ChannelTypePrivate:
		for _, targetUserID := range uniqueUserIDs(targetUserIDs) {
			s.doPublish(ctx, event.TypeChannelCreated, workspaceUserEventsSubject(workspaceID, targetUserID), workspaceID, ch.ID, actorID, privatePayload)
		}
	}
}

func (s *Service) publishMoveEvents(
	ctx context.Context,
	originalChannelID, sourceWorkspaceID, targetWorkspaceID uuid.UUID,
	msg *entity.Message,
	targetCh *entity.Channel,
	userID uuid.UUID,
	movedAt time.Time,
) {
	if originalChannelID == msg.ChannelID {
		s.publishEvent(ctx, event.TypeMessageUpdated, targetWorkspaceID, msg.ChannelID, userID, event.NewMessagePayload(msg, targetCh))
		s.publishToWorkspace(ctx, event.TypeMessageUpdated, targetWorkspaceID, msg.ChannelID, userID, event.NewMessagePayload(msg, targetCh))
		return
	}
	deleteRef := movedMessageDeleteRef{ID: msg.ID, ChannelID: originalChannelID, DeletedAt: movedAt}
	s.publishEvent(ctx, event.TypeMessageDeleted, sourceWorkspaceID, originalChannelID, userID, deleteRef)
	s.publishToWorkspace(ctx, event.TypeMessageDeleted, sourceWorkspaceID, originalChannelID, userID, deleteRef)
	s.publishEvent(ctx, event.TypeMessageCreated, targetWorkspaceID, msg.ChannelID, userID, event.NewMessagePayload(msg, targetCh))
	s.publishToWorkspace(ctx, event.TypeMessageCreated, targetWorkspaceID, msg.ChannelID, userID, event.NewMessagePayload(msg, targetCh))
}

func (s *Service) enqueueSearch(ctx context.Context, action string, fn func() error) {
	if s.search == nil || fn == nil {
		return
	}
	if err := fn(); err != nil {
		slog.ErrorContext(ctx, "search enqueue failed", "action", action, "error", err)
	}
}

func (s *Service) enqueueChannelSearchSync(ctx context.Context, workspaceID uuid.UUID, ch *entity.Channel) {
	if ch == nil {
		return
	}
	if isSearchableChannel(ch) {
		s.enqueueSearch(ctx, "index channel", func() error {
			return s.search.IndexChannel(ctx, workspaceID, ch.ID, ch.Name, derefTopicOrEmpty(ch.Topic), ch.CreatedAt, ch.UpdatedAt)
		})
		return
	}
	s.enqueueSearch(ctx, "delete channel from search", func() error {
		return s.search.DeleteChannel(ctx, workspaceID, ch.ID)
	})
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
	if !isSearchableChannel(ch) {
		return nil
	}
	return scope.SearchIndexer().EnqueueUpsert(ctx, searchsvc.Document{
		WorkspaceID: channelWorkspaceIDOrNil(ch),
		ResourceID:  ch.ID,
		ChannelID:   &ch.ID,
		Type:        searchsvc.ResourceTypeChannel,
		Title:       ch.Name,
		Content:     derefTopicOrEmpty(ch.Topic),
		CreatedAt:   ch.CreatedAt,
		UpdatedAt:   ch.UpdatedAt,
		Metadata: map[string]any{
			"type":     string(ch.Type),
			"archived": ch.Archived,
		},
	})
}

func (s *Service) enqueueChannelSearchSyncTx(ctx context.Context, scope txscope.Scope, ch *entity.Channel) error {
	if scope == nil || scope.SearchIndexer() == nil || ch == nil {
		return nil
	}
	workspaceID := channelWorkspaceIDOrNil(ch)
	if isSearchableChannel(ch) {
		return s.enqueueChannelSearchTx(ctx, scope, ch)
	}
	if workspaceID == uuid.Nil {
		return nil
	}
	return scope.SearchIndexer().EnqueueDelete(ctx, workspaceID, searchsvc.ResourceTypeChannel, ch.ID)
}

func (s *Service) enqueueMessageDeleteSearchTx(ctx context.Context, scope txscope.Scope, workspaceID, messageID uuid.UUID) error {
	if scope == nil || scope.SearchIndexer() == nil {
		return nil
	}
	return scope.SearchIndexer().EnqueueDelete(ctx, workspaceID, searchsvc.ResourceTypeMessage, messageID)
}

func (s *Service) enqueueMoveEventsTx(
	ctx context.Context,
	scope txscope.Scope,
	originalChannelID, sourceWorkspaceID, targetWorkspaceID uuid.UUID,
	msg *entity.Message,
	targetCh *entity.Channel,
	userID uuid.UUID,
	movedAt time.Time,
) error {
	if originalChannelID == msg.ChannelID {
		payload := event.NewMessagePayload(msg, targetCh)
		if err := s.enqueueEventTx(ctx, scope, event.TypeMessageUpdated, fmt.Sprintf("aloqa.chat.%s", msg.ChannelID), targetWorkspaceID, msg.ChannelID, userID, payload); err != nil {
			return err
		}
		return s.enqueueEventTx(ctx, scope, event.TypeMessageUpdated, fmt.Sprintf("aloqa.ws.%s", targetWorkspaceID), targetWorkspaceID, msg.ChannelID, userID, payload)
	}
	deleteRef := movedMessageDeleteRef{ID: msg.ID, ChannelID: originalChannelID, DeletedAt: movedAt}
	if err := s.enqueueEventTx(ctx, scope, event.TypeMessageDeleted, fmt.Sprintf("aloqa.chat.%s", originalChannelID), sourceWorkspaceID, originalChannelID, userID, deleteRef); err != nil {
		return err
	}
	if err := s.enqueueEventTx(ctx, scope, event.TypeMessageDeleted, fmt.Sprintf("aloqa.ws.%s", sourceWorkspaceID), sourceWorkspaceID, originalChannelID, userID, deleteRef); err != nil {
		return err
	}
	payload := event.NewMessagePayload(msg, targetCh)
	if err := s.enqueueEventTx(ctx, scope, event.TypeMessageCreated, fmt.Sprintf("aloqa.chat.%s", msg.ChannelID), targetWorkspaceID, msg.ChannelID, userID, payload); err != nil {
		return err
	}
	return s.enqueueEventTx(ctx, scope, event.TypeMessageCreated, fmt.Sprintf("aloqa.ws.%s", targetWorkspaceID), targetWorkspaceID, msg.ChannelID, userID, payload)
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

	// Broadcast the advanced read watermark so other clients viewing the
	// channel can update seen indicators in realtime (ALK-111). Best-effort:
	// publishEvent swallows transport errors, so a failure never fails the read.
	if decision.Channel != nil && decision.Channel.WorkspaceID != nil {
		s.publishEvent(ctx, event.TypeChannelRead, *decision.Channel.WorkspaceID, channelID, userID, event.ChannelReadPayload{
			ChannelID:  channelID,
			UserID:     userID,
			LastReadAt: time.Now().UTC(),
		})
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

func (s *Service) hydrateMessageReactions(ctx context.Context, items []entity.Message) error {
	messageIDs := make([]uuid.UUID, 0, len(items))
	for i := range items {
		if items[i].DeletedAt != nil {
			items[i].Reactions = nil
			continue
		}

		messageIDs = append(messageIDs, items[i].ID)
	}
	if len(messageIDs) == 0 {
		return nil
	}

	reactionsByMessageID, err := s.messages.ListReactionsByMessageIDs(ctx, messageIDs)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list message reactions", "message_count", len(messageIDs), "error", err)
		return cerrors.Internal("failed to list message reactions", err)
	}

	for i := range items {
		if items[i].DeletedAt != nil {
			continue
		}

		items[i].Reactions = reactionsByMessageID[items[i].ID]
	}
	return nil
}

func (s *Service) hydrateMessageAttachments(ctx context.Context, items []entity.Message) error {
	messageIDs := make([]uuid.UUID, 0, len(items))
	for i := range items {
		if items[i].DeletedAt != nil {
			items[i].Attachments = nil
			continue
		}

		messageIDs = append(messageIDs, items[i].ID)
	}
	if len(messageIDs) == 0 {
		return nil
	}

	attachmentsByMessageID, err := s.messages.ListAttachmentsByMessageIDs(ctx, messageIDs)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list message attachments", "message_count", len(messageIDs), "error", err)
		return cerrors.Internal("failed to list message attachments", err)
	}

	for i := range items {
		if items[i].DeletedAt != nil {
			continue
		}

		items[i].Attachments = attachmentsByMessageID[items[i].ID]
	}
	return nil
}

func (s *Service) shareMessageFilesTx(ctx context.Context, scope txscope.Scope, fileIDs []uuid.UUID, workspaceID, channelID, userID uuid.UUID) error {
	if len(fileIDs) == 0 {
		return nil
	}
	if scope.Files() == nil {
		return cerrors.Unavailable("file transaction scope is not configured")
	}
	for _, fileID := range fileIDs {
		if fileID == uuid.Nil {
			return cerrors.InvalidInput("file_ids must contain valid ids")
		}
		if err := scope.Files().ShareFile(ctx, fileID, repository.FileShareOptions{
			TargetType:  entity.FileShareTargetChannel,
			TargetID:    channelID,
			WorkspaceID: workspaceID,
			ActorID:     userID,
			OwnerOnly:   false,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) shareMessageFiles(ctx context.Context, fileIDs []uuid.UUID, workspaceID, channelID, userID uuid.UUID) error {
	if len(fileIDs) == 0 {
		return nil
	}
	if s.files == nil {
		return cerrors.Unavailable("file library is not configured")
	}
	for _, fileID := range fileIDs {
		if fileID == uuid.Nil {
			return cerrors.InvalidInput("file_ids must contain valid ids")
		}
		if err := s.files.ShareFile(ctx, fileID, repository.FileShareOptions{
			TargetType:  entity.FileShareTargetChannel,
			TargetID:    channelID,
			WorkspaceID: workspaceID,
			ActorID:     userID,
			OwnerOnly:   false,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) revokeMessageFileSharesBestEffort(ctx context.Context, fileIDs []uuid.UUID, workspaceID, channelID, userID uuid.UUID) {
	if len(fileIDs) == 0 || s.files == nil {
		return
	}
	for _, fileID := range fileIDs {
		if err := s.files.RevokeFileShare(ctx, fileID, repository.FileShareOptions{
			TargetType:  entity.FileShareTargetChannel,
			TargetID:    channelID,
			WorkspaceID: workspaceID,
			ActorID:     userID,
			OwnerOnly:   false,
		}); err != nil {
			slog.WarnContext(ctx, "failed to revoke file share after message create failure", "file_id", fileID, "channel_id", channelID, "error", err)
		}
	}
}

func (s *Service) hydrateMessageFilesTx(ctx context.Context, scope txscope.Scope, msg *entity.Message) error {
	if msg == nil || len(msg.FileIDs) == 0 {
		return nil
	}
	if scope.Files() == nil {
		return cerrors.Unavailable("file transaction scope is not configured")
	}
	files, err := scope.Files().ResolveMessageFiles(ctx, msg.FileIDs)
	if err != nil {
		return cerrors.Internal("failed to resolve message files", err)
	}
	msg.Files = files
	return nil
}

func (s *Service) hydrateMessageFilesForSend(ctx context.Context, msg *entity.Message) error {
	if msg == nil || len(msg.FileIDs) == 0 {
		return nil
	}
	if s.files == nil {
		return cerrors.Unavailable("file library is not configured")
	}
	files, err := s.files.ResolveMessageFiles(ctx, msg.FileIDs)
	if err != nil {
		return cerrors.Internal("failed to resolve message files", err)
	}
	msg.Files = files
	return nil
}

func (s *Service) hydrateMessageFiles(ctx context.Context, items []entity.Message) error {
	if s.files == nil {
		return nil
	}
	for i := range items {
		if items[i].DeletedAt != nil || len(items[i].FileIDs) == 0 {
			items[i].Files = nil
			continue
		}
		files, err := s.files.ResolveMessageFiles(ctx, items[i].FileIDs)
		if err != nil {
			return cerrors.Internal("failed to resolve message files", err)
		}
		items[i].Files = files
	}
	return nil
}

func (s *Service) hydrateMessageMentionsForRead(ctx context.Context, items []entity.Message) error {
	resolver, ok := s.messages.(messageMentionBatchResolver)
	if !ok {
		return nil
	}

	messageIDs := make([]uuid.UUID, 0, len(items))
	seen := make(map[uuid.UUID]struct{}, len(items))
	for i := range items {
		if items[i].DeletedAt != nil {
			items[i].Mentions = nil
			continue
		}
		items[i].Mentions = []uuid.UUID{}
		if !strings.Contains(items[i].Content, "@") {
			continue
		}
		if _, ok := seen[items[i].ID]; ok {
			continue
		}
		seen[items[i].ID] = struct{}{}
		messageIDs = append(messageIDs, items[i].ID)
	}
	if len(messageIDs) == 0 {
		return nil
	}

	mentionsByMessageID, err := resolver.ResolveMentionsByMessageIDs(ctx, messageIDs)
	if err != nil {
		slog.ErrorContext(ctx, "failed to resolve message mentions", "message_count", len(messageIDs), "error", err)
		return cerrors.Internal("failed to resolve message mentions", err)
	}

	for i := range items {
		if items[i].DeletedAt != nil {
			continue
		}
		items[i].Mentions = mentionsByMessageID[items[i].ID]
	}
	return nil
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
	msg.ForwardedFrom = nil
	msg.QuotedMessageID = nil
	msg.QuotedSnapshot = nil
	msg.ProfileShare = nil
	msg.Reactions = nil
	msg.Mentions = nil
	msg.Attachments = nil
	msg.FileIDs = nil
	msg.Files = nil
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
