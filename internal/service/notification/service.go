package notification

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
)

// Type identifies notification categories.
type Type string

const (
	TypeMention     Type = "mention"
	TypeDM          Type = "dm"
	TypeReaction    Type = "reaction"
	TypeCallInvite  Type = "call_invite"
	TypeCallMissed  Type = "call_missed"
	TypeChannelJoin Type = "channel_join"
	TypeSystem      Type = "system"
)

// Notification represents an in-app notification stored for delivery.
type Notification struct {
	ID          uuid.UUID `json:"id"`
	UserID      uuid.UUID `json:"user_id"`
	WorkspaceID uuid.UUID `json:"workspace_id"`
	Type        Type      `json:"type"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	ResourceID  string    `json:"resource_id,omitempty"` // channel/message/call ID
	Read        bool      `json:"read"`
	CreatedAt   time.Time `json:"created_at"`
}

// PushProvider is the interface for external push notification services.
// Implementations can integrate FCM, APNs, or Web Push.
type PushProvider interface {
	SendPush(ctx context.Context, userID uuid.UUID, title, body string, data map[string]string) error
}

// Store persists notifications for in-app retrieval.
type Store interface {
	Save(ctx context.Context, n *Notification) error
	ListByUser(ctx context.Context, userID, workspaceID uuid.UUID, limit int) ([]Notification, error)
	MarkRead(ctx context.Context, notificationID, userID uuid.UUID) error
	MarkAllRead(ctx context.Context, userID, workspaceID uuid.UUID) error
	CountUnread(ctx context.Context, userID, workspaceID uuid.UUID) (int, error)
}

// Service handles notification delivery through multiple channels.
type Service struct {
	store         Store
	pushProviders []PushProvider
}

// NewService creates a new notification service.
func NewService(store Store, providers ...PushProvider) *Service {
	return &Service{store: store, pushProviders: providers}
}

// Send creates an in-app notification and optionally sends push notifications.
func (s *Service) Send(ctx context.Context, userID, workspaceID uuid.UUID, notifType Type, title, body, resourceID string) error {
	n := &Notification{
		ID:          id.New(),
		UserID:      userID,
		WorkspaceID: workspaceID,
		Type:        notifType,
		Title:       title,
		Body:        body,
		ResourceID:  resourceID,
		CreatedAt:   time.Now(),
	}

	if s.store != nil {
		if err := s.store.Save(ctx, n); err != nil {
			slog.ErrorContext(ctx, "failed to save notification", "user_id", userID, "error", err)
			return cerrors.Internal("failed to save notification", err)
		}
	}

	// Dispatch to push providers asynchronously.
	data := map[string]string{
		"type":         string(notifType),
		"workspace_id": workspaceID.String(),
		"resource_id":  resourceID,
	}
	for _, p := range s.pushProviders {
		go func(provider PushProvider) {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("push provider panicked", "user_id", userID, "panic", r)
				}
			}()
			pushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if err := provider.SendPush(pushCtx, userID, title, body, data); err != nil {
				slog.Error("failed to send push notification", "user_id", userID, "error", err)
			}
		}(p)
	}

	return nil
}

// ListNotifications returns recent notifications for a user.
func (s *Service) ListNotifications(ctx context.Context, userID, workspaceID uuid.UUID, limit int) ([]Notification, error) {
	if s.store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	return s.store.ListByUser(ctx, userID, workspaceID, limit)
}

// MarkRead marks a notification as read.
func (s *Service) MarkRead(ctx context.Context, notificationID, userID uuid.UUID) error {
	if s.store == nil {
		return nil
	}
	return s.store.MarkRead(ctx, notificationID, userID)
}

// MarkAllRead marks all notifications as read for a user in a workspace.
func (s *Service) MarkAllRead(ctx context.Context, userID, workspaceID uuid.UUID) error {
	if s.store == nil {
		return nil
	}
	return s.store.MarkAllRead(ctx, userID, workspaceID)
}

// CountUnread returns the number of unread notifications.
func (s *Service) CountUnread(ctx context.Context, userID, workspaceID uuid.UUID) (int, error) {
	if s.store == nil {
		return 0, nil
	}
	return s.store.CountUnread(ctx, userID, workspaceID)
}
