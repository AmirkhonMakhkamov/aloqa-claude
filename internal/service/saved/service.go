package saved

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
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/repository/postgres"
	"aloqa/internal/security/accesspolicy"
)

type EventPublisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

type Service struct {
	users    repository.UserRepository
	channels repository.ChannelRepository
	messages repository.MessageRepository
	saved    *postgres.SavedRepo
	access   *accesspolicy.Checker
	pubsub   EventPublisher
}

func NewService(
	users repository.UserRepository,
	channels repository.ChannelRepository,
	messages repository.MessageRepository,
	savedRepo *postgres.SavedRepo,
	access *accesspolicy.Checker,
	pubsub EventPublisher,
) *Service {
	return &Service{users: users, channels: channels, messages: messages, saved: savedRepo, access: access, pubsub: pubsub}
}

type SaveResult struct {
	SavedMsgID uuid.UUID `json:"saved_msg_id"`
	ChannelID  uuid.UUID `json:"channel_id"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Service) ResolveWorkspaceChannel(ctx context.Context, userID, workspaceID uuid.UUID) (*entity.Channel, error) {
	return s.saved.ResolveChannel(ctx, userID, &workspaceID, entity.ChannelTypeSaved)
}

func (s *Service) ResolveGlobalChannel(ctx context.Context, userID uuid.UUID) (*entity.Channel, error) {
	return s.saved.ResolveChannel(ctx, userID, nil, entity.ChannelTypeSavedGlobal)
}

func (s *Service) SaveMessage(ctx context.Context, userID, messageID, workspaceID uuid.UUID) (*SaveResult, error) {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	src, err := s.messages.GetByID(ctx, messageID)
	if err != nil {
		return nil, err
	}
	sourceChannel, err := s.channels.GetByID(ctx, src.ChannelID)
	if err != nil {
		return nil, err
	}
	if sourceChannel.WorkspaceID == nil || *sourceChannel.WorkspaceID != workspaceID {
		return nil, cerrors.NotFound("message not found")
	}
	if src.DeletedAt != nil {
		return nil, cerrors.Unprocessable("cannot_save_deleted_message")
	}
	if s.access != nil {
		if _, err := s.access.Channel(ctx, sourceChannel.ID, userID, accesspolicy.CapabilityView); err != nil {
			return nil, err
		}
	}

	mode := user.SavedMessagesMode
	if mode == "" {
		mode = entity.SavedMessagesModePerWorkspace
	}
	selfChannelType := entity.ChannelTypeSaved
	selfWorkspaceID := &workspaceID
	if mode == entity.SavedMessagesModeGlobal {
		selfChannelType = entity.ChannelTypeSavedGlobal
		selfWorkspaceID = nil
	}
	selfChannel, err := s.saved.ResolveChannel(ctx, userID, selfWorkspaceID, selfChannelType)
	if err != nil {
		return nil, err
	}
	existing, err := s.saved.FindSavedCopy(ctx, selfChannel.ID, messageID)
	if err == nil {
		return &SaveResult{SavedMsgID: existing.ID, ChannelID: existing.ChannelID, CreatedAt: existing.CreatedAt}, nil
	}
	if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
		return nil, err
	}

	sourceWorkspaceID := workspaceID
	if sourceChannel.Type == entity.ChannelTypeSavedGlobal {
		sourceWorkspaceID = uuid.Nil
	}
	savedFrom := entity.SavedFrom{
		UserID:    src.UserID,
		MessageID: src.ID,
		ChannelID: src.ChannelID,
		CreatedAt: src.CreatedAt,
	}
	if sourceWorkspaceID != uuid.Nil {
		savedFrom.WorkspaceID = &sourceWorkspaceID
	}
	savedFromRaw, err := json.Marshal(savedFrom)
	if err != nil {
		return nil, cerrors.Internal("failed to encode saved_from", err)
	}

	now := time.Now().UTC()
	copy := &entity.Message{
		ID:              id.New(),
		ChannelID:       selfChannel.ID,
		UserID:          userID,
		Content:         src.Content,
		Type:            src.Type,
		ForwardedFrom:   src.ForwardedFrom,
		SavedFrom:       savedFromRaw,
		QuotedMessageID: src.QuotedMessageID,
		QuotedSnapshot:  src.QuotedSnapshot,
		ProfileShare:    src.ProfileShare,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.messages.Create(ctx, copy); err != nil {
		return nil, err
	}
	s.publishMessageEvent(ctx, event.TypeMessageCreated, selfChannel, copy, userID)
	return &SaveResult{SavedMsgID: copy.ID, ChannelID: copy.ChannelID, CreatedAt: copy.CreatedAt}, nil
}

func (s *Service) UnsaveMessage(ctx context.Context, userID, savedMsgID uuid.UUID) error {
	msg, err := s.messages.GetByID(ctx, savedMsgID)
	if err != nil {
		return err
	}
	ch, err := s.channels.GetByID(ctx, msg.ChannelID)
	if err != nil {
		return err
	}
	if !ch.Type.IsSelfChannel() || ch.OwnerUserID == nil || *ch.OwnerUserID != userID {
		return cerrors.NotFound("saved message not found")
	}
	if _, err := s.messages.SoftDeleteWithCascade(ctx, savedMsgID); err != nil {
		return err
	}
	deletedMsg, err := s.messages.GetByID(ctx, savedMsgID)
	if err != nil {
		return err
	}
	s.publishMessageEvent(ctx, event.TypeMessageDeleted, ch, deletedMsg, userID)
	return nil
}

func (s *Service) ListMessages(ctx context.Context, userID uuid.UUID, mode entity.SavedMessagesMode, workspaceID *uuid.UUID, cursor string, limit int) (*postgres.SavedMessagesPage, error) {
	if mode != entity.SavedMessagesModePerWorkspace && mode != entity.SavedMessagesModeGlobal {
		return nil, cerrors.InvalidInput("mode is required")
	}
	if mode == entity.SavedMessagesModePerWorkspace && workspaceID == nil {
		return nil, cerrors.InvalidInput("workspaceId is required")
	}
	if mode == entity.SavedMessagesModeGlobal && workspaceID != nil {
		return nil, cerrors.InvalidInput("workspaceId is not allowed for global mode")
	}
	return s.saved.ListMessages(ctx, userID, mode, workspaceID, cursor, limit)
}

func (s *Service) publishMessageEvent(ctx context.Context, eventType event.Type, channel *entity.Channel, message *entity.Message, userID uuid.UUID) {
	if s.pubsub == nil || channel == nil || message == nil {
		return
	}
	subject := fmt.Sprintf("aloqa.chat.%s", channel.ID)
	workspaceID := uuid.Nil
	if channel.WorkspaceID != nil {
		workspaceID = *channel.WorkspaceID
	}
	_, body, _, err := event.Prepare(subject, event.Event{
		Type:        eventType,
		WorkspaceID: workspaceID,
		ChannelID:   channel.ID,
		UserID:      userID,
		Timestamp:   time.Now().UTC(),
		Payload:     event.NewMessagePayload(message, channel),
	})
	if err != nil {
		slog.ErrorContext(ctx, "failed to prepare saved message event", "type", eventType, "error", err)
		return
	}
	if err := s.pubsub.Publish(ctx, subject, body); err != nil {
		slog.ErrorContext(ctx, "failed to publish saved message event", "type", eventType, "subject", subject, "error", err)
	}
}
