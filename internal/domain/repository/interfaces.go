package repository

import (
	"context"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/pagination"
)

// UserRepository manages user persistence.
type UserRepository interface {
	Create(ctx context.Context, user *entity.User) error
	GetByID(ctx context.Context, id uuid.UUID) (*entity.User, error)
	GetByEmail(ctx context.Context, email string) (*entity.User, error)
	Update(ctx context.Context, user *entity.User) error
}

// WorkspaceRepository manages workspace persistence.
type WorkspaceRepository interface {
	Create(ctx context.Context, ws *entity.Workspace) error
	GetByID(ctx context.Context, id uuid.UUID) (*entity.Workspace, error)
	GetBySlug(ctx context.Context, slug string) (*entity.Workspace, error)
	ListByUser(ctx context.Context, userID uuid.UUID) ([]entity.Workspace, error)
	Update(ctx context.Context, ws *entity.Workspace) error

	AddMember(ctx context.Context, m *entity.WorkspaceMember) error
	GetMember(ctx context.Context, workspaceID, userID uuid.UUID) (*entity.WorkspaceMember, error)
	ListMembers(ctx context.Context, workspaceID uuid.UUID, p pagination.Params) ([]entity.WorkspaceMember, error)
	UpdateMemberRole(ctx context.Context, workspaceID, userID uuid.UUID, role entity.WorkspaceRole) error
	RemoveMember(ctx context.Context, workspaceID, userID uuid.UUID) error
}

type WorkspaceCollaborationRepository interface {
	CreateConnection(ctx context.Context, connection *entity.WorkspaceConnection) error
	GetConnection(ctx context.Context, sourceWorkspaceID, targetWorkspaceID uuid.UUID) (*entity.WorkspaceConnection, error)
	ListConnections(ctx context.Context, workspaceID uuid.UUID, p pagination.Params) ([]entity.WorkspaceConnection, error)
	UpdateConnectionPolicy(ctx context.Context, id uuid.UUID, policy entity.WorkspaceConnectionPolicy) error
	UpdateConnectionStatus(ctx context.Context, id uuid.UUID, status entity.WorkspaceConnectionStatus, approvedBy *uuid.UUID) error
}

type WorkspaceRoleRepository interface {
	CreateDefinition(ctx context.Context, role *entity.WorkspaceRoleDefinition) error
	GetDefinition(ctx context.Context, workspaceID, roleID uuid.UUID) (*entity.WorkspaceRoleDefinition, error)
	ListDefinitions(ctx context.Context, workspaceID uuid.UUID) ([]entity.WorkspaceRoleDefinition, error)
	UpdateDefinition(ctx context.Context, role *entity.WorkspaceRoleDefinition) error
	DeleteDefinition(ctx context.Context, workspaceID, roleID uuid.UUID) error
	AssignRole(ctx context.Context, assignment *entity.WorkspaceRoleAssignment) error
	UnassignRole(ctx context.Context, workspaceID, userID, roleID uuid.UUID) error
	ListAssignedDefinitions(ctx context.Context, workspaceID, userID uuid.UUID) ([]entity.WorkspaceRoleDefinition, error)
}

type ChannelAccessGrantRepository interface {
	CreateGrant(ctx context.Context, grant *entity.ChannelAccessGrant) error
	GetGrant(ctx context.Context, channelID, userID uuid.UUID) (*entity.ChannelAccessGrant, error)
	ListByChannel(ctx context.Context, channelID uuid.UUID) ([]entity.ChannelAccessGrant, error)
}

type ChannelAccessStateRepository interface {
	GetState(ctx context.Context, channelID, userID uuid.UUID) (*entity.ChannelAccessState, error)
	UpsertState(ctx context.Context, state *entity.ChannelAccessState) error
}

// ChannelRepository manages channel persistence.
type ChannelRepository interface {
	Create(ctx context.Context, ch *entity.Channel) error
	GetByID(ctx context.Context, id uuid.UUID) (*entity.Channel, error)
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID, p pagination.Params) ([]entity.Channel, error)
	ListByUser(ctx context.Context, workspaceID, userID uuid.UUID) ([]entity.Channel, error)
	ListArchivedByUser(ctx context.Context, workspaceID, userID uuid.UUID) ([]entity.ArchivedChannelInfo, error)
	Update(ctx context.Context, ch *entity.Channel) error
	Archive(ctx context.Context, id uuid.UUID) error

	AddMember(ctx context.Context, m *entity.ChannelMember) error
	GetMember(ctx context.Context, channelID, userID uuid.UUID) (*entity.ChannelMember, error)
	ListMembers(ctx context.Context, channelID uuid.UUID) ([]entity.ChannelMember, error)
	RemoveMember(ctx context.Context, channelID, userID uuid.UUID) error
	UpdateLastRead(ctx context.Context, channelID, userID uuid.UUID) error

	// GetDMChannel finds an existing DM channel between two users in a workspace.
	GetDMChannel(ctx context.Context, workspaceID, userA, userB uuid.UUID) (*entity.Channel, error)
}

// MessageRepository manages message persistence.
type MessageRepository interface {
	Create(ctx context.Context, msg *entity.Message) error
	GetByID(ctx context.Context, id uuid.UUID) (*entity.Message, error)
	ListByChannel(ctx context.Context, channelID uuid.UUID, p pagination.Params) ([]entity.Message, error)
	ListThreadReplies(ctx context.Context, parentID uuid.UUID, p pagination.Params) ([]entity.Message, error)
	HasActiveMessage(ctx context.Context, channelID uuid.UUID) (bool, error)
	Update(ctx context.Context, msg *entity.Message) error
	SoftDelete(ctx context.Context, id uuid.UUID) error
	SoftDeleteWithCascade(ctx context.Context, id uuid.UUID) ([]uuid.UUID, error)

	Pin(ctx context.Context, messageID, userID uuid.UUID) error
	Unpin(ctx context.Context, messageID uuid.UUID) error
	ListPinned(ctx context.Context, channelID uuid.UUID) ([]entity.Message, error)

	AddReaction(ctx context.Context, r *entity.Reaction) error
	GetReactionByID(ctx context.Context, id uuid.UUID) (*entity.Reaction, error)
	GetReactionByMessageUserEmoji(ctx context.Context, messageID, userID uuid.UUID, emoji string) (*entity.Reaction, error)
	RemoveReaction(ctx context.Context, messageID, userID uuid.UUID, emoji string) error
	RemoveReactionByID(ctx context.Context, id uuid.UUID) error
	ListReactions(ctx context.Context, messageID uuid.UUID) ([]entity.Reaction, error)
	ListReactionsByMessageIDs(ctx context.Context, messageIDs []uuid.UUID) (map[uuid.UUID][]entity.Reaction, error)

	CreateAttachment(ctx context.Context, a *entity.Attachment) error
	DeleteAttachment(ctx context.Context, id uuid.UUID) error
	GetAttachmentByStoragePath(ctx context.Context, storagePath string) (*entity.Attachment, error)
	ListAttachments(ctx context.Context, messageID uuid.UUID) ([]entity.Attachment, error)

	// CountUnread returns the number of messages in a channel created after
	// the given timestamp, excluding messages from the specified user.
	CountUnread(ctx context.Context, channelID, userID uuid.UUID, since time.Time) (int, error)

	// BatchUnreadCounts returns unread summaries for every channel the user
	// is a member of in the workspace, in a single query.
	BatchUnreadCounts(ctx context.Context, workspaceID, userID uuid.UUID) ([]UnreadSummary, error)

	// CountThreadReplies returns the number of non-deleted replies to a parent message.
	CountThreadReplies(ctx context.Context, parentID uuid.UUID) (int, error)
}

// UnreadSummary is a single channel's unread state.
type UnreadSummary struct {
	ChannelID  uuid.UUID
	LastReadAt time.Time
	Unread     int
}

// RecordingRepository manages call recording persistence.
type RecordingRepository interface {
	Create(ctx context.Context, rec *entity.Recording) error
	GetByID(ctx context.Context, id uuid.UUID) (*entity.Recording, error)
	GetByEgressID(ctx context.Context, egressID string) (*entity.Recording, error)
	GetActiveByCall(ctx context.Context, callID uuid.UUID) (*entity.Recording, error)
	SetEgressID(ctx context.Context, id uuid.UUID, egressID string) error
	ListByCall(ctx context.Context, callID uuid.UUID) ([]entity.Recording, error)
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID, p pagination.Params) ([]entity.Recording, error)
	ListByStatus(ctx context.Context, status entity.RecordingStatus, p pagination.Params) ([]entity.Recording, error)
	ListProcessable(ctx context.Context, now time.Time, p pagination.Params) ([]entity.Recording, error)
	ListExpired(ctx context.Context, now time.Time, p pagination.Params) ([]entity.Recording, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status entity.RecordingStatus) error
	SetReady(ctx context.Context, rec *entity.Recording) error
	MarkProcessingAttempt(ctx context.Context, id uuid.UUID, nextRetryAt time.Time) (*entity.Recording, error)
	MarkFailed(ctx context.Context, id uuid.UUID, lastError string, nextRetryAt *time.Time) error
	SetLegalHold(ctx context.Context, id uuid.UUID, hold bool) error
	Stop(ctx context.Context, id uuid.UUID) (*entity.Recording, error)
	Delete(ctx context.Context, id uuid.UUID) error
	ReplaceArtifacts(ctx context.Context, recordingID uuid.UUID, artifacts []entity.RecordingArtifact) error
	ListArtifacts(ctx context.Context, recordingID uuid.UUID) ([]entity.RecordingArtifact, error)
	GetArtifact(ctx context.Context, recordingID, artifactID uuid.UUID) (*entity.RecordingArtifact, error)
	WorkspaceStorageUsage(ctx context.Context, workspaceID uuid.UUID) (int64, error)
}

type MediaRepository interface {
	UpsertPlacement(ctx context.Context, placement *entity.MediaRoomPlacement) error
	GetPlacement(ctx context.Context, callID uuid.UUID) (*entity.MediaRoomPlacement, error)
	ListPlacementsByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]entity.MediaRoomPlacement, error)
	ReplaceRelayEdges(ctx context.Context, callID uuid.UUID, edges []entity.MediaRelayEdge) error
	ListRelayEdgesByCall(ctx context.Context, callID uuid.UUID) ([]entity.MediaRelayEdge, error)
	ListRelayEdgesByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]entity.MediaRelayEdge, error)
	AppendQoSSamples(ctx context.Context, samples []entity.MediaQoSSample) error
	ListQoSSamples(ctx context.Context, workspaceID, callID uuid.UUID, limit int) ([]entity.MediaQoSSample, error)
	SummarizeQoS(ctx context.Context, workspaceID, callID uuid.UUID) (*entity.MediaQoSSummary, error)
	UpsertQualityPolicy(ctx context.Context, policy *entity.MediaQualityPolicy) error
	GetQualityPolicy(ctx context.Context, workspaceID, callID uuid.UUID) (*entity.MediaQualityPolicy, error)
	UpsertQualityAlert(ctx context.Context, alert *entity.MediaQualityAlert) error
	ResolveQualityAlert(ctx context.Context, workspaceID, callID uuid.UUID, kind string) error
	ListQualityAlerts(ctx context.Context, workspaceID, callID uuid.UUID, limit int) ([]entity.MediaQualityAlert, error)
}

// BreakoutRoomRepository manages breakout room persistence.
type BreakoutRoomRepository interface {
	Create(ctx context.Context, room *entity.BreakoutRoom) error
	GetByID(ctx context.Context, id uuid.UUID) (*entity.BreakoutRoom, error)
	ListByCall(ctx context.Context, callID uuid.UUID) ([]entity.BreakoutRoom, error)
	Close(ctx context.Context, id uuid.UUID) error
	CloseAllByCall(ctx context.Context, callID uuid.UUID) error

	// AssignParticipant sets the breakout_room_id on a call participant.
	AssignParticipant(ctx context.Context, callID, userID, breakoutRoomID uuid.UUID) error
	// UnassignParticipant clears breakout_room_id (returns participant to main room).
	UnassignParticipant(ctx context.Context, callID, userID uuid.UUID) error
	// UnassignAllByRoom clears breakout_room_id for all participants in a room.
	UnassignAllByRoom(ctx context.Context, breakoutRoomID uuid.UUID) error
	// ListParticipants returns all connected participants in a breakout room.
	ListParticipants(ctx context.Context, breakoutRoomID uuid.UUID) ([]entity.CallParticipant, error)
}

// GuestInviteRepository manages guest invite persistence.
type GuestInviteRepository interface {
	Create(ctx context.Context, invite *entity.GuestInvite) error
	GetByToken(ctx context.Context, token string) (*entity.GuestInvite, error)
	GetByID(ctx context.Context, id uuid.UUID) (*entity.GuestInvite, error)
	IncrementUseCount(ctx context.Context, id uuid.UUID) error
	Revoke(ctx context.Context, id uuid.UUID) error
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]entity.GuestInvite, error)
}

type GuestAccessRepository interface {
	CreateGrant(ctx context.Context, grant *entity.GuestAccessGrant) error
	ListActiveByUserWorkspace(ctx context.Context, userID, workspaceID uuid.UUID, now time.Time) ([]entity.GuestAccessGrant, error)
}

// AuditRepository manages audit log persistence.
type AuditRepository interface {
	Create(ctx context.Context, entry *entity.AuditEntry) error
	List(ctx context.Context, workspaceID uuid.UUID, p pagination.Params) ([]entity.AuditEntry, int, error)
	ListByActor(ctx context.Context, workspaceID, actorID uuid.UUID, p pagination.Params) ([]entity.AuditEntry, error)
	ListByAction(ctx context.Context, workspaceID uuid.UUID, action entity.AuditAction, p pagination.Params) ([]entity.AuditEntry, error)
}

// CallRepository manages call persistence.
type CallRepository interface {
	Create(ctx context.Context, call *entity.Call) error
	GetByID(ctx context.Context, id uuid.UUID) (*entity.Call, error)
	ListActiveByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]entity.Call, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status entity.CallStatus) error
	UpdateSettings(ctx context.Context, id uuid.UUID, settings entity.CallSettings) error
	End(ctx context.Context, id uuid.UUID) error

	AddParticipant(ctx context.Context, p *entity.CallParticipant) error
	// AddParticipantIfCapacity atomically inserts a participant only if the
	// current active count is below maxParticipants. Returns cerrors.Forbidden
	// if the call is full.
	AddParticipantIfCapacity(ctx context.Context, p *entity.CallParticipant, maxParticipants int) error
	GetParticipant(ctx context.Context, callID, userID uuid.UUID) (*entity.CallParticipant, error)
	ListParticipants(ctx context.Context, callID uuid.UUID) ([]entity.CallParticipant, error)
	UpdateParticipantStatus(ctx context.Context, id uuid.UUID, status entity.ParticipantStatus) error
	UpdateParticipantRole(ctx context.Context, id uuid.UUID, role entity.CallRole) error
	// TransferHost atomically demotes the current host (fromUserID) to participant
	// and promotes toUserID to host in a single all-or-nothing statement that only
	// fires while fromUserID is still the host AND toUserID is still a participant
	// of the call. Returns true when the swap happened, false when it was a no-op
	// (host already changed / target gone). Enforces the single-host invariant
	// under concurrency. (ALK-696)
	TransferHost(ctx context.Context, callID, fromUserID, toUserID uuid.UUID) (bool, error)
	UpdateParticipantMedia(ctx context.Context, id uuid.UUID, audioMuted, videoMuted, screenSharing bool) error
	// SetCanScreenShare flips a participant's screen-share grant. Keyed by the
	// participant id (uuid) to match UpdateParticipantMedia. (ALK-697)
	SetCanScreenShare(ctx context.Context, id uuid.UUID, canShare bool) error
	// SetFeaturedShareUserID sets (or clears with nil) the host-featured share
	// pick on a call row. Keyed by callID since it's a calls-row update. (ALK-697)
	SetFeaturedShareUserID(ctx context.Context, callID uuid.UUID, userID *uuid.UUID) error
	RemoveParticipant(ctx context.Context, callID, userID uuid.UUID) error
}

type CallMessageRepository interface {
	Create(ctx context.Context, msg *entity.CallMessage) error
	ListByCall(ctx context.Context, callID uuid.UUID, p pagination.Params) ([]entity.CallMessage, error)
	SoftDelete(ctx context.Context, id, callID uuid.UUID) error
	GetByID(ctx context.Context, id uuid.UUID) (*entity.CallMessage, error)
}

type CalendarRepository interface {
	ListUserCalendars(ctx context.Context, workspaceID, ownerID uuid.UUID) ([]entity.UserCalendar, error)
	GetUserCalendar(ctx context.Context, workspaceID, calendarID, ownerID uuid.UUID) (*entity.UserCalendar, error)
	CreateUserCalendar(ctx context.Context, calendar *entity.UserCalendar) error
	UpdateUserCalendar(ctx context.Context, calendar *entity.UserCalendar) error
	DeleteUserCalendar(ctx context.Context, workspaceID, calendarID, ownerID uuid.UUID) error
	SetCalendarVisibility(ctx context.Context, workspaceID, calendarID, ownerID uuid.UUID, visible bool) (*entity.UserCalendar, error)
	EnsureDefaultCalendar(ctx context.Context, workspaceID, ownerID uuid.UUID) (*entity.UserCalendar, error)

	ListEvents(ctx context.Context, workspaceID uuid.UUID, fromTS, toTS time.Time, viewerID uuid.UUID) ([]entity.EventOccurrence, error)
	ListUpcoming(ctx context.Context, workspaceID, viewerID uuid.UUID, withinMinutes int) ([]entity.EventOccurrence, error)
	ListDueReminderTargets(ctx context.Context, now time.Time, horizon time.Duration, limit int) ([]entity.ReminderTarget, error)
	EnqueueReminderOutbox(ctx context.Context, target entity.ReminderTarget, payloadJSON []byte, enqueuedAt time.Time) error
	MarkReminderDispatched(ctx context.Context, reminderID uuid.UUID, occurrenceAt, dispatchedAt time.Time) error
	PublishReminderOutbox(ctx context.Context, limit, maxAttempts int, publish func(context.Context, entity.ReminderOutboxMessage) error) (processed, failed, dead int, err error)
	GetEvent(ctx context.Context, eventID uuid.UUID) (*entity.CalendarEvent, error)
	LockEventForStartCall(ctx context.Context, eventID uuid.UUID) (*entity.CalendarEvent, error)
	CreateEvent(ctx context.Context, event *entity.CalendarEvent) (*entity.CalendarEvent, error)
	UpdateEvent(ctx context.Context, event *entity.CalendarEvent) (*entity.CalendarEvent, error)
	// CreateEventTx inserts an event using the current connection. It must not open a nested transaction.
	CreateEventTx(ctx context.Context, event *entity.CalendarEvent) (*entity.CalendarEvent, error)
	// UpdateEventTx updates an event using the current connection. expectedUpdatedAt enables exact optimistic locking.
	UpdateEventTx(ctx context.Context, event *entity.CalendarEvent, expectedUpdatedAt *time.Time) (*entity.CalendarEvent, error)
	DeleteEvent(ctx context.Context, workspaceID, eventID uuid.UUID) error
	SetEventCallIDIfUnset(ctx context.Context, eventID, callID uuid.UUID) (*entity.CalendarEvent, error)
	LinkEventAndCall(ctx context.Context, eventID, callID uuid.UUID) error
	UpsertRsvp(ctx context.Context, eventID, userID uuid.UUID, status entity.RsvpStatus) (*entity.EventAttendee, error)
}
