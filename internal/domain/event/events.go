package event

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/id"
)

const CurrentVersion = 1

// Type identifies the kind of domain event.
type Type string

type DeliverySemantic string

const (
	DeliveryEphemeral   DeliverySemantic = "ephemeral"
	DeliveryBestEffort  DeliverySemantic = "best_effort"
	DeliveryAtLeastOnce DeliverySemantic = "at_least_once"
)

const (
	// Chat events.
	TypeMessageCreated  Type = "message.created"
	TypeMessageUpdated  Type = "message.updated"
	TypeMessageDeleted  Type = "message.deleted"
	TypeReactionAdded   Type = "reaction.added"
	TypeReactionRemoved Type = "reaction.removed"
	TypeMessagePinned   Type = "message.pinned"
	TypeMessageUnpinned Type = "message.unpinned"
	TypeTypingStarted   Type = "typing.started"
	TypeChannelCreated  Type = "channel.created"
	TypeChannelUpdated  Type = "channel.updated"
	TypeMemberJoined    Type = "member.joined"
	TypeMemberLeft      Type = "member.left"
	TypePresenceChanged Type = "presence.changed"

	// Call events.
	TypeCallStarted            Type = "call.started"
	TypeCallEnded              Type = "call.ended"
	TypeCallParticipantJoined  Type = "call.participant.joined"
	TypeCallParticipantLeft    Type = "call.participant.left"
	TypeCallParticipantUpdated Type = "call.participant.updated"
	TypeCallQualityAdapted     Type = "call.quality.adapted"
	TypeCallMessageCreated     Type = "call.message.created"
	TypeCallMessageDeleted     Type = "call.message.deleted"
	TypeCallHandRaised         Type = "call.participant.hand_raised"
	TypeCallHandLowered        Type = "call.participant.hand_lowered"
	TypeCallReaction           Type = "call.reaction.added"
	TypeCallRecordingStarted   Type = "call.recording.started"
	TypeCallRecordingUpdated   Type = "call.recording.updated"
	// Screen-share permission control (ALK-697).
	TypeCallShareRequestCreated  Type = "call.share_request.created"
	TypeCallShareRequestResolved Type = "call.share_request.resolved"
	TypeCallFeaturedShareUpdated Type = "call.featured_share.updated"

	// Waiting room events.
	TypeWaitingRoomJoined   Type = "waiting_room.joined"
	TypeWaitingRoomAdmitted Type = "waiting_room.admitted"
	TypeWaitingRoomRejected Type = "waiting_room.rejected"

	// Breakout room events.
	TypeBreakoutRoomCreated      Type = "breakout.room.created"
	TypeBreakoutRoomClosed       Type = "breakout.room.closed"
	TypeBreakoutRoomsAllClosed   Type = "breakout.rooms.all_closed"
	TypeBreakoutParticipantMoved Type = "breakout.participant.moved"
	TypeBreakoutBroadcast        Type = "breakout.broadcast"

	// Signaling events (WebRTC).
	TypeSignalOffer     Type = "signal.offer"
	TypeSignalAnswer    Type = "signal.answer"
	TypeSignalCandidate Type = "signal.candidate"

	// Calendar events.
	TypeUserCalendarCreated         Type = "calendar.user.created"
	TypeUserCalendarUpdated         Type = "calendar.user.updated"
	TypeUserCalendarDeleted         Type = "calendar.user.deleted"
	TypeCalendarEventCreated        Type = "calendar.event.created"
	TypeCalendarEventUpdated        Type = "calendar.event.updated"
	TypeCalendarEventDeleted        Type = "calendar.event.deleted"
	TypeCalendarEventReminderFired  Type = "calendar.event.reminder_fired"
	TypeCalendarAttendeeRsvpUpdated Type = "calendar.attendee.rsvp_updated"
)

// Event is the envelope for all domain events published through the event bus.
type Event struct {
	ID               uuid.UUID        `json:"id"`
	Version          int              `json:"version"`
	Sequence         int64            `json:"sequence,omitempty"`
	Type             Type             `json:"type"`
	Subject          string           `json:"subject,omitempty"`
	WorkspaceID      uuid.UUID        `json:"workspace_id"`
	ChannelID        uuid.UUID        `json:"channel_id,omitempty"`
	UserID           uuid.UUID        `json:"user_id"`
	DeliverySemantic DeliverySemantic `json:"delivery_semantic,omitempty"`
	Replayable       bool             `json:"replayable"`
	Timestamp        time.Time        `json:"timestamp"`
	Payload          any              `json:"payload"`
}

type QueuedEvent struct {
	Event       Event
	Body        []byte
	Attempts    int
	MaxAttempts int
}

type Definition struct {
	Version          int
	DeliverySemantic DeliverySemantic
	Replayable       bool
}

func DefinitionForType(t Type) Definition {
	switch t {
	case TypeTypingStarted, TypeSignalOffer, TypeSignalAnswer, TypeSignalCandidate,
		TypeCallHandRaised, TypeCallHandLowered, TypeCallReaction,
		TypeCallShareRequestCreated, TypeCallShareRequestResolved, TypeCallFeaturedShareUpdated:
		return Definition{
			Version:          CurrentVersion,
			DeliverySemantic: DeliveryEphemeral,
			Replayable:       false,
		}
	case TypePresenceChanged, TypeCallQualityAdapted:
		return Definition{
			Version:          CurrentVersion,
			DeliverySemantic: DeliveryBestEffort,
			Replayable:       false,
		}
	default:
		return Definition{
			Version:          CurrentVersion,
			DeliverySemantic: DeliveryAtLeastOnce,
			Replayable:       true,
		}
	}
}

// Prepare normalizes an event envelope for transport or durable queuing.
// It fills in versioning, delivery semantics, replayability, IDs, and body.
func Prepare(subject string, evt Event) (Event, []byte, bool, error) {
	if evt.ID == uuid.Nil {
		evt.ID = id.New()
	}
	definition := DefinitionForType(evt.Type)
	evt.Version = definition.Version
	evt.Subject = subject
	evt.DeliverySemantic = definition.DeliverySemantic
	evt.Replayable = definition.Replayable
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now().UTC()
	} else {
		evt.Timestamp = evt.Timestamp.UTC()
	}
	body, err := json.Marshal(evt)
	if err != nil {
		return Event{}, nil, false, err
	}
	return evt, body, evt.DeliverySemantic == DeliveryAtLeastOnce, nil
}

// --- Payload types ---

type MessagePayload struct {
	Message            *entity.Message     `json:"message"`
	ChannelType        *entity.ChannelType `json:"channel_type,omitempty"`
	ChannelWorkspaceID *uuid.UUID          `json:"-"`
	SavedFromMessageID *uuid.UUID          `json:"saved_from_message_id,omitempty"`
}

type messagePayloadJSON struct {
	Message            *entity.Message     `json:"message"`
	ChannelType        *entity.ChannelType `json:"channel_type,omitempty"`
	ChannelWorkspaceID *uuid.UUID          `json:"channel_workspace_id,omitempty"`
	SavedFromMessageID *uuid.UUID          `json:"saved_from_message_id,omitempty"`
}

type selfChannelMessagePayloadJSON struct {
	Message            *entity.Message     `json:"message"`
	ChannelType        *entity.ChannelType `json:"channel_type"`
	ChannelWorkspaceID *uuid.UUID          `json:"channel_workspace_id"`
	SavedFromMessageID *uuid.UUID          `json:"saved_from_message_id,omitempty"`
}

func NewMessagePayload(message *entity.Message, channel *entity.Channel) MessagePayload {
	payload := MessagePayload{Message: message}
	if message == nil || channel == nil || !channel.Type.IsSelfChannel() {
		return payload
	}

	payload.ChannelType = &channel.Type
	payload.ChannelWorkspaceID = channel.WorkspaceID

	if len(message.SavedFrom) > 0 {
		var savedFrom entity.SavedFrom
		if err := json.Unmarshal(message.SavedFrom, &savedFrom); err == nil && savedFrom.MessageID != uuid.Nil {
			payload.SavedFromMessageID = &savedFrom.MessageID
		}
	}

	return payload
}

func (p MessagePayload) MarshalJSON() ([]byte, error) {
	if p.ChannelType == nil {
		return json.Marshal(messagePayloadJSON{
			Message:            p.Message,
			ChannelType:        p.ChannelType,
			ChannelWorkspaceID: p.ChannelWorkspaceID,
			SavedFromMessageID: p.SavedFromMessageID,
		})
	}

	return json.Marshal(selfChannelMessagePayloadJSON{
		Message:            p.Message,
		ChannelType:        p.ChannelType,
		ChannelWorkspaceID: p.ChannelWorkspaceID,
		SavedFromMessageID: p.SavedFromMessageID,
	})
}

type ReactionPayload struct {
	MessageID uuid.UUID `json:"message_id"`
	ChannelID uuid.UUID `json:"channel_id"`
	UserID    uuid.UUID `json:"user_id"`
	Emoji     string    `json:"emoji"`
}

type PinPayload struct {
	MessageID uuid.UUID `json:"message_id"`
	ChannelID uuid.UUID `json:"channel_id"`
	UserID    uuid.UUID `json:"user_id"`
}

type TypingPayload struct {
	ChannelID uuid.UUID `json:"channel_id"`
	UserID    uuid.UUID `json:"user_id"`
}

type ChannelPayload struct {
	Channel *entity.Channel `json:"channel"`
}

type MemberPayload struct {
	ChannelID uuid.UUID `json:"channel_id"`
	UserID    uuid.UUID `json:"user_id"`
}

type PresencePayload struct {
	UserID       uuid.UUID  `json:"user_id"`
	WorkspaceID  uuid.UUID  `json:"workspace_id"`
	Status       string     `json:"status"`
	Online       bool       `json:"online"`
	CustomStatus string     `json:"custom_status,omitempty"`
	CustomEmoji  string     `json:"custom_emoji,omitempty"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
}

type CallPayload struct {
	Call *entity.Call `json:"call"`
}

type CallMessagePayload struct {
	CallID  uuid.UUID          `json:"call_id"`
	Message entity.CallMessage `json:"message"`
}

type CallMessageDeletedPayload struct {
	CallID    uuid.UUID `json:"call_id"`
	MessageID uuid.UUID `json:"message_id"`
}

type CallParticipantPayload struct {
	CallID      uuid.UUID               `json:"call_id"`
	Participant *entity.CallParticipant `json:"participant"`
}

type CallQualityPayload struct {
	CallID              uuid.UUID `json:"call_id"`
	UserID              uuid.UUID `json:"user_id"`
	StreamID            string    `json:"stream_id"`
	Source              string    `json:"source,omitempty"`
	PreviousQuality     string    `json:"previous_quality"`
	TargetQuality       string    `json:"target_quality"`
	NetworkGrade        string    `json:"network_grade"`
	AudioPriority       bool      `json:"audio_priority"`
	VideoSuspended      bool      `json:"video_suspended"`
	SyncMode            string    `json:"sync_mode"`
	VideoDegradeMode    string    `json:"video_degrade_mode"`
	MaxVideoBitrateKbps int       `json:"max_video_bitrate_kbps"`
	MaxVideoFPS         int       `json:"max_video_fps"`
	TargetAudioBufferMs int       `json:"target_audio_buffer_ms"`
	TargetVideoBufferMs int       `json:"target_video_buffer_ms"`
	LipSyncWindowMs     int       `json:"lip_sync_window_ms"`
	Reasons             []string  `json:"reasons,omitempty"`
}

type CallHandPayload struct {
	CallID uuid.UUID `json:"call_id"`
	UserID uuid.UUID `json:"user_id"`
}

type CallReactionPayload struct {
	CallID uuid.UUID `json:"call_id"`
	UserID uuid.UUID `json:"user_id"`
	Emoji  string    `json:"emoji"`
}

// CallRecordingPayload carries the full recording row (incl. status) for the
// call.recording.started / call.recording.updated events (ALK-701). Matches the
// FE CallRecordingPayloadSchema.
type CallRecordingPayload struct {
	CallID    uuid.UUID         `json:"call_id"`
	Recording *entity.Recording `json:"recording"`
}

// ShareRequestPayload announces that a participant requested permission to
// screen-share. (ALK-697)
type ShareRequestPayload struct {
	CallID          uuid.UUID `json:"call_id"`
	RequesterUserID uuid.UUID `json:"requester_user_id"`
}

// ShareRequestResolvedPayload announces that a host approved or denied a
// screen-share request. (ALK-697)
type ShareRequestResolvedPayload struct {
	CallID          uuid.UUID `json:"call_id"`
	RequesterUserID uuid.UUID `json:"requester_user_id"`
	Approved        bool      `json:"approved"`
}

// FeaturedSharePayload announces the host's featured-share pick for everyone;
// a nil FeaturedShareUserID clears the featured share. (ALK-697)
type FeaturedSharePayload struct {
	CallID              uuid.UUID  `json:"call_id"`
	FeaturedShareUserID *uuid.UUID `json:"featured_share_user_id"`
}

type BreakoutRoomPayload struct {
	CallID uuid.UUID            `json:"call_id"`
	Room   *entity.BreakoutRoom `json:"room"`
}

type BreakoutRoomsAllClosedPayload struct {
	CallID uuid.UUID `json:"call_id"`
}

type BreakoutParticipantMovedPayload struct {
	CallID         uuid.UUID  `json:"call_id"`
	UserID         uuid.UUID  `json:"user_id"`
	BreakoutRoomID *uuid.UUID `json:"breakout_room_id"` // nil = returned to main room
}

type BreakoutBroadcastPayload struct {
	CallID  uuid.UUID `json:"call_id"`
	UserID  uuid.UUID `json:"user_id"`
	Message string    `json:"message"`
}

type UserCalendarPayload struct {
	Calendar entity.UserCalendar `json:"calendar"`
}

type CalendarEventPayload struct {
	Event entity.CalendarEvent `json:"event"`
}

type CalendarRsvpPayload struct {
	EventID  uuid.UUID            `json:"event_id"`
	Attendee entity.EventAttendee `json:"attendee"`
}

type SignalPayload struct {
	CallID   uuid.UUID `json:"call_id"`
	FromUser uuid.UUID `json:"from_user"`
	ToUser   uuid.UUID `json:"to_user"`
	SDP      string    `json:"sdp,omitempty"`
	Type     string    `json:"type,omitempty"`

	// ICE candidate fields.
	Candidate     string `json:"candidate,omitempty"`
	SDPMid        string `json:"sdp_mid,omitempty"`
	SDPMLineIndex *int   `json:"sdp_mline_index,omitempty"`
}
