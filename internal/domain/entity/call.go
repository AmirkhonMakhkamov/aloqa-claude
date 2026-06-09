package entity

import (
	"encoding/json"
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

// EntryMode controls how a non-guest member enters a call (set at creation, #4):
//   - EntryModeManualAdmit: the member is held in the waiting room until a host
//     admits them. This is the default for new group/meeting calls.
//   - EntryModePassword: the member must supply the correct join password.
//   - EntryModeOpen: the member joins directly.
//
// Guests (one-time link) always pass through the waiting room regardless of the
// mode (ALK-700), and bypass the password (the link is the host's approval).
type EntryMode string

const (
	EntryModeManualAdmit EntryMode = "manual_admit"
	EntryModePassword    EntryMode = "password"
	EntryModeOpen        EntryMode = "open"
)

// Valid reports whether m is a recognised entry mode.
func (m EntryMode) Valid() bool {
	switch m {
	case EntryModeManualAdmit, EntryModePassword, EntryModeOpen:
		return true
	default:
		return false
	}
}

type BreakoutCreationPolicy string

const (
	BreakoutCreationHost     BreakoutCreationPolicy = "host"
	BreakoutCreationEveryone BreakoutCreationPolicy = "everyone"
)

func (p BreakoutCreationPolicy) Valid() bool {
	switch p {
	case BreakoutCreationHost, BreakoutCreationEveryone:
		return true
	default:
		return false
	}
}

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

// AccessLevel controls who may join a channel-less call (ALK-814 / S6). It is
// stored in a dedicated calls column (default 'public'), never in the settings
// JSONB, and is inert for channel-attached calls (access = channel membership)
// and one_to_one (DM). 'private' restricts joining to the creator, host/co-host,
// invited members (call_invited_members), currently-connected participants, and
// guests holding a grant for this call.
type AccessLevel string

const (
	AccessLevelPublic  AccessLevel = "public"
	AccessLevelPrivate AccessLevel = "private"
)

func (a AccessLevel) Valid() bool {
	switch a {
	case AccessLevelPublic, AccessLevelPrivate:
		return true
	default:
		return false
	}
}

// Resolved returns the concrete access level, defaulting an empty/invalid value
// to public so a legacy call (pre-migration 058) stays open.
func (a AccessLevel) Resolved() AccessLevel {
	if a.Valid() {
		return a
	}
	return AccessLevelPublic
}

// WhoCanAddGuestsPolicy controls who may mint a call guest link (ALK-814 / S6).
// Stored in the settings JSONB; the restrictive default ('host') applies to a
// legacy call. For a private call the host/co-host requirement always applies
// regardless of this policy (enforced in the service).
type WhoCanAddGuestsPolicy string

const (
	WhoCanAddGuestsHost     WhoCanAddGuestsPolicy = "host"
	WhoCanAddGuestsEveryone WhoCanAddGuestsPolicy = "everyone"
)

func (p WhoCanAddGuestsPolicy) Valid() bool {
	return p == WhoCanAddGuestsHost || p == WhoCanAddGuestsEveryone
}

type CallSettings struct {
	WaitingRoom      bool                   `json:"waiting_room"`
	MuteOnJoin       bool                   `json:"mute_on_join"`
	Recording        bool                   `json:"recording"`
	ScreenSharing    bool                   `json:"screen_sharing"`
	Chat             bool                   `json:"chat"`
	BreakoutRooms    bool                   `json:"breakout_rooms"`
	BreakoutCreation BreakoutCreationPolicy `json:"breakout_creation"`
	MaxBreakoutRooms int                    `json:"max_breakout_rooms"`
	MaxParticipants  int                    `json:"max_participants"`
	E2EE             bool                   `json:"e2ee"`
	Watermark        bool                   `json:"watermark"`
	// EntryMode is stored in the settings JSONB. Empty on rows created before
	// migration 051 — ResolvedEntryMode derives it from WaitingRoom so legacy
	// calls behave exactly as before. The service normalises this to a concrete
	// value before persisting (StartCall) and after loading (repo reads), so the
	// API always returns one of the three EntryMode values.
	EntryMode EntryMode `json:"entry_mode"`
	// MembersCanUnmuteMic / MembersCanEnableCamera are the meeting-level member
	// permission policies (ALK-812 / S4). Pointers so a row created before this
	// feature (no key in the settings JSONB) resolves to the permissive default
	// (true) rather than the bool zero value (false): a nil pointer means "unset".
	// The call response serialises the resolved booleans (see MarshalJSON) so the
	// wire is always a concrete bool; the persisted JSONB keeps the pointer.
	MembersCanUnmuteMic    *bool `json:"members_can_unmute_mic"`
	MembersCanEnableCamera *bool `json:"members_can_enable_camera"`
	// WhoCanAddGuests (ALK-814 / S6) — empty resolves to host (restrictive).
	WhoCanAddGuests WhoCanAddGuestsPolicy `json:"who_can_add_guests"`
}

// ResolvedWhoCanAddGuests defaults an empty/invalid policy to host (only
// host/co-host may mint a guest link) — the restrictive, backward-compatible
// default for a call created before S6.
func (c CallSettings) ResolvedWhoCanAddGuests() WhoCanAddGuestsPolicy {
	if c.WhoCanAddGuests.Valid() {
		return c.WhoCanAddGuests
	}
	return WhoCanAddGuestsHost
}

// ResolvedMembersCanUnmuteMic reports whether members may unmute their mic,
// defaulting to true (permissive) for legacy calls that predate the field.
func (c CallSettings) ResolvedMembersCanUnmuteMic() bool {
	return c.MembersCanUnmuteMic == nil || *c.MembersCanUnmuteMic
}

// ResolvedMembersCanEnableCamera reports whether members may enable their
// camera, defaulting to true (permissive) for legacy calls.
func (c CallSettings) ResolvedMembersCanEnableCamera() bool {
	return c.MembersCanEnableCamera == nil || *c.MembersCanEnableCamera
}

// MarshalJSON emits the member-permission policies as concrete booleans (via the
// Resolved* accessors) so neither the API wire nor a re-persisted row ever carries
// null for these fields. The type alias drops CallSettings' own MarshalJSON to
// avoid infinite recursion and preserves every other field; the json-tag collision
// between the embedded alias' *bool fields (depth 1) and the outer bool fields
// (depth 0) resolves in favour of the shallower outer fields, so the resolved
// bool wins. The postgres repo persists settings via json.Marshal(call.Settings)
// (call.go:150/630), so this marshaler also governs persistence — that is benign:
// a legacy nil simply backfills to the permissive default (true) on its next
// write, and StartCall creates new calls with explicit pointers, so the
// unset-vs-explicit-true distinction is never load-bearing (both mean permissive).
func (c CallSettings) MarshalJSON() ([]byte, error) {
	type alias CallSettings
	return json.Marshal(struct {
		alias
		MembersCanUnmuteMic    bool `json:"members_can_unmute_mic"`
		MembersCanEnableCamera bool `json:"members_can_enable_camera"`
	}{
		alias:                  alias(c),
		MembersCanUnmuteMic:    c.ResolvedMembersCanUnmuteMic(),
		MembersCanEnableCamera: c.ResolvedMembersCanEnableCamera(),
	})
}

// ResolvedEntryMode returns the concrete entry mode, deriving it from the legacy
// WaitingRoom flag when EntryMode is unset (pre-#4 calls): waiting_room=true maps
// to manual_admit, otherwise open.
func (c CallSettings) ResolvedEntryMode() EntryMode {
	if c.EntryMode.Valid() {
		return c.EntryMode
	}
	if c.WaitingRoom {
		return EntryModeManualAdmit
	}
	return EntryModeOpen
}

func (c CallSettings) ResolvedBreakoutCreation() BreakoutCreationPolicy {
	if c.BreakoutCreation == BreakoutCreationEveryone {
		return BreakoutCreationEveryone
	}
	return BreakoutCreationHost
}

func (c CallSettings) ResolvedMaxBreakoutRooms() int {
	if c.MaxBreakoutRooms <= 0 {
		return 8
	}
	if c.MaxBreakoutRooms > 8 {
		return 8
	}
	if c.MaxBreakoutRooms < 1 {
		return 1
	}
	return c.MaxBreakoutRooms
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
	AccessLevel      AccessLevel      `json:"access_level"`
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
	ID              uuid.UUID  `json:"id"`
	WorkspaceID     uuid.UUID  `json:"workspace_id"`
	ChannelID       *uuid.UUID `json:"channel_id,omitempty"`
	Type            CallType   `json:"type"`
	Status          CallStatus `json:"status"`
	Title           string     `json:"title,omitempty"`
	CreatedBy       uuid.UUID  `json:"created_by"`
	ScheduledCallID *uuid.UUID `json:"scheduled_call_id,omitempty"`
	// AccessLevel (ALK-814 / S6) is stored in a dedicated calls column (migration
	// 058), default 'public'. Inert for channel-attached and one_to_one calls.
	AccessLevel AccessLevel  `json:"access_level"`
	Settings    CallSettings `json:"settings"`
	// JoinPasswordHash is the bcrypt hash of the password-mode join password. It
	// is stored in a dedicated calls column (migration 051), never in the
	// settings JSONB, and is marshalled with json:"-" so it is never exposed by
	// the API. Only JoinCall reads it (bcrypt compare). Hydrated by GetByID.
	JoinPasswordHash        string        `json:"-"`
	StartedAt               *time.Time    `json:"started_at,omitempty"`
	EndedAt                 *time.Time    `json:"ended_at,omitempty"`
	EndReason               CallEndReason `json:"end_reason,omitempty"`
	FeaturedShareUserID     *uuid.UUID    `json:"featured_share_user_id,omitempty"`
	PinnedParticipantUserID *uuid.UUID    `json:"pinned_participant_user_id,omitempty"`
	CreatedAt               time.Time     `json:"created_at"`
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
