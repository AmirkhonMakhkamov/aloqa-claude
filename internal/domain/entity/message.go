package entity

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type MessageType string

const (
	MessageTypeText   MessageType = "text"
	MessageTypeSystem MessageType = "system"
	MessageTypeFile   MessageType = "file"
)

type Message struct {
	ID        uuid.UUID   `json:"id"`
	ChannelID uuid.UUID   `json:"channel_id"`
	UserID    uuid.UUID   `json:"user_id"`
	ParentID  *uuid.UUID  `json:"parent_id,omitempty"`
	Content   string      `json:"content"`
	Type      MessageType `json:"type"`
	Edited    bool        `json:"edited"`
	EditedAt  *time.Time  `json:"edited_at,omitempty"`
	Pinned    bool        `json:"pinned"`
	PinnedBy  *uuid.UUID  `json:"pinned_by,omitempty"`
	PinnedAt  *time.Time  `json:"pinned_at,omitempty"`
	// JSON-typed forwarded-from envelope. Persisted verbatim; the FE owns the schema.
	ForwardedFrom json.RawMessage `json:"forwarded_from,omitempty" db:"forwarded_from"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	DeletedAt     *time.Time      `json:"deleted_at,omitempty"`

	// Aggregated fields (populated by queries, not stored directly).
	ReplyCount  int          `json:"reply_count,omitempty"`
	Reactions   []Reaction   `json:"reactions,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	User        *User        `json:"user,omitempty"`
}

type Reaction struct {
	ID        uuid.UUID `json:"id"`
	MessageID uuid.UUID `json:"message_id"`
	UserID    uuid.UUID `json:"user_id"`
	Emoji     string    `json:"emoji"`
	CreatedAt time.Time `json:"created_at"`
}

type Attachment struct {
	ID          uuid.UUID `json:"id"`
	MessageID   uuid.UUID `json:"message_id"`
	FileName    string    `json:"file_name"`
	FileSize    int64     `json:"file_size"`
	MimeType    string    `json:"mime_type"`
	StoragePath string    `json:"-"`
	URL         string    `json:"url,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}
