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
	ID                uuid.UUID         `json:"id"`
	Email             string            `json:"email"`
	DisplayName       string            `json:"display_name"`
	AvatarURL         string            `json:"avatar_url,omitempty"`
	PasswordHash      string            `json:"-"`
	Status            UserStatus        `json:"status"`
	DeactivatedAt     *time.Time        `json:"deactivated_at,omitempty"`
	SavedMessagesMode SavedMessagesMode `json:"saved_messages_mode"`
	Locale            string            `json:"locale"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}
