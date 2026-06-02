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
	AvatarColor string    `json:"avatar_color"`
	// Position is the user's human-readable job title (for example
	// "Senior Software Engineer, Platform Infrastructure"). Nullable; absent
	// for users that haven't set one. Marshalled as `position: null` when
	// unset so the client can distinguish "missing" from "empty string".
	Position *string `json:"position"`
	// Department is the org-chart bucket (e.g. "Engineering", "Product",
	// "Design"). Nullable, free-form text.
	Department *string `json:"department"`
	// Phone is a free-form contact number (any format; the client formats
	// it for display). Nullable; absent for users that haven't set one.
	Phone *string `json:"phone"`
	// Timezone is the user's IANA timezone name (e.g. "Asia/Tashkent",
	// "Europe/Moscow", "America/Los_Angeles"). Nullable; clients fall back
	// to a missing-tz UX when absent. Stored as TEXT so the backend stays
	// agnostic of the tz-database version on the client.
	Timezone          *string           `json:"timezone"`
	PasswordHash      string            `json:"-"`
	Status            UserStatus        `json:"status"`
	DeactivatedAt     *time.Time        `json:"deactivated_at,omitempty"`
	SavedMessagesMode SavedMessagesMode `json:"saved_messages_mode"`
	Locale            string            `json:"locale"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}
