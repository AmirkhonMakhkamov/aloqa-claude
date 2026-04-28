package pagination

import (
	"encoding/base64"

	"github.com/google/uuid"
)

const DefaultLimit = 50
const MaxLimit = 100

// Params holds pagination parameters. Supports both cursor-based and offset-based.
type Params struct {
	Cursor uuid.UUID
	Limit  int
	Offset int
}

// Normalize clamps Limit to [1, MaxLimit] and defaults to DefaultLimit.
func (p *Params) Normalize() {
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}
}

// Page is a generic paginated response.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

// EncodeCursor encodes a UUID as a base64 cursor string.
func EncodeCursor(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return base64.URLEncoding.EncodeToString(id[:])
}

// DecodeCursor decodes a base64 cursor string to a UUID.
func DecodeCursor(cursor string) (uuid.UUID, error) {
	if cursor == "" {
		return uuid.Nil, nil
	}
	b, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return uuid.Nil, err
	}
	return uuid.FromBytes(b)
}
