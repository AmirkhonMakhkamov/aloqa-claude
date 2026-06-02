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

type CallEndReason string

const (
	CallEndReasonHostEnded CallEndReason = "host_ended"
	CallEndReasonAllLeft   CallEndReason = "all_left"
	CallEndReasonFailed    CallEndReason = "failed"
	CallEndReasonMissed    CallEndReason = "missed"
	CallEndReasonCancelled CallEndReason = "cancelled"
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
	// ParticipantStatusDeclined is a terminal state for a (guest) participant the
	// host rejected from the waiting room; a subsequent JoinCall is refused with
	// WAITING_ROOM_DECLINED instead of re-creating a waiting row (ALK-700).
	ParticipantStatusDeclined ParticipantStatus = "declined"
)

type ParticipantLeftReason string

const (
	ParticipantLeftReasonLeft     ParticipantLeftReason = "left"
	ParticipantLeftReasonTimeout  ParticipantLeftReason = "timeout"
	ParticipantLeftReasonDeclined ParticipantLeftReason = "declined"
	ParticipantLeftReasonMissed   ParticipantLeftReason = "missed"
)

type CallSettings struct {
	WaitingRoom     bool `json:"waiting_room"`
	MuteOnJoin      bool `json:"mute_on_join"`
	Recording       bool `json:"recording"`
	ScreenSharing   bool `json:"screen_sharing"`
	Chat            bool `json:"chat"`
	BreakoutRooms   bool `json:"breakout_rooms"`
	MaxParticipants int  `json:"max_participants"`
	E2EE            bool `json:"e2ee"`
	Watermark       bool `json:"watermark"`
}

// TopParticipant is a thin user projection used by ActiveCallSummary to
// render avatar stacks on the Calls Home Live Now section. AvatarColor is the
// persisted fallback color; ColorSeed is kept for older clients.
type TopParticipant struct {
	UserID      uuid.UUID `json:"user_id"`
	DisplayName string    `json:"display_name"`
	AvatarURL   *string   `json:"avatar_url"`
	AvatarColor string    `json:"avatar_color"`
	ColorSeed   *int      `json:"color_seed"`
}

// ActiveCallSummary is the projection returned by GET /calls/active. It joins
// the raw call row with its channel, host, and connected participants so the
// FE can render a Live Now card without follow-up requests. Matches the FE
// schema in packages/core/src/api/calls.ts (ActiveCallSummarySchema).
type ActiveCallSummary struct {
	ID               uuid.UUID        `json:"id"`
	Type             CallType         `json:"type"`
	Title            *string          `json:"title"`
	StartedAt        time.Time        `json:"started_at"`
	ChannelID        *uuid.UUID       `json:"channel_id"`
	ChannelName      *string          `json:"channel_name"`
	HostUserID       uuid.UUID        `json:"host_user_id"`
	HostDisplayName  string           `json:"host_display_name"`
	Recording        bool             `json:"recording"`
	IsOpen           bool             `json:"is_open"`
	ParticipantCount int              `json:"participant_count"`
	TopParticipants  []TopParticipant `json:"top_participants"`
	ObserverCount    int              `json:"observer_count"`
}

// ActiveCallObservation is the cross-workspace projection exported to
// observability systems for the staging calls dashboard.
type ActiveCallObservation struct {
	ID               uuid.UUID  `json:"id"`
	WorkspaceID      uuid.UUID  `json:"workspace_id"`
	Type             CallType   `json:"type"`
	Status           CallStatus `json:"status"`
	Title            *string    `json:"title"`
	StartedAt        time.Time  `json:"started_at"`
	ChannelName      *string    `json:"channel_name"`
	HostDisplayName  string     `json:"host_display_name"`
	Recording        bool       `json:"recording"`
	IsOpen           bool       `json:"is_open"`
	ParticipantCount int        `json:"participant_count"`
	ObserverCount    int        `json:"observer_count"`
}

type Call struct {
	ID                  uuid.UUID     `json:"id"`
	WorkspaceID         uuid.UUID     `json:"workspace_id"`
	ChannelID           *uuid.UUID    `json:"channel_id,omitempty"`
	Type                CallType      `json:"type"`
	Status              CallStatus    `json:"status"`
	Title               string        `json:"title,omitempty"`
	CreatedBy           uuid.UUID     `json:"created_by"`
	ScheduledCallID     *uuid.UUID    `json:"scheduled_call_id,omitempty"`
	Settings            CallSettings  `json:"settings"`
	StartedAt           *time.Time    `json:"started_at,omitempty"`
	EndedAt             *time.Time    `json:"ended_at,omitempty"`
	EndReason           CallEndReason `json:"end_reason,omitempty"`
	FeaturedShareUserID *uuid.UUID    `json:"featured_share_user_id,omitempty"`
	CreatedAt           time.Time     `json:"created_at"`
}

type CallParticipant struct {
	ID             uuid.UUID             `json:"id"`
	CallID         uuid.UUID             `json:"call_id"`
	UserID         uuid.UUID             `json:"user_id"`
	BreakoutRoomID *uuid.UUID            `json:"breakout_room_id,omitempty"`
	Role           CallRole              `json:"role"`
	Status         ParticipantStatus     `json:"status"`
	AudioMuted     bool                  `json:"audio_muted"`
	VideoMuted     bool                  `json:"video_muted"`
	ScreenSharing  bool                  `json:"screen_sharing"`
	CanScreenShare bool                  `json:"can_screen_share"`
	JoinedAt       *time.Time            `json:"joined_at,omitempty"`
	LeftAt         *time.Time            `json:"left_at,omitempty"`
	LeftReason     ParticipantLeftReason `json:"left_reason,omitempty"`
	// DisplayName and IsGuest are denormalized, read-time-only projections (not
	// persisted on call_participants) so the FE can render "{name} (Guest)" for
	// participants who are not workspace members and thus absent from the
	// workspace-members lookup (ALK-700). Populated by the participant repo reads.
	DisplayName string `json:"display_name,omitempty"`
	IsGuest     bool   `json:"is_guest"`
}

type LiveKitWebhookEvent struct {
	EventID        string     `json:"event_id"`
	CallID         uuid.UUID  `json:"call_id"`
	EventType      string     `json:"event_type"`
	Status         string     `json:"status"`
	ClaimToken     string     `json:"claim_token"`
	ReceivedAt     time.Time  `json:"received_at"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	ProcessedAt    *time.Time `json:"processed_at,omitempty"`
}

type LiveKitWebhookClaimResult string

const (
	LiveKitWebhookClaimProcess    LiveKitWebhookClaimResult = "process"
	LiveKitWebhookClaimDuplicate  LiveKitWebhookClaimResult = "duplicate"
	LiveKitWebhookClaimInProgress LiveKitWebhookClaimResult = "in_progress"
)

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
	ClosesAt  *time.Time         `json:"closes_at"`
	Status    BreakoutRoomStatus `json:"status"`
	CreatedAt time.Time          `json:"created_at"`
	ClosedAt  *time.Time         `json:"closed_at,omitempty"`
}
