package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const SessionRevokedSubject = "aloqa.session.evicted"

type SessionEventPublisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

type SessionEventNotifier interface {
	SessionRevoked(ctx context.Context, sessionID string, userID uuid.UUID) error
}

type SessionRevokedEvent struct {
	SessionID string    `json:"session_id"`
	UserID    uuid.UUID `json:"user_id"`
	Timestamp time.Time `json:"timestamp"`
}

type PubSubSessionNotifier struct {
	pub SessionEventPublisher
}

func NewPubSubSessionNotifier(pub SessionEventPublisher) *PubSubSessionNotifier {
	return &PubSubSessionNotifier{pub: pub}
}

func (n *PubSubSessionNotifier) SessionRevoked(ctx context.Context, sessionID string, userID uuid.UUID) error {
	if n == nil || n.pub == nil || sessionID == "" || userID == uuid.Nil {
		return nil
	}
	payload, err := json.Marshal(SessionRevokedEvent{
		SessionID: sessionID,
		UserID:    userID,
		Timestamp: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("marshal session revoked event: %w", err)
	}
	if err := n.pub.Publish(ctx, SessionRevokedSubject, payload); err != nil {
		return fmt.Errorf("publish session revoked event: %w", err)
	}
	return nil
}
