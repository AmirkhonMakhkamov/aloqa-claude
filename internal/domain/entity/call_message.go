package entity

import (
	"time"

	"github.com/google/uuid"
)

type CallMessage struct {
	ID        uuid.UUID  `json:"id"`
	CallID    uuid.UUID  `json:"call_id"`
	SenderID  uuid.UUID  `json:"sender_id"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"created_at"`
	DeletedAt *time.Time `json:"deleted_at"`
}
