package entity

import (
	"time"

	"github.com/google/uuid"
)

// MentionRow is a read model surfacing one @-mention event to the Mentions
// screen. The shape is denormalised server-side so the frontend can render
// the card without per-row roundtrips for channel / author metadata.
type MentionRow struct {
	MessageID       uuid.UUID  `json:"message_id"`
	ChannelID       uuid.UUID  `json:"channel_id"`
	ChannelName     string     `json:"channel_name"`
	ChannelType     string     `json:"channel_type"`
	ChannelArchived bool       `json:"channel_archived"`
	AuthorID        uuid.UUID  `json:"author_id"`
	AuthorName      string     `json:"author_name"`
	AuthorAvatarURL string     `json:"author_avatar_url"`
	AuthorEmail     string     `json:"author_email"`
	Body            string     `json:"body"`
	CreatedAt       time.Time  `json:"created_at"`
	ParentMessageID *uuid.UUID `json:"parent_message_id,omitempty"`
}
