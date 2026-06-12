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

type QuotedSnapshot struct {
	UserID          uuid.UUID  `json:"user_id" db:"user_id"`
	ContentExcerpt  string     `json:"content_excerpt" db:"content_excerpt"`
	CreatedAt       time.Time  `json:"created_at" db:"created_at"`
	Deleted         *bool      `json:"deleted,omitempty" db:"deleted,omitempty"`
	ParentMessageID *uuid.UUID `json:"parent_message_id,omitempty" db:"parent_message_id,omitempty"`
}

type ProfileShareSnapshot struct {
	DisplayName string        `json:"display_name"`
	AvatarURL   string        `json:"avatar_url,omitempty"`
	AvatarColor string        `json:"avatar_color"`
	Role        WorkspaceRole `json:"role"`
	Position    *string       `json:"position,omitempty"`
	Department  *string       `json:"department,omitempty"`
}

type ProfileShare struct {
	UserID      uuid.UUID            `json:"user_id" db:"user_id"`
	WorkspaceID uuid.UUID            `json:"workspace_id" db:"workspace_id"`
	Snapshot    ProfileShareSnapshot `json:"snapshot" db:"snapshot"`
}

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
	ForwardedFrom   json.RawMessage `json:"forwarded_from,omitempty" db:"forwarded_from"`
	SavedFrom       json.RawMessage `json:"saved_from,omitempty" db:"saved_from"`
	QuotedMessageID *uuid.UUID      `json:"quoted_message_id,omitempty" db:"quoted_message_id"`
	QuotedSnapshot  *QuotedSnapshot `json:"quoted_snapshot,omitempty" db:"quoted_snapshot"`
	ProfileShare    *ProfileShare   `json:"profile_share,omitempty" db:"profile_share"`
	// JSON-typed call-event envelope on a type='system' message written into the
	// call's channel/DM timeline when a call ends. Persisted verbatim; the FE
	// owns the schema (call_id, type, end_reason, duration, participants).
	CallEvent json.RawMessage `json:"call_event,omitempty" db:"call_event"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	DeletedAt *time.Time      `json:"deleted_at,omitempty"`

	// HereRecipients is the snapshot of members who were online when an @here
	// broadcast was sent. Persisted so the mention feed stays consistent on
	// reload (presence is transient). Server-side only (ALK broadcast mentions).
	HereRecipients []uuid.UUID `json:"-" db:"mention_here_recipients"`

	// Aggregated fields (populated by queries, not stored directly).
	ReplyCount  int           `json:"reply_count,omitempty"`
	Reactions   []Reaction    `json:"reactions,omitempty"`
	Mentions    []uuid.UUID   `json:"mentions"`
	Attachments []Attachment  `json:"attachments,omitempty"`
	FileIDs     []uuid.UUID   `json:"file_ids,omitempty" db:"file_ids"`
	Files       []MessageFile `json:"files,omitempty"`
	User        *User         `json:"user,omitempty"`

	// Transient echo of the client-supplied id on send. NOT persisted (absent
	// from the messages columns); set only on the in-memory message before the
	// message.created event/HTTP response so the client can match its optimistic
	// row by exact id rather than a content+timestamp heuristic (ALK-440).
	ClientMessageID *string `json:"client_message_id,omitempty"`
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
