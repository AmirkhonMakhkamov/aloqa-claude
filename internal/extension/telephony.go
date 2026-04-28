package extension

import (
	"context"

	"github.com/google/uuid"
)

// TelephonyProvider abstracts SIP/PSTN telephony integration. Implementations
// connect to specific SIP trunk providers or PBX systems.
type TelephonyProvider interface {
	// MakeCall initiates an outbound call to a phone number.
	MakeCall(ctx context.Context, workspaceID uuid.UUID, fromExtension, toNumber string) (callID string, err error)
	// TransferCall transfers an active call to another extension or number.
	TransferCall(ctx context.Context, callID, targetExtension string) error
	// HangUp terminates an active call.
	HangUp(ctx context.Context, callID string) error
	// ListExtensions returns all SIP extensions for a workspace.
	ListExtensions(ctx context.Context, workspaceID uuid.UUID) ([]Extension, error)
	// RegisterExtension provisions a new SIP extension.
	RegisterExtension(ctx context.Context, workspaceID, userID uuid.UUID, number string) (*Extension, error)
}

// Extension represents a SIP extension assigned to a user.
type Extension struct {
	ID          uuid.UUID `json:"id"`
	WorkspaceID uuid.UUID `json:"workspace_id"`
	UserID      uuid.UUID `json:"user_id"`
	Number      string    `json:"number"`
	DisplayName string    `json:"display_name"`
	Status      string    `json:"status"` // registered, unregistered
}

// IVRMenu represents an interactive voice response menu.
type IVRMenu struct {
	ID          uuid.UUID    `json:"id"`
	WorkspaceID uuid.UUID    `json:"workspace_id"`
	Name        string       `json:"name"`
	Greeting    string       `json:"greeting"` // Audio file or TTS text
	Options     []IVROption  `json:"options"`
}

// IVROption maps a DTMF digit to an action.
type IVROption struct {
	Digit       string `json:"digit"`
	Action      string `json:"action"`      // transfer, queue, voicemail, submenu
	Destination string `json:"destination"` // Extension, queue ID, or submenu ID
}

// CallQueue represents a call queue for distributing inbound calls.
type CallQueue struct {
	ID           uuid.UUID `json:"id"`
	WorkspaceID  uuid.UUID `json:"workspace_id"`
	Name         string    `json:"name"`
	Strategy     string    `json:"strategy"` // round_robin, least_recent, simultaneous, random
	MaxWaitTime  int       `json:"max_wait_time"` // seconds
	MusicOnHold  string    `json:"music_on_hold"`
	Members      []uuid.UUID `json:"members"` // Agent user IDs
}
