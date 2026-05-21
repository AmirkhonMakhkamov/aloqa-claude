package entity

import (
	"time"

	"github.com/google/uuid"
)

type UserStatus string

const (
	UserStatusActive      UserStatus = "active"
	UserStatusSuspended   UserStatus = "suspended"
	UserStatusDeactivated UserStatus = "deactivated"
)

type SavedMessagesMode string

const (
	SavedMessagesModePerWorkspace SavedMessagesMode = "per_workspace"
	SavedMessagesModeGlobal       SavedMessagesMode = "global"
)

type User struct {
	ID          uuid.UUID `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
	// Position is the user's human-readable job title (for example
	// "Senior Software Engineer, Platform Infrastructure"). Nullable; absent
	// for users that haven't set one. Marshalled as `position: null` when
	// unset so the client can distinguish "missing" from "empty string".
	Position *string `json:"position"`
	// Department is the org-chart bucket (e.g. "Engineering", "Product",
	// "Design"). Nullable, free-form text.
	Department        *string           `json:"department"`
	PasswordHash      string            `json:"-"`
	Status            UserStatus        `json:"status"`
	DeactivatedAt     *time.Time        `json:"deactivated_at,omitempty"`
	SavedMessagesMode SavedMessagesMode `json:"saved_messages_mode"`
	Locale            string            `json:"locale"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}
