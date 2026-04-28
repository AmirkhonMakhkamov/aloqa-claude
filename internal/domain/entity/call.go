package entity

import (
	"time"

	"github.com/google/uuid"
)

type CallType string

const (
	CallTypeOneToOne CallType = "one_to_one"
	CallTypeGroup    CallType = "group"
	CallTypeMeeting  CallType = "meeting"
	CallTypeWebinar  CallType = "webinar"
	CallTypeSelector CallType = "selector"
)

type CallStatus string

const (
	CallStatusRinging CallStatus = "ringing"
	CallStatusActive  CallStatus = "active"
	CallStatusEnded   CallStatus = "ended"
)

type CallRole string

const (
	CallRoleHost        CallRole = "host"
	CallRoleCoHost      CallRole = "co_host"
	CallRolePresenter   CallRole = "presenter"
	CallRoleParticipant CallRole = "participant"
	CallRoleViewer      CallRole = "viewer"
)

type ParticipantStatus string

const (
	ParticipantStatusInvited      ParticipantStatus = "invited"
	ParticipantStatusWaiting      ParticipantStatus = "waiting"
	ParticipantStatusJoining      ParticipantStatus = "joining"
	ParticipantStatusConnected    ParticipantStatus = "connected"
	ParticipantStatusDisconnected ParticipantStatus = "disconnected"
)

type CallSettings struct {
	WaitingRoom       bool `json:"waiting_room"`
	MuteOnJoin        bool `json:"mute_on_join"`
	Recording         bool `json:"recording"`
	ScreenSharing     bool `json:"screen_sharing"`
	Chat              bool `json:"chat"`
	BreakoutRooms     bool `json:"breakout_rooms"`
	MaxParticipants   int  `json:"max_participants"`
	E2EE              bool `json:"e2ee"`
	Watermark         bool `json:"watermark"`
}

type Call struct {
	ID          uuid.UUID    `json:"id"`
	WorkspaceID uuid.UUID    `json:"workspace_id"`
	ChannelID   *uuid.UUID   `json:"channel_id,omitempty"`
	Type        CallType     `json:"type"`
	Status      CallStatus   `json:"status"`
	Title       string       `json:"title,omitempty"`
	CreatedBy   uuid.UUID    `json:"created_by"`
	Settings    CallSettings `json:"settings"`
	StartedAt   *time.Time   `json:"started_at,omitempty"`
	EndedAt     *time.Time   `json:"ended_at,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
}

type CallParticipant struct {
	ID             uuid.UUID         `json:"id"`
	CallID         uuid.UUID         `json:"call_id"`
	UserID         uuid.UUID         `json:"user_id"`
	BreakoutRoomID *uuid.UUID        `json:"breakout_room_id,omitempty"`
	Role           CallRole          `json:"role"`
	Status         ParticipantStatus `json:"status"`
	AudioMuted     bool              `json:"audio_muted"`
	VideoMuted     bool              `json:"video_muted"`
	ScreenSharing  bool              `json:"screen_sharing"`
	JoinedAt       *time.Time        `json:"joined_at,omitempty"`
	LeftAt         *time.Time        `json:"left_at,omitempty"`
}

// --- Breakout Rooms ---

type BreakoutRoomStatus string

const (
	BreakoutRoomStatusActive BreakoutRoomStatus = "active"
	BreakoutRoomStatusClosed BreakoutRoomStatus = "closed"
)

// BreakoutRoom represents a temporary sub-session within a parent call.
// Participants can be moved from the main room into breakout rooms for
// private discussions, and returned to the main room when done.
type BreakoutRoom struct {
	ID        uuid.UUID          `json:"id"`
	CallID    uuid.UUID          `json:"call_id"`
	Name      string             `json:"name"`
	CreatedBy uuid.UUID          `json:"created_by"`
	TimeLimit *int               `json:"time_limit,omitempty"` // seconds; nil = no limit
	Status    BreakoutRoomStatus `json:"status"`
	CreatedAt time.Time          `json:"created_at"`
	ClosedAt  *time.Time         `json:"closed_at,omitempty"`
}
