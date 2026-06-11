package chat

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	eventpkg "aloqa/internal/domain/event"
	"aloqa/internal/domain/repository"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
	"aloqa/internal/platform/txscope"
	"aloqa/internal/security/accesspolicy"
	"aloqa/internal/security/collabaccess"
	"aloqa/internal/security/guestaccess"
	searchsvc "aloqa/internal/service/search"
)

func TestChannelAccessRequiresWorkspaceAndPrivateMembership(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	publicChannelID := uuid.New()
	privateChannelID := uuid.New()
	memberID := uuid.New()
	intruderID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			publicChannelID:  {ID: publicChannelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
			privateChannelID: {ID: privateChannelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePrivate},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, memberID}: {WorkspaceID: workspaceID, UserID: memberID, Role: entity.WorkspaceRoleMember},
	}}
	svc := NewService(channels, nil, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)

	if _, err := svc.GetChannel(ctx, publicChannelID, intruderID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("GetChannel public non-workspace member error = %v, want FORBIDDEN", err)
	}
	if _, err := svc.GetChannel(ctx, privateChannelID, memberID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("GetChannel private non-channel member error = %v, want FORBIDDEN", err)
	}

	channels.members[[2]uuid.UUID{privateChannelID, memberID}] = &entity.ChannelMember{ChannelID: privateChannelID, UserID: memberID}
	if _, err := svc.GetChannel(ctx, privateChannelID, memberID); err != nil {
		t.Fatalf("GetChannel private member returned error: %v", err)
	}
}

func TestGetOrCreateDMRequiresBothWorkspaceMembers(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userA := uuid.New()
	userB := uuid.New()

	channels := &fakeChannelRepo{}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userA}: {WorkspaceID: workspaceID, UserID: userA, Role: entity.WorkspaceRoleMember},
	}}
	svc := NewService(channels, nil, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)

	if _, err := svc.GetOrCreateDM(ctx, workspaceID, userA, userB, nil); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("GetOrCreateDM target outside workspace error = %v, want FORBIDDEN", err)
	}
	if len(channels.created) != 0 {
		t.Fatalf("created %d DM channels, want 0", len(channels.created))
	}
}

func TestCreateChannelAddsSelectedWorkspaceMembers(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	creatorID := uuid.New()
	memberID := uuid.New()

	channels := &fakeChannelRepo{}
	publisher := &recordingSubjectPublisher{}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, creatorID}: {WorkspaceID: workspaceID, UserID: creatorID, Role: entity.WorkspaceRoleOwner},
		{workspaceID, memberID}:  {WorkspaceID: workspaceID, UserID: memberID, Role: entity.WorkspaceRoleMember},
	}}
	svc := NewService(channels, nil, workspaces, nil, publisher, nil, nil, nil, nil)

	channel, err := svc.CreateChannel(
		ctx,
		workspaceID,
		creatorID,
		"design",
		"Design critiques",
		entity.ChannelTypePrivate,
		[]uuid.UUID{memberID},
	)
	if err != nil {
		t.Fatalf("CreateChannel returned error: %v", err)
	}

	if member := channels.members[[2]uuid.UUID{channel.ID, creatorID}]; member == nil || member.Role != entity.ChannelRoleOwner {
		t.Fatalf("creator channel membership missing or wrong: %+v", member)
	}
	if member := channels.members[[2]uuid.UUID{channel.ID, memberID}]; member == nil || member.Role != entity.ChannelRoleMember {
		t.Fatalf("selected channel membership missing or wrong: %+v", member)
	}
	workspaceSubject := "aloqa.ws." + workspaceID.String()
	if publisher.hasEvent(workspaceSubject, eventpkg.TypeChannelCreated) {
		t.Fatalf("private channel.created leaked to workspace subject; subjects=%v", publisher.subjects())
	}
	memberSubject := workspaceUserEventsSubject(workspaceID, memberID)
	if !publisher.hasEvent(memberSubject, eventpkg.TypeChannelCreated) {
		t.Fatalf("channel.created was not published to selected member subject; subjects=%v", publisher.subjects())
	}
	payload, ok := publisher.channelCreatedPayload(memberSubject)
	if !ok || payload.Channel == nil {
		t.Fatalf("channel.created payload missing on selected member subject")
	}
	if !hasUUID(payload.Channel.Members, creatorID) || !hasUUID(payload.Channel.Members, memberID) {
		t.Fatalf("channel.created members = %v, want creator and selected member", payload.Channel.Members)
	}
}

// TestUpdateChannelUnarchiveBypassPreservesNameAndTopic locks in the
// ALK-617 unarchive bypass contract: a `{archived:false}` request against a
// currently-archived channel must flip Archived without copying the
// (possibly empty / unvalidated) request name/topic onto the entity.
func TestUpdateChannelUnarchiveBypassPreservesNameAndTopic(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	channelID := uuid.New()
	originalTopic := "Q1 launches"

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {
				ID:          channelID,
				WorkspaceID: &workspaceID,
				Name:        "announcements-q1",
				Topic:       &originalTopic,
				Type:        entity.ChannelTypePublic,
				CreatedBy:   userID,
				Archived:    true,
			},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID, Role: entity.ChannelRoleOwner},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleOwner},
	}}
	publisher := &recordingSubjectPublisher{}
	svc := NewService(channels, nil, workspaces, nil, publisher, nil, nil, nil, nil)

	archived := false
	updated, err := svc.UpdateChannel(ctx, channelID, userID, "", "", &archived)
	if err != nil {
		t.Fatalf("UpdateChannel unarchive returned error: %v", err)
	}
	if updated.Archived {
		t.Fatalf("Archived flag was not cleared: %+v", updated)
	}
	if updated.Name != "announcements-q1" {
		t.Fatalf("Name overwritten by empty bypass request: got %q want %q", updated.Name, "announcements-q1")
	}
	if updated.Topic == nil || *updated.Topic != originalTopic {
		t.Fatalf("Topic overwritten by empty bypass request: got %v want %q", updated.Topic, originalTopic)
	}
}

// TestUpdateChannelRejectsArchivedNameEdit confirms the archived guard still
// rejects name/topic edits on an archived channel — the bypass only exists
// for the `archived=false` unarchive path.
func TestUpdateChannelRejectsArchivedNameEdit(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	channelID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {
				ID:          channelID,
				WorkspaceID: &workspaceID,
				Name:        "old-name",
				Type:        entity.ChannelTypePublic,
				CreatedBy:   userID,
				Archived:    true,
			},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID, Role: entity.ChannelRoleOwner},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleOwner},
	}}
	publisher := &recordingSubjectPublisher{}
	svc := NewService(channels, nil, workspaces, nil, publisher, nil, nil, nil, nil)

	if _, err := svc.UpdateChannel(ctx, channelID, userID, "new-name", "new-topic", nil); err == nil {
		t.Fatal("expected Forbidden when editing name/topic on archived channel")
	}
}

func TestUpdateChannelDeletesSearchDocumentWhenArchived(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	channelID := uuid.New()
	topic := "Launches"

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {
				ID:          channelID,
				WorkspaceID: &workspaceID,
				Name:        "announcements",
				Topic:       &topic,
				Type:        entity.ChannelTypePublic,
				CreatedBy:   userID,
			},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID, Role: entity.ChannelRoleOwner},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleOwner},
	}}
	search := &recordingSearchIndexer{}
	svc := NewService(channels, nil, workspaces, nil, noopPublisher{}, nil, nil, search, nil)

	archived := true
	if _, err := svc.UpdateChannel(ctx, channelID, userID, "announcements", topic, &archived); err != nil {
		t.Fatalf("UpdateChannel archive returned error: %v", err)
	}
	if len(search.indexedChannels) != 0 {
		t.Fatalf("indexed channels = %v, want none for archived channel", search.indexedChannels)
	}
	if len(search.deletedChannels) != 1 || search.deletedChannels[0] != channelID {
		t.Fatalf("deleted channels = %v, want [%s]", search.deletedChannels, channelID)
	}
}

func TestUpdateChannelDeletesSearchDocumentWhenArchivedInTransaction(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	channelID := uuid.New()
	topic := "Launches"

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {
				ID:          channelID,
				WorkspaceID: &workspaceID,
				Name:        "announcements",
				Topic:       &topic,
				Type:        entity.ChannelTypePublic,
				CreatedBy:   userID,
			},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID, Role: entity.ChannelRoleOwner},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleOwner},
	}}
	search := &recordingSearchQueue{}
	txScope := &fakeChatTxScope{channels: channels, search: search}
	txManager := &fakeChatTxManager{scope: txScope}
	svc := NewService(channels, nil, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)
	svc.SetTransactionManager(txManager)

	archived := true
	if _, err := svc.UpdateChannel(ctx, channelID, userID, "announcements", topic, &archived); err != nil {
		t.Fatalf("UpdateChannel archive returned error: %v", err)
	}
	if txManager.calls != 1 {
		t.Fatalf("transaction calls = %d, want 1", txManager.calls)
	}
	if len(search.upserts) != 0 {
		t.Fatalf("upserted docs = %v, want none for archived channel", search.upserts)
	}
	if len(search.deletes) != 1 {
		t.Fatalf("delete calls = %v, want one channel delete", search.deletes)
	}
	deleteCall := search.deletes[0]
	if deleteCall.workspaceID != workspaceID || deleteCall.resourceType != searchsvc.ResourceTypeChannel || deleteCall.resourceID != channelID {
		t.Fatalf("delete call = %+v, want workspace=%s type=%s resource=%s", deleteCall, workspaceID, searchsvc.ResourceTypeChannel, channelID)
	}
}

func TestSendMessageAllowsGlobalSavedChannelFromWorkspaceRoute(t *testing.T) {
	workspaceID := uuid.New()
	userID := uuid.New()
	channelID := uuid.New()
	ctx := context.WithValue(context.Background(), middleware.WorkspaceIDKey, workspaceID)

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {
				ID:          channelID,
				Type:        entity.ChannelTypeSavedGlobal,
				CreatedBy:   userID,
				OwnerUserID: &userID,
			},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID},
		},
	}
	messages := &fakeMessageRepo{}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	publisher := &recordingSubjectPublisher{}
	svc := NewService(channels, messages, workspaces, nil, publisher, nil, nil, nil, nil)
	svc.SetAccessPolicy(accesspolicy.NewChecker(workspaces, channels, nil, nil))

	msg, err := svc.SendMessage(ctx, channelID, userID, SendMessageInput{Content: "note to self"})
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
	if msg.ChannelID != channelID {
		t.Fatalf("message channel = %s, want %s", msg.ChannelID, channelID)
	}
	if !publisher.hasEvent("aloqa.ws."+workspaceID.String(), eventpkg.TypeMessageCreated) {
		t.Fatalf("message.created was not published to workspace subject; subjects=%v", publisher.subjects())
	}
}

func TestSendMessageFileIDsTransactionalShareFailurePreservesAppError(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	userID := uuid.New()
	fileID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID},
		},
	}
	messages := &fakeMessageRepo{messages: map[uuid.UUID]*entity.Message{}}
	files := &fakeChatFileRepo{shareErr: cerrors.Forbidden("cannot share file")}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	txManager := &fakeChatTxManager{scope: &fakeChatTxScope{
		messages: messages,
		files:    files,
		channels: channels,
	}}
	svc := NewService(channels, messages, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)
	svc.SetTransactionManager(txManager)

	_, err := svc.SendMessage(ctx, channelID, userID, SendMessageInput{FileIDs: []uuid.UUID{fileID}})
	if !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("SendMessage error = %v, want FORBIDDEN", err)
	}
	if len(messages.messages) != 0 {
		t.Fatalf("messages persisted = %d, want none after share failure", len(messages.messages))
	}
	if txManager.calls != 1 {
		t.Fatalf("transaction calls = %d, want 1", txManager.calls)
	}
	if len(files.shares) != 1 || files.shares[0].fileID != fileID {
		t.Fatalf("shares = %+v, want attempted share for %s", files.shares, fileID)
	}
}

func TestSendMessageResolvesMentionsForCreatedEvent(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	userID := uuid.New()
	mentionedID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{},
		resolveMentionsFunc: func(_ context.Context, gotChannelID, gotAuthorID uuid.UUID, content string) ([]uuid.UUID, error) {
			if gotChannelID != channelID {
				t.Fatalf("ResolveMentions channel = %s, want %s", gotChannelID, channelID)
			}
			if gotAuthorID != userID {
				t.Fatalf("ResolveMentions author = %s, want %s", gotAuthorID, userID)
			}
			if content != "Hello @alice" {
				t.Fatalf("ResolveMentions content = %q, want mention content", content)
			}
			return []uuid.UUID{mentionedID}, nil
		},
	}
	publisher := &recordingSubjectPublisher{}
	svc := NewService(channels, messages, workspaces, nil, publisher, nil, nil, nil, nil)

	msg, err := svc.SendMessage(ctx, channelID, userID, SendMessageInput{Content: "Hello @alice"})
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
	requireUUIDs(t, msg.Mentions, mentionedID)
	if len(messages.resolveMentionsCalls) != 1 {
		t.Fatalf("ResolveMentions calls = %d, want 1", len(messages.resolveMentionsCalls))
	}
	if len(publisher.events) != 2 {
		t.Fatalf("published events = %d, want channel + workspace message.created events", len(publisher.events))
	}

	var envelope struct {
		Type    eventpkg.Type `json:"type"`
		Payload struct {
			Message entity.Message `json:"message"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(publisher.events[0].data, &envelope); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if envelope.Type != eventpkg.TypeMessageCreated {
		t.Fatalf("event type = %s, want %s", envelope.Type, eventpkg.TypeMessageCreated)
	}
	requireUUIDs(t, envelope.Payload.Message.Mentions, mentionedID)
}

func TestEditMessageResolvesMentionsForUpdatedEvent(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	userID := uuid.New()
	messageID := uuid.New()
	mentionedID := uuid.New()
	now := time.Now().UTC()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			messageID: {
				ID:        messageID,
				ChannelID: channelID,
				UserID:    userID,
				Content:   "old content",
				Type:      entity.MessageTypeText,
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
		resolveMentionsFunc: func(_ context.Context, gotChannelID, gotAuthorID uuid.UUID, content string) ([]uuid.UUID, error) {
			if gotChannelID != channelID || gotAuthorID != userID || content != "Now hello @alice" {
				t.Fatalf("ResolveMentions args = %s/%s/%q, want %s/%s/%q", gotChannelID, gotAuthorID, content, channelID, userID, "Now hello @alice")
			}
			return []uuid.UUID{mentionedID}, nil
		},
	}
	publisher := &recordingSubjectPublisher{}
	svc := NewService(channels, messages, workspaces, nil, publisher, nil, nil, nil, nil)

	msg, err := svc.EditMessage(ctx, messageID, userID, "Now hello @alice")
	if err != nil {
		t.Fatalf("EditMessage returned error: %v", err)
	}
	requireUUIDs(t, msg.Mentions, mentionedID)
	if len(messages.resolveMentionsCalls) != 1 {
		t.Fatalf("ResolveMentions calls = %d, want 1", len(messages.resolveMentionsCalls))
	}
	if len(publisher.events) != 2 {
		t.Fatalf("published events = %d, want channel + workspace message.updated events", len(publisher.events))
	}

	var envelope struct {
		Type    eventpkg.Type `json:"type"`
		Payload struct {
			Message entity.Message `json:"message"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(publisher.events[0].data, &envelope); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if envelope.Type != eventpkg.TypeMessageUpdated {
		t.Fatalf("event type = %s, want %s", envelope.Type, eventpkg.TypeMessageUpdated)
	}
	requireUUIDs(t, envelope.Payload.Message.Mentions, mentionedID)
}

func TestEditMessageEmitsEmptyMentionsWhenMentionRemoved(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	userID := uuid.New()
	messageID := uuid.New()
	oldMentionedID := uuid.New()
	now := time.Now().UTC()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			messageID: {
				ID:        messageID,
				ChannelID: channelID,
				UserID:    userID,
				Content:   "old @alice",
				Type:      entity.MessageTypeText,
				Mentions:  []uuid.UUID{oldMentionedID},
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
	}
	publisher := &recordingSubjectPublisher{}
	svc := NewService(channels, messages, workspaces, nil, publisher, nil, nil, nil, nil)

	msg, err := svc.EditMessage(ctx, messageID, userID, "mention removed")
	if err != nil {
		t.Fatalf("EditMessage returned error: %v", err)
	}
	if len(msg.Mentions) != 0 {
		t.Fatalf("message mentions = %v, want empty slice", msg.Mentions)
	}
	if len(messages.resolveMentionsCalls) != 0 {
		t.Fatalf("ResolveMentions calls = %d, want 0 for content without @", len(messages.resolveMentionsCalls))
	}
	if len(publisher.events) == 0 {
		t.Fatalf("expected message.updated event")
	}
	if !strings.Contains(string(publisher.events[0].data), `"mentions":[]`) {
		t.Fatalf("message.updated payload = %s, want explicit empty mentions", publisher.events[0].data)
	}
}

func TestMoveMessageMovesAcrossAccessibleChannels(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	sourceChannelID := uuid.New()
	targetChannelID := uuid.New()
	messageID := uuid.New()
	now := time.Now().UTC()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			sourceChannelID: {ID: sourceChannelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
			targetChannelID: {ID: targetChannelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePrivate},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{sourceChannelID, userID}: {ChannelID: sourceChannelID, UserID: userID},
			{targetChannelID, userID}: {ChannelID: targetChannelID, UserID: userID},
		},
	}
	messages := &fakeMessageRepo{messages: map[uuid.UUID]*entity.Message{
		messageID: {
			ID:        messageID,
			ChannelID: sourceChannelID,
			UserID:    userID,
			Content:   "move me",
			Type:      entity.MessageTypeText,
			CreatedAt: now,
			UpdatedAt: now,
			Pinned:    true,
			PinnedBy:  &userID,
			PinnedAt:  &now,
		},
	}}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	publisher := &recordingSubjectPublisher{}
	svc := NewService(channels, messages, workspaces, nil, publisher, nil, nil, nil, nil)

	moved, err := svc.MoveMessage(ctx, sourceChannelID, messageID, userID, targetChannelID, nil)
	if err != nil {
		t.Fatalf("MoveMessage returned error: %v", err)
	}
	if moved.ChannelID != targetChannelID {
		t.Fatalf("moved channel = %s, want %s", moved.ChannelID, targetChannelID)
	}
	if moved.Pinned || moved.PinnedBy != nil || moved.PinnedAt != nil {
		t.Fatalf("moved pinned state = pinned:%v by:%v at:%v, want cleared", moved.Pinned, moved.PinnedBy, moved.PinnedAt)
	}
	if !publisher.hasEvent("aloqa.chat."+sourceChannelID.String(), eventpkg.TypeMessageDeleted) {
		t.Fatalf("message.deleted was not published to source channel; subjects=%v", publisher.subjects())
	}
	if !publisher.hasEvent("aloqa.chat."+targetChannelID.String(), eventpkg.TypeMessageCreated) {
		t.Fatalf("message.created was not published to target channel; subjects=%v", publisher.subjects())
	}
}

func TestCreateChannelRejectsSelectedNonWorkspaceMember(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	creatorID := uuid.New()
	intruderID := uuid.New()

	channels := &fakeChannelRepo{}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, creatorID}: {WorkspaceID: workspaceID, UserID: creatorID, Role: entity.WorkspaceRoleOwner},
	}}
	svc := NewService(channels, nil, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)

	if _, err := svc.CreateChannel(
		ctx,
		workspaceID,
		creatorID,
		"design",
		"",
		entity.ChannelTypePrivate,
		[]uuid.UUID{intruderID},
	); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("CreateChannel non-workspace selected member error = %v, want FORBIDDEN", err)
	}
	if len(channels.created) != 0 {
		t.Fatalf("created %d channels, want 0", len(channels.created))
	}
}

func TestGuestGrantAllowsChannelAccessWithoutWorkspaceMembership(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	guestID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePrivate},
		},
	}
	guests := guestaccess.NewChecker(&fakeGuestAccessRepo{grants: []entity.GuestAccessGrant{{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		UserID:      guestID,
		ChannelIDs:  []uuid.UUID{channelID},
		ExpiresAt:   time.Now().Add(time.Hour),
	}}})
	svc := NewService(channels, nil, &fakeWorkspaceRepo{}, nil, noopPublisher{}, guests, nil, nil, nil)

	if _, err := svc.GetChannel(ctx, channelID, guestID); err != nil {
		t.Fatalf("GetChannel guest returned error: %v", err)
	}
}

func TestGetOrCreateDMCreatesCrossWorkspaceGrantWhenCollaborationAllows(t *testing.T) {
	ctx := context.Background()
	workspaceA := uuid.New()
	workspaceB := uuid.New()
	userA := uuid.New()
	userB := uuid.New()

	channels := &fakeChannelRepo{members: map[[2]uuid.UUID]*entity.ChannelMember{}}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceA, userA}: {WorkspaceID: workspaceA, UserID: userA, Role: entity.WorkspaceRoleMember},
		{workspaceB, userB}: {WorkspaceID: workspaceB, UserID: userB, Role: entity.WorkspaceRoleMember},
	}}
	grants := &fakeChannelGrantRepo{}
	svc := NewService(channels, nil, workspaces, grants, noopPublisher{}, nil, nil, nil, fakeContactAuthorizer{err: nil})

	channel, err := svc.GetOrCreateDM(ctx, workspaceA, userA, userB, &workspaceB)
	if err != nil {
		t.Fatalf("GetOrCreateDM returned error: %v", err)
	}
	if channel == nil || channel.WorkspaceID == nil || *channel.WorkspaceID != workspaceA {
		t.Fatalf("expected cross-workspace DM anchored in source workspace")
	}
	if len(grants.created) != 1 {
		t.Fatalf("created %d grants, want 1", len(grants.created))
	}
	if grants.created[0].UserID != userB || grants.created[0].RemoteWorkspaceID != workspaceB {
		t.Fatalf("unexpected grant %+v", grants.created[0])
	}
}

func TestCrossWorkspaceDMAccessRequiresActiveCollaborationGrant(t *testing.T) {
	ctx := context.Background()
	workspaceA := uuid.New()
	channelID := uuid.New()
	userA := uuid.New()
	userB := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceA, Type: entity.ChannelTypeDM},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userA}: {ChannelID: channelID, UserID: userA},
			{channelID, userB}: {ChannelID: channelID, UserID: userB},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceA, userA}: {WorkspaceID: workspaceA, UserID: userA, Role: entity.WorkspaceRoleMember},
	}}
	svc := NewService(channels, nil, workspaces, nil, noopPublisher{}, nil, fakeCollabChecker{
		decision: collabaccess.Decision{Managed: true, Allowed: false},
	}, nil, nil)

	if _, err := svc.GetChannel(ctx, channelID, userB); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("GetChannel remote user error = %v, want FORBIDDEN", err)
	}
}

func TestGuestCanSendAndTrackUnreadWithSharedAccessPolicy(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	guestID := uuid.New()
	ownerID := uuid.New()
	messageID := uuid.New()
	now := time.Now().UTC()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePrivate},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, ownerID}: {ChannelID: channelID, UserID: ownerID, Role: entity.ChannelRoleMember, LastReadAt: now.Add(-time.Hour)},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, ownerID}: {WorkspaceID: workspaceID, UserID: ownerID, Role: entity.WorkspaceRoleMember},
	}}
	guests := guestaccess.NewChecker(&fakeGuestAccessRepo{grants: []entity.GuestAccessGrant{{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		UserID:      guestID,
		ChannelIDs:  []uuid.UUID{channelID},
		ExpiresAt:   now.Add(time.Hour),
	}}})
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			messageID: {ID: messageID, ChannelID: channelID, UserID: ownerID, Content: "welcome", CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)},
		},
	}
	readStates := &fakeChannelAccessStateRepo{}

	svc := NewService(channels, messages, workspaces, nil, noopPublisher{}, guests, nil, nil, nil)
	svc.SetAccessPolicy(accesspolicy.NewChecker(workspaces, channels, guests, nil))
	svc.SetChannelAccessStates(readStates)

	if _, err := svc.SendMessage(ctx, channelID, guestID, SendMessageInput{Content: "hi team"}); err != nil {
		t.Fatalf("SendMessage guest returned error: %v", err)
	}

	counts, err := svc.GetUnreadCounts(ctx, workspaceID, guestID)
	if err != nil {
		t.Fatalf("GetUnreadCounts returned error: %v", err)
	}
	if len(counts) != 1 || counts[0].UnreadCount != 1 {
		t.Fatalf("counts = %+v, want one channel with unread=1", counts)
	}

	if err := svc.MarkRead(ctx, channelID, guestID); err != nil {
		t.Fatalf("MarkRead returned error: %v", err)
	}
	counts, err = svc.GetUnreadCounts(ctx, workspaceID, guestID)
	if err != nil {
		t.Fatalf("GetUnreadCounts after mark read returned error: %v", err)
	}
	if len(counts) != 0 {
		t.Fatalf("counts after mark read = %+v, want empty", counts)
	}
}

func TestMarkReadPublishesChannelReadEvent(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	userID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	publisher := &recordingPublisher{}
	svc := NewService(channels, &fakeMessageRepo{}, workspaces, nil, publisher, nil, nil, nil, nil)
	svc.SetAccessPolicy(accesspolicy.NewChecker(workspaces, channels, nil, nil))

	if err := svc.MarkRead(ctx, channelID, userID); err != nil {
		t.Fatalf("MarkRead returned error: %v", err)
	}

	// MarkRead broadcasts a single channel.read event so other clients update
	// seen indicators in realtime (ALK-111).
	if len(publisher.events) != 1 {
		t.Fatalf("published events = %d, want 1 channel.read", len(publisher.events))
	}
	var envelope struct {
		Type    eventpkg.Type               `json:"type"`
		Payload eventpkg.ChannelReadPayload `json:"payload"`
	}
	if err := json.Unmarshal(publisher.events[0], &envelope); err != nil {
		t.Fatalf("unmarshal channel.read envelope: %v", err)
	}
	if envelope.Type != eventpkg.TypeChannelRead {
		t.Fatalf("event type = %s, want channel.read", envelope.Type)
	}
	if envelope.Payload.ChannelID != channelID || envelope.Payload.UserID != userID {
		t.Fatalf("payload = %+v, want channel %s / user %s", envelope.Payload, channelID, userID)
	}
	if envelope.Payload.LastReadAt.IsZero() {
		t.Fatalf("payload last_read_at is zero, want a timestamp")
	}
}

func TestCollaboratorCanSendWithSharedAccessPolicy(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	localUserID := uuid.New()
	remoteUserID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypeDM},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, localUserID}:  {ChannelID: channelID, UserID: localUserID, Role: entity.ChannelRoleMember},
			{channelID, remoteUserID}: {ChannelID: channelID, UserID: remoteUserID, Role: entity.ChannelRoleMember},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, localUserID}: {WorkspaceID: workspaceID, UserID: localUserID, Role: entity.WorkspaceRoleMember},
	}}
	messages := &fakeMessageRepo{messages: map[uuid.UUID]*entity.Message{}}

	svc := NewService(channels, messages, workspaces, nil, noopPublisher{}, nil, fakeCollabChecker{
		decision: collabaccess.Decision{Managed: true, Allowed: true},
	}, nil, nil)
	svc.SetAccessPolicy(accesspolicy.NewChecker(workspaces, channels, nil, fakeCollabChecker{
		decision: collabaccess.Decision{Managed: true, Allowed: true},
	}))

	msg, err := svc.SendMessage(ctx, channelID, remoteUserID, SendMessageInput{Content: "from remote"})
	if err != nil {
		t.Fatalf("SendMessage collaborator returned error: %v", err)
	}
	if msg.UserID != remoteUserID {
		t.Fatalf("message user = %s, want %s", msg.UserID, remoteUserID)
	}
}

func TestSendMessageForwardedFromValidationAndPersistence(t *testing.T) {
	quotedMessageID := uuid.New()
	quotedUserID := uuid.New()
	quotedParentID := uuid.New()
	quotedCreatedAt := time.Now().UTC()
	tests := []struct {
		name                string
		content             string
		forwardedFrom       json.RawMessage
		quotedMessageID     *uuid.UUID
		quotedSnapshot      *ParsedQuotedSnapshotInput
		wantErrCode         cerrors.Code
		wantErrMessage      string
		wantForwardedFrom   json.RawMessage
		wantQuotedSnapshot  bool
		wantCreatedMessages int
	}{
		{
			name:                "content only stores null forwarded_from",
			content:             "hi",
			wantCreatedMessages: 1,
		},
		{
			name:                "forward with comment stores forwarded_from",
			content:             "comment",
			forwardedFrom:       json.RawMessage(`{"message_id":"m1","snapshot":{"content":"original"}}`),
			wantForwardedFrom:   json.RawMessage(`{"message_id":"m1","snapshot":{"content":"original"}}`),
			wantCreatedMessages: 1,
		},
		{
			name:                "commentless forward is accepted",
			content:             "",
			forwardedFrom:       json.RawMessage(`{"message_id":"m1"}`),
			wantForwardedFrom:   json.RawMessage(`{"message_id":"m1"}`),
			wantCreatedMessages: 1,
		},
		{
			name:           "empty content without forward rejected",
			content:        "",
			wantErrCode:    cerrors.CodeInvalidInput,
			wantErrMessage: "content is required",
		},
		{
			// Share flow: source message becomes a quoted_snapshot and the
			// author may publish without their own comment text.
			name:            "empty content with quoted_snapshot is accepted",
			content:         "",
			quotedMessageID: &quotedMessageID,
			quotedSnapshot: &ParsedQuotedSnapshotInput{
				UserID:          uuid.New(),
				ContentExcerpt:  "Original",
				CreatedAt:       quotedCreatedAt,
				ParentMessageID: &quotedParentID,
			},
			wantQuotedSnapshot:  true,
			wantCreatedMessages: 1,
		},
		{
			name:           "oversize content rejected",
			content:        strings.Repeat("a", 40001),
			wantErrCode:    cerrors.CodeInvalidInput,
			wantErrMessage: "content must be at most 40000 characters",
		},
		{
			name:           "invalid forwarded_from rejected",
			content:        "comment",
			forwardedFrom:  json.RawMessage(`not json`),
			wantErrCode:    cerrors.CodeInvalidInput,
			wantErrMessage: "forwarded_from must be valid JSON",
		},
		{
			name:                "json scalar forwarded_from is accepted",
			content:             "comment",
			forwardedFrom:       json.RawMessage(`"a string"`),
			wantForwardedFrom:   json.RawMessage(`"a string"`),
			wantCreatedMessages: 1,
		},
		{
			name:                "empty object forwarded_from is accepted",
			content:             "comment",
			forwardedFrom:       json.RawMessage(`{}`),
			wantForwardedFrom:   json.RawMessage(`{}`),
			wantCreatedMessages: 1,
		},
		{
			name:            "quote fields persist typed snapshot",
			content:         "reply",
			quotedMessageID: &quotedMessageID,
			quotedSnapshot: &ParsedQuotedSnapshotInput{
				UserID:          quotedUserID,
				ContentExcerpt:  "quoted text",
				CreatedAt:       quotedCreatedAt,
				ParentMessageID: &quotedParentID,
			},
			wantQuotedSnapshot:  true,
			wantCreatedMessages: 1,
		},
		{
			name:            "quoted_message_id without snapshot rejected",
			content:         "reply",
			quotedMessageID: &quotedMessageID,
			wantErrCode:     cerrors.CodeInvalidInput,
			wantErrMessage:  "quoted_message_id and quoted_snapshot must be set together",
		},
		{
			name:    "quoted_snapshot without message id rejected",
			content: "reply",
			quotedSnapshot: &ParsedQuotedSnapshotInput{
				UserID:         quotedUserID,
				ContentExcerpt: "quoted text",
				CreatedAt:      quotedCreatedAt,
			},
			wantErrCode:    cerrors.CodeInvalidInput,
			wantErrMessage: "quoted_message_id and quoted_snapshot must be set together",
		},
		{
			name:            "quoted excerpt exactly 200 codepoints accepted",
			content:         "reply",
			quotedMessageID: &quotedMessageID,
			quotedSnapshot: &ParsedQuotedSnapshotInput{
				UserID:         quotedUserID,
				ContentExcerpt: strings.Repeat("a", 200),
				CreatedAt:      quotedCreatedAt,
			},
			wantQuotedSnapshot:  true,
			wantCreatedMessages: 1,
		},
		{
			name:            "quoted excerpt over 200 codepoints rejected",
			content:         "reply",
			quotedMessageID: &quotedMessageID,
			quotedSnapshot: &ParsedQuotedSnapshotInput{
				UserID:         quotedUserID,
				ContentExcerpt: strings.Repeat("a", 201),
				CreatedAt:      quotedCreatedAt,
			},
			wantErrCode:    cerrors.CodeInvalidInput,
			wantErrMessage: "quoted_snapshot.content_excerpt must be at most 200 characters",
		},
		{
			name:            "quoted multibyte excerpt at rune limit accepted",
			content:         "reply",
			quotedMessageID: &quotedMessageID,
			quotedSnapshot: &ParsedQuotedSnapshotInput{
				UserID:         quotedUserID,
				ContentExcerpt: strings.Repeat("я", 200),
				CreatedAt:      quotedCreatedAt,
			},
			wantQuotedSnapshot:  true,
			wantCreatedMessages: 1,
		},
		{
			name:            "quoted multibyte excerpt over rune limit rejected",
			content:         "reply",
			quotedMessageID: &quotedMessageID,
			quotedSnapshot: &ParsedQuotedSnapshotInput{
				UserID:         quotedUserID,
				ContentExcerpt: strings.Repeat("я", 201),
				CreatedAt:      quotedCreatedAt,
			},
			wantErrCode:    cerrors.CodeInvalidInput,
			wantErrMessage: "quoted_snapshot.content_excerpt must be at most 200 characters",
		},
		{
			name:                "multibyte content at rune limit accepted",
			content:             strings.Repeat("я", 40000),
			wantCreatedMessages: 1,
		},
		{
			name:           "multibyte content over rune limit rejected",
			content:        strings.Repeat("я", 40001),
			wantErrCode:    cerrors.CodeInvalidInput,
			wantErrMessage: "content must be at most 40000 characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			workspaceID := uuid.New()
			channelID := uuid.New()
			userID := uuid.New()
			channels := &fakeChannelRepo{
				channels: map[uuid.UUID]*entity.Channel{
					channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
				},
				members: map[[2]uuid.UUID]*entity.ChannelMember{
					{channelID, userID}: {ChannelID: channelID, UserID: userID},
				},
			}
			workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
				{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
			}}
			messages := &fakeMessageRepo{messages: map[uuid.UUID]*entity.Message{}}
			svc := NewService(channels, messages, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)

			var forwardedFrom *json.RawMessage
			if tt.forwardedFrom != nil {
				forwardedFrom = &tt.forwardedFrom
			}
			msg, err := svc.SendMessage(ctx, channelID, userID, SendMessageInput{
				Content:         tt.content,
				ForwardedFrom:   forwardedFrom,
				QuotedMessageID: tt.quotedMessageID,
				QuotedSnapshot:  tt.quotedSnapshot,
			})
			if tt.wantErrCode != "" {
				if !hasCode(err, tt.wantErrCode) {
					t.Fatalf("SendMessage error = %v, want code %s", err, tt.wantErrCode)
				}
				if err.Error() != string(tt.wantErrCode)+": "+tt.wantErrMessage {
					t.Fatalf("SendMessage error = %q, want message %q", err.Error(), tt.wantErrMessage)
				}
				if len(messages.messages) != 0 {
					t.Fatalf("created %d messages on invalid input, want 0", len(messages.messages))
				}
				return
			}
			if err != nil {
				t.Fatalf("SendMessage returned error: %v", err)
			}
			if len(messages.messages) != tt.wantCreatedMessages {
				t.Fatalf("created %d messages, want %d", len(messages.messages), tt.wantCreatedMessages)
			}
			if msg.Content != tt.content {
				t.Fatalf("content = %q, want %q", msg.Content, tt.content)
			}
			if string(msg.ForwardedFrom) != string(tt.wantForwardedFrom) {
				t.Fatalf("forwarded_from = %s, want %s", msg.ForwardedFrom, tt.wantForwardedFrom)
			}
			if !tt.wantQuotedSnapshot {
				if msg.QuotedMessageID != nil || msg.QuotedSnapshot != nil {
					t.Fatalf("quote fields = %s %+v, want nil", msg.QuotedMessageID, msg.QuotedSnapshot)
				}
				return
			}
			if msg.QuotedMessageID == nil || *msg.QuotedMessageID != *tt.quotedMessageID {
				t.Fatalf("quoted_message_id = %v, want %s", msg.QuotedMessageID, *tt.quotedMessageID)
			}
			if msg.QuotedSnapshot == nil {
				t.Fatalf("quoted_snapshot = nil, want value")
			}
			if msg.QuotedSnapshot.UserID != tt.quotedSnapshot.UserID ||
				msg.QuotedSnapshot.ContentExcerpt != tt.quotedSnapshot.ContentExcerpt ||
				!msg.QuotedSnapshot.CreatedAt.Equal(tt.quotedSnapshot.CreatedAt) {
				t.Fatalf("quoted_snapshot = %+v, want %+v", msg.QuotedSnapshot, tt.quotedSnapshot)
			}
			if tt.quotedSnapshot.ParentMessageID != nil {
				if msg.QuotedSnapshot.ParentMessageID == nil || *msg.QuotedSnapshot.ParentMessageID != *tt.quotedSnapshot.ParentMessageID {
					t.Fatalf("quoted_snapshot.parent_message_id = %v, want %s", msg.QuotedSnapshot.ParentMessageID, *tt.quotedSnapshot.ParentMessageID)
				}
			}
			if msg.QuotedSnapshot.Deleted != nil {
				t.Fatalf("quoted_snapshot.deleted = %v, want nil on send", *msg.QuotedSnapshot.Deleted)
			}
		})
	}
}

func TestSendMessageProfileShareBuildsSnapshotAndValidatesWorkspace(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	actorID := uuid.New()
	targetID := uuid.New()
	position := "Product Manager"
	department := "Product"

	tests := []struct {
		name           string
		channelType    entity.ChannelType
		targetMember   *entity.WorkspaceMember
		inputUserID    uuid.UUID
		wantErrCode    cerrors.Code
		wantErrMessage string
	}{
		{
			name:        "dm share persists authoritative profile snapshot",
			channelType: entity.ChannelTypeDM,
			targetMember: &entity.WorkspaceMember{
				WorkspaceID: workspaceID,
				UserID:      targetID,
				Role:        entity.WorkspaceRoleAdmin,
				User: &entity.User{
					ID:          targetID,
					DisplayName: "Madina Karimova",
					AvatarURL:   "https://cdn.test/madina.png",
					AvatarColor: "#0EA5E9",
					Position:    &position,
					Department:  &department,
					Status:      entity.UserStatusActive,
				},
			},
			inputUserID: targetID,
		},
		{
			name:        "public channel share persists authoritative profile snapshot",
			channelType: entity.ChannelTypePublic,
			targetMember: &entity.WorkspaceMember{
				WorkspaceID: workspaceID,
				UserID:      targetID,
				Role:        entity.WorkspaceRoleAdmin,
				User: &entity.User{
					ID:          targetID,
					DisplayName: "Madina Karimova",
					AvatarURL:   "https://cdn.test/madina.png",
					AvatarColor: "#0EA5E9",
					Position:    &position,
					Department:  &department,
					Status:      entity.UserStatusActive,
				},
			},
			inputUserID: targetID,
		},
		{
			name:        "private channel share persists authoritative profile snapshot",
			channelType: entity.ChannelTypePrivate,
			targetMember: &entity.WorkspaceMember{
				WorkspaceID: workspaceID,
				UserID:      targetID,
				Role:        entity.WorkspaceRoleAdmin,
				User: &entity.User{
					ID:          targetID,
					DisplayName: "Madina Karimova",
					AvatarURL:   "https://cdn.test/madina.png",
					AvatarColor: "#0EA5E9",
					Position:    &position,
					Department:  &department,
					Status:      entity.UserStatusActive,
				},
			},
			inputUserID: targetID,
		},
		{
			name:        "group dm share persists authoritative profile snapshot",
			channelType: entity.ChannelTypeGroupDM,
			targetMember: &entity.WorkspaceMember{
				WorkspaceID: workspaceID,
				UserID:      targetID,
				Role:        entity.WorkspaceRoleAdmin,
				User: &entity.User{
					ID:          targetID,
					DisplayName: "Madina Karimova",
					AvatarURL:   "https://cdn.test/madina.png",
					AvatarColor: "#0EA5E9",
					Position:    &position,
					Department:  &department,
					Status:      entity.UserStatusActive,
				},
			},
			inputUserID: targetID,
		},
		{
			name:           "target must be in workspace",
			channelType:    entity.ChannelTypeDM,
			inputUserID:    targetID,
			wantErrCode:    cerrors.CodeForbidden,
			wantErrMessage: "shared profile is not a member of this workspace",
		},
		{
			name:        "profile share is not allowed in saved channel",
			channelType: entity.ChannelTypeSaved,
			targetMember: &entity.WorkspaceMember{
				WorkspaceID: workspaceID,
				UserID:      targetID,
				Role:        entity.WorkspaceRoleMember,
				User: &entity.User{
					ID:          targetID,
					DisplayName: "Madina Karimova",
					Status:      entity.UserStatusActive,
				},
			},
			inputUserID:    targetID,
			wantErrCode:    cerrors.CodeForbidden,
			wantErrMessage: "profile shares cannot be sent to this channel",
		},
		{
			name:        "inactive profile cannot be shared",
			channelType: entity.ChannelTypeDM,
			targetMember: &entity.WorkspaceMember{
				WorkspaceID: workspaceID,
				UserID:      targetID,
				Role:        entity.WorkspaceRoleMember,
				User: &entity.User{
					ID:          targetID,
					DisplayName: "Madina Karimova",
					Status:      entity.UserStatusDeactivated,
				},
			},
			inputUserID:    targetID,
			wantErrCode:    cerrors.CodeForbidden,
			wantErrMessage: "shared profile is not active",
		},
		{
			name:           "user id is required",
			channelType:    entity.ChannelTypeDM,
			inputUserID:    uuid.Nil,
			wantErrCode:    cerrors.CodeInvalidInput,
			wantErrMessage: "profile_share.user_id is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channels := &fakeChannelRepo{
				channels: map[uuid.UUID]*entity.Channel{
					channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: tt.channelType},
				},
				members: map[[2]uuid.UUID]*entity.ChannelMember{
					{channelID, actorID}: {ChannelID: channelID, UserID: actorID},
				},
			}
			workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
				{workspaceID, actorID}: {WorkspaceID: workspaceID, UserID: actorID, Role: entity.WorkspaceRoleMember},
			}}
			if tt.targetMember != nil {
				workspaces.members[[2]uuid.UUID{workspaceID, tt.targetMember.UserID}] = tt.targetMember
			}
			messages := &fakeMessageRepo{messages: map[uuid.UUID]*entity.Message{}}
			svc := NewService(channels, messages, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)

			msg, err := svc.SendMessage(ctx, channelID, actorID, SendMessageInput{
				Content:      "",
				ProfileShare: &ProfileShareInput{UserID: tt.inputUserID},
			})
			if tt.wantErrCode != "" {
				if !hasCode(err, tt.wantErrCode) {
					t.Fatalf("SendMessage error = %v, want code %s", err, tt.wantErrCode)
				}
				if err.Error() != string(tt.wantErrCode)+": "+tt.wantErrMessage {
					t.Fatalf("SendMessage error = %q, want message %q", err.Error(), tt.wantErrMessage)
				}
				if len(messages.messages) != 0 {
					t.Fatalf("created %d messages on invalid profile share, want 0", len(messages.messages))
				}
				return
			}
			if err != nil {
				t.Fatalf("SendMessage returned error: %v", err)
			}
			if msg.ProfileShare == nil {
				t.Fatalf("profile_share = nil, want value")
			}
			if msg.ProfileShare.UserID != targetID || msg.ProfileShare.WorkspaceID != workspaceID {
				t.Fatalf("profile_share ids = %s/%s, want %s/%s", msg.ProfileShare.UserID, msg.ProfileShare.WorkspaceID, targetID, workspaceID)
			}
			snapshot := msg.ProfileShare.Snapshot
			if snapshot.DisplayName != "Madina Karimova" || snapshot.AvatarURL != "https://cdn.test/madina.png" || snapshot.AvatarColor != "#0EA5E9" || snapshot.Role != entity.WorkspaceRoleAdmin {
				t.Fatalf("profile_share snapshot = %+v, want target user snapshot", snapshot)
			}
			if snapshot.Position == nil || *snapshot.Position != position {
				t.Fatalf("profile_share position = %v, want %q", snapshot.Position, position)
			}
			if snapshot.Department == nil || *snapshot.Department != department {
				t.Fatalf("profile_share department = %v, want %q", snapshot.Department, department)
			}
		})
	}
}

func TestEditMessageRejectsEmptyContent(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	userID := uuid.New()
	messageID := uuid.New()
	now := time.Now().UTC()
	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	messages := &fakeMessageRepo{messages: map[uuid.UUID]*entity.Message{
		messageID: {ID: messageID, ChannelID: channelID, UserID: userID, Content: "hello", CreatedAt: now, UpdatedAt: now},
	}}
	svc := NewService(channels, messages, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)

	if _, err := svc.EditMessage(ctx, messageID, userID, ""); err == nil {
		t.Fatalf("EditMessage empty content returned nil error, want validation error")
	}
}

func TestGuestCanReactWithSharedAccessPolicy(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	ownerID := uuid.New()
	guestID := uuid.New()
	messageID := uuid.New()
	now := time.Now().UTC()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePrivate},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, ownerID}: {ChannelID: channelID, UserID: ownerID, Role: entity.ChannelRoleMember},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, ownerID}: {WorkspaceID: workspaceID, UserID: ownerID, Role: entity.WorkspaceRoleMember},
	}}
	guests := guestaccess.NewChecker(&fakeGuestAccessRepo{grants: []entity.GuestAccessGrant{{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		UserID:      guestID,
		ChannelIDs:  []uuid.UUID{channelID},
		ExpiresAt:   now.Add(time.Hour),
	}}})
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			messageID: {ID: messageID, ChannelID: channelID, UserID: ownerID, Content: "hello", CreatedAt: now, UpdatedAt: now},
		},
	}

	svc := NewService(channels, messages, workspaces, nil, noopPublisher{}, guests, nil, nil, nil)
	svc.SetAccessPolicy(accesspolicy.NewChecker(workspaces, channels, guests, nil))

	if _, err := svc.AddReaction(ctx, messageID, guestID, ":+1:"); err != nil {
		t.Fatalf("AddReaction guest returned error: %v", err)
	}
	if err := svc.RemoveReaction(ctx, messageID, guestID, ":+1:"); err != nil {
		t.Fatalf("RemoveReaction guest returned error: %v", err)
	}
}

func TestEditAndDeleteRequireParticipateAccess(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	userID := uuid.New()
	messageID := uuid.New()
	now := time.Now().UTC()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			messageID: {ID: messageID, ChannelID: channelID, UserID: userID, Content: "hello", CreatedAt: now, UpdatedAt: now},
		},
	}

	svc := NewService(channels, messages, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)
	svc.SetAccessPolicy(accesspolicy.NewChecker(workspaces, channels, nil, nil))

	if _, err := svc.EditMessage(ctx, messageID, userID, "edited"); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("EditMessage error = %v, want FORBIDDEN", err)
	}
	if err := svc.DeleteMessage(ctx, messageID, userID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("DeleteMessage error = %v, want FORBIDDEN", err)
	}
}

func TestGetMessagesReturnsDeletedTombstoneWithoutContent(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	userID := uuid.New()
	messageID := uuid.New()
	deletedAt := time.Now().UTC()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			messageID: {
				ID:        messageID,
				ChannelID: channelID,
				UserID:    userID,
				Content:   "secret deleted text",
				CreatedAt: deletedAt.Add(-time.Minute),
				UpdatedAt: deletedAt,
				DeletedAt: &deletedAt,
				Edited:    true,
				EditedAt:  &deletedAt,
				Pinned:    true,
				PinnedBy:  &userID,
				PinnedAt:  &deletedAt,
				ForwardedFrom: json.RawMessage(
					`{"message_id":"source","snapshot":{"content":"secret snapshot"}}`,
				),
			},
		},
	}
	svc := NewService(channels, messages, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)

	page, err := svc.GetMessages(ctx, channelID, userID, pagination.Params{Limit: 10})
	if err != nil {
		t.Fatalf("GetMessages returned error: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items len = %d, want 1", len(page.Items))
	}
	got := page.Items[0]
	if got.DeletedAt == nil {
		t.Fatalf("DeletedAt = nil, want tombstone timestamp")
	}
	if got.Content != "" {
		t.Fatalf("Content = %q, want redacted empty content", got.Content)
	}
	if got.ForwardedFrom != nil {
		t.Fatalf("ForwardedFrom = %s, want nil after deleted tombstone redaction", got.ForwardedFrom)
	}
	if got.Edited || got.EditedAt != nil || got.Pinned || got.PinnedBy != nil || got.PinnedAt != nil {
		t.Fatalf("deleted metadata was not redacted: %+v", got)
	}
}

func TestGetMessagesHydratesReactionsInSingleBatch(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	userID := uuid.New()
	messageID := uuid.New()
	secondMessageID := uuid.New()
	deletedMessageID := uuid.New()
	deletedAt := time.Now().UTC()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			messageID:        {ID: messageID, ChannelID: channelID, UserID: userID, Content: "one", CreatedAt: deletedAt.Add(-3 * time.Minute), UpdatedAt: deletedAt.Add(-3 * time.Minute)},
			secondMessageID:  {ID: secondMessageID, ChannelID: channelID, UserID: userID, Content: "two", CreatedAt: deletedAt.Add(-2 * time.Minute), UpdatedAt: deletedAt.Add(-2 * time.Minute)},
			deletedMessageID: {ID: deletedMessageID, ChannelID: channelID, UserID: userID, Content: "deleted", CreatedAt: deletedAt.Add(-time.Minute), UpdatedAt: deletedAt, DeletedAt: &deletedAt},
		},
		reactions: map[uuid.UUID]entity.Reaction{},
	}
	firstReaction := entity.Reaction{ID: uuid.New(), MessageID: messageID, UserID: userID, Emoji: "👍", CreatedAt: deletedAt.Add(-2 * time.Minute)}
	secondReaction := entity.Reaction{ID: uuid.New(), MessageID: secondMessageID, UserID: userID, Emoji: "🚀", CreatedAt: deletedAt.Add(-time.Minute)}
	deletedReaction := entity.Reaction{ID: uuid.New(), MessageID: deletedMessageID, UserID: userID, Emoji: "👀", CreatedAt: deletedAt}
	messages.reactions[firstReaction.ID] = firstReaction
	messages.reactions[secondReaction.ID] = secondReaction
	messages.reactions[deletedReaction.ID] = deletedReaction

	svc := NewService(channels, messages, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)

	page, err := svc.GetMessages(ctx, channelID, userID, pagination.Params{Limit: 10})
	if err != nil {
		t.Fatalf("GetMessages returned error: %v", err)
	}
	if messages.listReactionsCalls != 0 {
		t.Fatalf("ListReactions calls = %d, want 0", messages.listReactionsCalls)
	}
	if messages.listReactionsByMessageIDsCalls != 1 {
		t.Fatalf("ListReactionsByMessageIDs calls = %d, want 1", messages.listReactionsByMessageIDsCalls)
	}
	if len(messages.lastListReactionsByMessageIDsArg) != 2 {
		t.Fatalf("batch message IDs = %v, want only 2 non-deleted IDs", messages.lastListReactionsByMessageIDsArg)
	}
	batchedIDs := map[uuid.UUID]bool{}
	for _, id := range messages.lastListReactionsByMessageIDsArg {
		batchedIDs[id] = true
	}
	if !batchedIDs[messageID] || !batchedIDs[secondMessageID] || batchedIDs[deletedMessageID] {
		t.Fatalf("batch message IDs = %v, want active IDs only", messages.lastListReactionsByMessageIDsArg)
	}

	gotByID := map[uuid.UUID]entity.Message{}
	for _, msg := range page.Items {
		gotByID[msg.ID] = msg
	}
	if gotByID[messageID].Reactions[0].ID != firstReaction.ID {
		t.Fatalf("first message reactions = %+v, want %+v", gotByID[messageID].Reactions, firstReaction)
	}
	if gotByID[secondMessageID].Reactions[0].ID != secondReaction.ID {
		t.Fatalf("second message reactions = %+v, want %+v", gotByID[secondMessageID].Reactions, secondReaction)
	}
	if gotByID[deletedMessageID].Reactions != nil {
		t.Fatalf("deleted message reactions = %+v, want nil", gotByID[deletedMessageID].Reactions)
	}
}

func TestGetMessageHydratesMentions(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	userID := uuid.New()
	messageID := uuid.New()
	mentionedID := uuid.New()
	now := time.Now().UTC()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			messageID: {
				ID:        messageID,
				ChannelID: channelID,
				UserID:    userID,
				Content:   "hello @alice",
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
		mentionsByMessageID: map[uuid.UUID][]uuid.UUID{
			messageID: {mentionedID},
		},
	}
	svc := NewService(channels, messages, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)

	msg, err := svc.GetMessage(ctx, messageID, userID)
	if err != nil {
		t.Fatalf("GetMessage returned error: %v", err)
	}
	requireUUIDs(t, msg.Mentions, mentionedID)
	if messages.resolveMentionsByMessageIDsCalls != 1 {
		t.Fatalf("ResolveMentionsByMessageIDs calls = %d, want 1", messages.resolveMentionsByMessageIDsCalls)
	}
	requireUUIDs(t, messages.lastResolveMentionsByMessageIDsArg, messageID)
}

func TestGetMessagesHydratesMentionsInSingleBatch(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	userID := uuid.New()
	messageID := uuid.New()
	secondMessageID := uuid.New()
	noMentionMessageID := uuid.New()
	deletedMessageID := uuid.New()
	mentionedID := uuid.New()
	secondMentionedID := uuid.New()
	deletedMentionedID := uuid.New()
	deletedAt := time.Now().UTC()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			messageID:          {ID: messageID, ChannelID: channelID, UserID: userID, Content: "one @alice", CreatedAt: deletedAt.Add(-4 * time.Minute), UpdatedAt: deletedAt.Add(-4 * time.Minute)},
			secondMessageID:    {ID: secondMessageID, ChannelID: channelID, UserID: userID, Content: "two @bob", CreatedAt: deletedAt.Add(-3 * time.Minute), UpdatedAt: deletedAt.Add(-3 * time.Minute)},
			noMentionMessageID: {ID: noMentionMessageID, ChannelID: channelID, UserID: userID, Content: "plain text", CreatedAt: deletedAt.Add(-2 * time.Minute), UpdatedAt: deletedAt.Add(-2 * time.Minute)},
			deletedMessageID:   {ID: deletedMessageID, ChannelID: channelID, UserID: userID, Content: "deleted @chris", CreatedAt: deletedAt.Add(-time.Minute), UpdatedAt: deletedAt, DeletedAt: &deletedAt},
		},
		mentionsByMessageID: map[uuid.UUID][]uuid.UUID{
			messageID:        {mentionedID},
			secondMessageID:  {secondMentionedID},
			deletedMessageID: {deletedMentionedID},
		},
	}
	svc := NewService(channels, messages, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)

	page, err := svc.GetMessages(ctx, channelID, userID, pagination.Params{Limit: 10})
	if err != nil {
		t.Fatalf("GetMessages returned error: %v", err)
	}
	if messages.resolveMentionsByMessageIDsCalls != 1 {
		t.Fatalf("ResolveMentionsByMessageIDs calls = %d, want 1", messages.resolveMentionsByMessageIDsCalls)
	}
	batchedIDs := map[uuid.UUID]bool{}
	for _, id := range messages.lastResolveMentionsByMessageIDsArg {
		batchedIDs[id] = true
	}
	if len(batchedIDs) != 2 || !batchedIDs[messageID] || !batchedIDs[secondMessageID] {
		t.Fatalf("batch message IDs = %v, want only active messages with @", messages.lastResolveMentionsByMessageIDsArg)
	}

	gotByID := map[uuid.UUID]entity.Message{}
	for _, msg := range page.Items {
		gotByID[msg.ID] = msg
	}
	requireUUIDs(t, gotByID[messageID].Mentions, mentionedID)
	requireUUIDs(t, gotByID[secondMessageID].Mentions, secondMentionedID)
	if len(gotByID[noMentionMessageID].Mentions) != 0 {
		t.Fatalf("plain message mentions = %+v, want empty slice", gotByID[noMentionMessageID].Mentions)
	}
	if gotByID[deletedMessageID].Mentions != nil {
		t.Fatalf("deleted message mentions = %+v, want nil", gotByID[deletedMessageID].Mentions)
	}
}

func TestDeleteMessagePublishesRedactedTombstone(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	userID := uuid.New()
	messageID := uuid.New()
	now := time.Now().UTC()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			messageID: {
				ID:        messageID,
				ChannelID: channelID,
				UserID:    userID,
				Content:   "secret deleted text",
				CreatedAt: now,
				UpdatedAt: now,
				Edited:    true,
				EditedAt:  &now,
				Pinned:    true,
				PinnedBy:  &userID,
				PinnedAt:  &now,
				ForwardedFrom: json.RawMessage(
					`{"message_id":"source","snapshot":{"content":"secret snapshot"}}`,
				),
			},
		},
	}
	publisher := &recordingPublisher{}
	svc := NewService(channels, messages, workspaces, nil, publisher, nil, nil, nil, nil)

	if err := svc.DeleteMessage(ctx, messageID, userID); err != nil {
		t.Fatalf("DeleteMessage returned error: %v", err)
	}
	// Delete now fans out to BOTH the channel subject and the workspace subject
	// (ALK-654); both must carry the same redacted tombstone.
	if len(publisher.events) != 2 {
		t.Fatalf("published events = %d, want 2 (channel + workspace)", len(publisher.events))
	}

	for i, raw := range publisher.events {
		var envelope struct {
			Type    eventpkg.Type `json:"type"`
			Payload struct {
				Message entity.Message `json:"message"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("unmarshal event %d: %v", i, err)
		}
		if envelope.Type != eventpkg.TypeMessageDeleted {
			t.Fatalf("event[%d] type = %s, want %s", i, envelope.Type, eventpkg.TypeMessageDeleted)
		}
		got := envelope.Payload.Message
		if got.ID != messageID || got.DeletedAt == nil {
			t.Fatalf("event[%d] message = %+v, want deleted tombstone for %s", i, got, messageID)
		}
		if got.Content != "" {
			t.Fatalf("event[%d] content = %q, want redacted empty content", i, got.Content)
		}
		if got.ForwardedFrom != nil {
			t.Fatalf("event[%d] forwarded_from = %s, want nil after delete", i, got.ForwardedFrom)
		}
		if got.Edited || got.EditedAt != nil || got.Pinned || got.PinnedBy != nil || got.PinnedAt != nil {
			t.Fatalf("event[%d] deleted metadata was not redacted: %+v", i, got)
		}
	}
}

func TestDeleteMessageWithTxEnqueuesCascadeQuoteUpdatedEvents(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	otherWorkspaceID := uuid.New()
	channelID := uuid.New()
	otherChannelID := uuid.New()
	userID := uuid.New()
	messageID := uuid.New()
	quotedIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	now := time.Now().UTC()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID:      {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
			otherChannelID: {ID: otherChannelID, WorkspaceID: &otherWorkspaceID, Type: entity.ChannelTypePublic},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}: {ChannelID: channelID, UserID: userID},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	messages := &fakeMessageRepo{messages: map[uuid.UUID]*entity.Message{
		messageID: {
			ID:        messageID,
			ChannelID: channelID,
			UserID:    userID,
			Content:   "source",
			CreatedAt: now,
			UpdatedAt: now,
		},
	}}
	for i, id := range quotedIDs {
		msgChannelID := channelID
		if i == len(quotedIDs)-1 {
			msgChannelID = otherChannelID
		}
		messages.messages[id] = &entity.Message{
			ID:              id,
			ChannelID:       msgChannelID,
			UserID:          userID,
			Content:         "quote",
			QuotedMessageID: &messageID,
			QuotedSnapshot: &entity.QuotedSnapshot{
				UserID:         userID,
				ContentExcerpt: "source",
				CreatedAt:      now,
			},
			CreatedAt: now,
			UpdatedAt: now,
		}
	}

	txScope := &fakeChatTxScope{messages: messages, channels: channels}
	svc := NewService(channels, messages, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)
	svc.tx = &fakeChatTxManager{scope: txScope}

	if err := svc.DeleteMessage(ctx, messageID, userID); err != nil {
		t.Fatalf("DeleteMessage returned error: %v", err)
	}

	updated := map[uuid.UUID]eventpkg.Event{}
	for _, evt := range txScope.events {
		if evt.Type != eventpkg.TypeMessageUpdated {
			continue
		}
		payload, ok := evt.Payload.(eventpkg.MessagePayload)
		if !ok || payload.Message == nil {
			t.Fatalf("message.updated payload = %#v, want MessagePayload", evt.Payload)
		}
		updated[payload.Message.ID] = evt
	}
	if len(updated) != len(quotedIDs) {
		t.Fatalf("message.updated events = %d, want %d", len(updated), len(quotedIDs))
	}
	for _, id := range quotedIDs {
		evt, ok := updated[id]
		if !ok {
			t.Fatalf("missing message.updated for quoted row %s", id)
		}
		payload := evt.Payload.(eventpkg.MessagePayload)
		if payload.Message.QuotedSnapshot == nil || payload.Message.QuotedSnapshot.Deleted == nil || !*payload.Message.QuotedSnapshot.Deleted {
			t.Fatalf("quoted_snapshot.deleted for %s = %+v, want true", id, payload.Message.QuotedSnapshot)
		}
		wantWorkspaceID := workspaceID
		if payload.Message.ChannelID == otherChannelID {
			wantWorkspaceID = otherWorkspaceID
		}
		if evt.WorkspaceID != wantWorkspaceID {
			t.Fatalf("event workspace_id for %s = %s, want %s", id, evt.WorkspaceID, wantWorkspaceID)
		}
	}
}

func TestListChannelsHidesRecipientDMWithOnlyDeletedMessages(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	creatorID := uuid.New()
	recipientID := uuid.New()
	messageID := uuid.New()
	deletedAt := time.Now().UTC()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {
				ID:          channelID,
				WorkspaceID: &workspaceID,
				Type:        entity.ChannelTypeDM,
				CreatedBy:   creatorID,
			},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, creatorID}:   {ChannelID: channelID, UserID: creatorID},
			{channelID, recipientID}: {ChannelID: channelID, UserID: recipientID},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, recipientID}: {WorkspaceID: workspaceID, UserID: recipientID, Role: entity.WorkspaceRoleMember},
	}}
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			messageID: {
				ID:        messageID,
				ChannelID: channelID,
				UserID:    creatorID,
				Content:   "deleted only",
				CreatedAt: deletedAt,
				UpdatedAt: deletedAt,
				DeletedAt: &deletedAt,
			},
		},
	}
	svc := NewService(channels, messages, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)

	got, err := svc.ListChannels(ctx, workspaceID, recipientID)
	if err != nil {
		t.Fatalf("ListChannels returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("channels = %+v, want recipient-side deleted-only DM hidden", got)
	}
}

func TestListChannelsStampsLastActivityFromNonDeletedMessages(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	activeChannelID := uuid.New()
	emptyChannelID := uuid.New()
	oldMessageID := uuid.New()
	latestMessageID := uuid.New()
	deletedMessageID := uuid.New()
	olderAt := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	latestAt := time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC)
	deletedAt := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			activeChannelID: {
				ID:          activeChannelID,
				WorkspaceID: &workspaceID,
				Type:        entity.ChannelTypePublic,
				Name:        "active",
			},
			emptyChannelID: {
				ID:          emptyChannelID,
				WorkspaceID: &workspaceID,
				Type:        entity.ChannelTypePublic,
				Name:        "empty",
			},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{activeChannelID, userID}: {ChannelID: activeChannelID, UserID: userID},
			{emptyChannelID, userID}:  {ChannelID: emptyChannelID, UserID: userID},
		},
	}
	messages := &fakeMessageRepo{
		messages: map[uuid.UUID]*entity.Message{
			oldMessageID: {
				ID:        oldMessageID,
				ChannelID: activeChannelID,
				UserID:    userID,
				Content:   "old",
				CreatedAt: olderAt,
				UpdatedAt: olderAt,
			},
			latestMessageID: {
				ID:        latestMessageID,
				ChannelID: activeChannelID,
				UserID:    userID,
				Content:   "latest",
				CreatedAt: latestAt,
				UpdatedAt: latestAt,
			},
			deletedMessageID: {
				ID:        deletedMessageID,
				ChannelID: activeChannelID,
				UserID:    userID,
				Content:   "",
				CreatedAt: deletedAt,
				UpdatedAt: deletedAt,
				DeletedAt: &deletedAt,
			},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	svc := NewService(channels, messages, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)

	got, err := svc.ListChannels(ctx, workspaceID, userID)
	if err != nil {
		t.Fatalf("ListChannels returned error: %v", err)
	}

	byID := make(map[uuid.UUID]entity.Channel, len(got))
	for _, channel := range got {
		byID[channel.ID] = channel
	}
	active := byID[activeChannelID]
	if active.LastActivityAt == nil || !active.LastActivityAt.Equal(latestAt) {
		t.Fatalf("active LastActivityAt = %v, want %v", active.LastActivityAt, latestAt)
	}
	if empty := byID[emptyChannelID]; empty.LastActivityAt != nil {
		t.Fatalf("empty LastActivityAt = %v, want nil", empty.LastActivityAt)
	}
}

func TestListChannelsUsesMembershipForWorkspaceMembersWithAccessPolicy(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	joinedPublicID := uuid.New()
	unjoinedPublicID := uuid.New()
	joinedPrivateID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			joinedPublicID:   {ID: joinedPublicID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic, Name: "joined"},
			unjoinedPublicID: {ID: unjoinedPublicID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic, Name: "unjoined"},
			joinedPrivateID:  {ID: joinedPrivateID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePrivate, Name: "private"},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{joinedPublicID, userID}:  {ChannelID: joinedPublicID, UserID: userID},
			{joinedPrivateID, userID}: {ChannelID: joinedPrivateID, UserID: userID},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	svc := NewService(channels, nil, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)
	svc.SetAccessPolicy(accesspolicy.NewChecker(workspaces, channels, nil, nil))

	got, err := svc.ListChannels(ctx, workspaceID, userID)
	if err != nil {
		t.Fatalf("ListChannels returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("channels length = %d, want 2: %+v", len(got), got)
	}
	for _, ch := range got {
		if ch.ID == unjoinedPublicID {
			t.Fatalf("ListChannels returned unjoined public channel: %+v", got)
		}
	}
}

func TestListChannelsFiltersCollaborationAccessForWorkspaceMembers(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	userID := uuid.New()
	otherUserID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypeDM, Name: ""},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, userID}:      {ChannelID: channelID, UserID: userID},
			{channelID, otherUserID}: {ChannelID: channelID, UserID: otherUserID},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	svc := NewService(channels, nil, workspaces, nil, noopPublisher{}, nil, fakeCollabChecker{
		decision: collabaccess.Decision{Managed: true, Allowed: false},
	}, nil, nil)
	svc.SetAccessPolicy(accesspolicy.NewChecker(workspaces, channels, nil, fakeCollabChecker{
		decision: collabaccess.Decision{Managed: true, Allowed: false},
	}))

	got, err := svc.ListChannels(ctx, workspaceID, userID)
	if err != nil {
		t.Fatalf("ListChannels returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("channels = %+v, want collaboration-revoked DM hidden", got)
	}
}

func TestListDirectoryReadsAllPaginatedMembersAndChannels(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	actorID := uuid.New()

	workspaces := &fakeWorkspaceRepo{
		members: map[[2]uuid.UUID]*entity.WorkspaceMember{
			{workspaceID, actorID}: {WorkspaceID: workspaceID, UserID: actorID, Role: entity.WorkspaceRoleMember},
		},
	}
	for range 125 {
		userID := uuid.New()
		workspaces.listMembers = append(workspaces.listMembers, entity.WorkspaceMember{
			ID:          uuid.New(),
			WorkspaceID: workspaceID,
			UserID:      userID,
			Role:        entity.WorkspaceRoleMember,
			User: &entity.User{
				ID:          userID,
				Email:       "person@example.com",
				DisplayName: "Directory Person",
				Status:      entity.UserStatusActive,
			},
		})
	}

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{},
		members:  map[[2]uuid.UUID]*entity.ChannelMember{},
	}
	for range 125 {
		channelID := uuid.New()
		channels.channels[channelID] = &entity.Channel{
			ID:          channelID,
			WorkspaceID: &workspaceID,
			Name:        "directory-channel",
			Type:        entity.ChannelTypePublic,
		}
	}

	svc := NewService(channels, nil, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)

	got, err := svc.ListDirectory(ctx, workspaceID, actorID)
	if err != nil {
		t.Fatalf("ListDirectory returned error: %v", err)
	}
	if len(got.People) != 125 {
		t.Fatalf("people count = %d, want 125", len(got.People))
	}
	if len(got.Channels) != 125 {
		t.Fatalf("channels count = %d, want 125", len(got.Channels))
	}
}

func TestJoinAndLeaveChannelUseTransactionalEventEnqueue(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	userID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePublic},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	txScope := &fakeChatTxScope{channels: channels}
	txManager := &fakeChatTxManager{scope: txScope}

	svc := NewService(channels, nil, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)
	svc.SetTransactionManager(txManager)

	if err := svc.JoinChannel(ctx, channelID, userID); err != nil {
		t.Fatalf("JoinChannel returned error: %v", err)
	}
	if txManager.calls != 1 {
		t.Fatalf("tx calls after join = %d, want 1", txManager.calls)
	}
	if channels.members[[2]uuid.UUID{channelID, userID}] == nil {
		t.Fatalf("member not added during transactional join")
	}
	if len(txScope.events) != 1 || txScope.events[0].Type != eventpkg.TypeMemberJoined {
		t.Fatalf("events after join = %+v, want member.joined", txScope.events)
	}

	if err := svc.LeaveChannel(ctx, channelID, userID); err != nil {
		t.Fatalf("LeaveChannel returned error: %v", err)
	}
	if txManager.calls != 2 {
		t.Fatalf("tx calls after leave = %d, want 2", txManager.calls)
	}
	if channels.members[[2]uuid.UUID{channelID, userID}] != nil {
		t.Fatalf("member still present after transactional leave")
	}
	if len(txScope.events) != 3 ||
		txScope.events[1].Type != eventpkg.TypeMemberLeft ||
		txScope.events[2].Type != eventpkg.TypeMemberLeft ||
		txScope.events[1].Subject != "aloqa.chat."+channelID.String() ||
		txScope.events[2].Subject != workspaceUserEventsSubject(workspaceID, userID) {
		t.Fatalf("events after leave = %+v, want channel and per-user member.left", txScope.events)
	}
}

func TestListChannelMembersReturnsVisibleMembership(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	ownerID := uuid.New()
	memberID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePrivate},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, ownerID}:  {ChannelID: channelID, UserID: ownerID, Role: entity.ChannelRoleOwner},
			{channelID, memberID}: {ChannelID: channelID, UserID: memberID, Role: entity.ChannelRoleMember},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, ownerID}:  {WorkspaceID: workspaceID, UserID: ownerID, Role: entity.WorkspaceRoleOwner},
		{workspaceID, memberID}: {WorkspaceID: workspaceID, UserID: memberID, Role: entity.WorkspaceRoleMember},
	}}
	svc := NewService(channels, nil, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)

	members, err := svc.ListChannelMembers(ctx, channelID, ownerID)
	if err != nil {
		t.Fatalf("ListChannelMembers returned error: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("members length = %d, want 2", len(members))
	}
}

func TestAddChannelMembersRequiresManagerAndWorkspaceMembership(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	ownerID := uuid.New()
	memberID := uuid.New()
	targetID := uuid.New()
	outsiderID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePrivate},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, ownerID}:  {ChannelID: channelID, UserID: ownerID, Role: entity.ChannelRoleOwner},
			{channelID, memberID}: {ChannelID: channelID, UserID: memberID, Role: entity.ChannelRoleMember},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, ownerID}:  {WorkspaceID: workspaceID, UserID: ownerID, Role: entity.WorkspaceRoleOwner},
		{workspaceID, memberID}: {WorkspaceID: workspaceID, UserID: memberID, Role: entity.WorkspaceRoleMember},
		{workspaceID, targetID}: {WorkspaceID: workspaceID, UserID: targetID, Role: entity.WorkspaceRoleMember},
	}}
	publisher := &recordingSubjectPublisher{}
	svc := NewService(channels, nil, workspaces, nil, publisher, nil, nil, nil, nil)

	if _, err := svc.AddChannelMembers(ctx, channelID, memberID, []uuid.UUID{targetID}); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("AddChannelMembers member error = %v, want FORBIDDEN", err)
	}
	if _, err := svc.AddChannelMembers(ctx, channelID, ownerID, []uuid.UUID{outsiderID}); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("AddChannelMembers outsider error = %v, want FORBIDDEN", err)
	}

	added, err := svc.AddChannelMembers(ctx, channelID, ownerID, []uuid.UUID{targetID, targetID, ownerID})
	if err != nil {
		t.Fatalf("AddChannelMembers returned error: %v", err)
	}
	if len(added) != 1 {
		t.Fatalf("added length = %d, want 1", len(added))
	}
	if member := channels.members[[2]uuid.UUID{channelID, targetID}]; member == nil || member.Role != entity.ChannelRoleMember {
		t.Fatalf("target channel membership missing or wrong: %+v", member)
	}
	if publisher.hasEvent("aloqa.ws."+workspaceID.String(), eventpkg.TypeChannelCreated) {
		t.Fatalf("private channel.created leaked to workspace subject; subjects=%v", publisher.subjects())
	}
	payload, ok := publisher.channelCreatedPayload(workspaceUserEventsSubject(workspaceID, targetID))
	if !ok || payload.Channel == nil {
		t.Fatalf("channel.created payload missing after AddChannelMembers")
	}
	if !hasUUID(payload.Channel.Members, ownerID) ||
		!hasUUID(payload.Channel.Members, memberID) ||
		!hasUUID(payload.Channel.Members, targetID) {
		t.Fatalf("channel.created members = %v, want full current member list", payload.Channel.Members)
	}
}

func TestRemoveChannelMemberRequiresManagerAndProtectsOwner(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	ownerID := uuid.New()
	adminID := uuid.New()
	memberID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePrivate},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{
			{channelID, ownerID}:  {ChannelID: channelID, UserID: ownerID, Role: entity.ChannelRoleOwner},
			{channelID, adminID}:  {ChannelID: channelID, UserID: adminID, Role: entity.ChannelRoleAdmin},
			{channelID, memberID}: {ChannelID: channelID, UserID: memberID, Role: entity.ChannelRoleMember},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, ownerID}:  {WorkspaceID: workspaceID, UserID: ownerID, Role: entity.WorkspaceRoleOwner},
		{workspaceID, adminID}:  {WorkspaceID: workspaceID, UserID: adminID, Role: entity.WorkspaceRoleAdmin},
		{workspaceID, memberID}: {WorkspaceID: workspaceID, UserID: memberID, Role: entity.WorkspaceRoleMember},
	}}
	publisher := &recordingSubjectPublisher{}
	svc := NewService(channels, nil, workspaces, nil, publisher, nil, nil, nil, nil)

	if err := svc.RemoveChannelMember(ctx, channelID, memberID, adminID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("RemoveChannelMember member error = %v, want FORBIDDEN", err)
	}
	if err := svc.RemoveChannelMember(ctx, channelID, adminID, ownerID); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("RemoveChannelMember owner error = %v, want FORBIDDEN", err)
	}
	if err := svc.RemoveChannelMember(ctx, channelID, adminID, memberID); err != nil {
		t.Fatalf("RemoveChannelMember returned error: %v", err)
	}
	if channels.members[[2]uuid.UUID{channelID, memberID}] != nil {
		t.Fatalf("member still present after remove")
	}
	if !publisher.hasEvent("aloqa.chat."+channelID.String(), eventpkg.TypeMemberLeft) {
		t.Fatalf("member.left was not published to channel subject; subjects=%v", publisher.subjects())
	}
	if publisher.hasEvent("aloqa.ws."+workspaceID.String(), eventpkg.TypeMemberLeft) {
		t.Fatalf("member.left leaked to workspace subject; subjects=%v", publisher.subjects())
	}
	if !publisher.hasEvent(workspaceUserEventsSubject(workspaceID, memberID), eventpkg.TypeMemberLeft) {
		t.Fatalf("member.left was not published to removed user subject; subjects=%v", publisher.subjects())
	}
}

type noopPublisher struct{}

func (noopPublisher) Publish(context.Context, string, []byte) error { return nil }

type recordingPublisher struct {
	events [][]byte
}

func (p *recordingPublisher) Publish(_ context.Context, _ string, data []byte) error {
	copied := append([]byte(nil), data...)
	p.events = append(p.events, copied)
	return nil
}

type recordedPublish struct {
	subject string
	data    []byte
}

type recordingSubjectPublisher struct {
	events []recordedPublish
}

func (p *recordingSubjectPublisher) Publish(_ context.Context, subject string, data []byte) error {
	p.events = append(p.events, recordedPublish{subject: subject, data: append([]byte(nil), data...)})
	return nil
}

func (p *recordingSubjectPublisher) hasEvent(subject string, typ eventpkg.Type) bool {
	for _, published := range p.events {
		if published.subject != subject {
			continue
		}
		var envelope struct {
			Type eventpkg.Type `json:"type"`
		}
		if err := json.Unmarshal(published.data, &envelope); err == nil && envelope.Type == typ {
			return true
		}
	}
	return false
}

func (p *recordingSubjectPublisher) channelCreatedPayload(subject string) (eventpkg.ChannelPayload, bool) {
	for _, published := range p.events {
		if published.subject != subject {
			continue
		}
		var envelope struct {
			Type    eventpkg.Type   `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(published.data, &envelope); err != nil || envelope.Type != eventpkg.TypeChannelCreated {
			continue
		}
		var payload eventpkg.ChannelPayload
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			continue
		}
		return payload, true
	}
	return eventpkg.ChannelPayload{}, false
}

func (p *recordingSubjectPublisher) subjects() []string {
	subjects := make([]string, 0, len(p.events))
	for _, published := range p.events {
		subjects = append(subjects, published.subject)
	}
	return subjects
}

func hasUUID(values []uuid.UUID, want uuid.UUID) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func requireUUIDs(t *testing.T, got []uuid.UUID, want ...uuid.UUID) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("uuid slice = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("uuid slice = %v, want %v", got, want)
		}
	}
}

type recordingSearchIndexer struct {
	indexedChannels []uuid.UUID
	deletedChannels []uuid.UUID
}

func (r *recordingSearchIndexer) IndexMessage(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, time.Time) error {
	return nil
}

func (r *recordingSearchIndexer) DeleteMessage(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (r *recordingSearchIndexer) DeleteFile(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (r *recordingSearchIndexer) IndexChannel(_ context.Context, _, channelID uuid.UUID, _ string, _ string, _ time.Time, _ time.Time) error {
	r.indexedChannels = append(r.indexedChannels, channelID)
	return nil
}

func (r *recordingSearchIndexer) DeleteChannel(_ context.Context, _, channelID uuid.UUID) error {
	r.deletedChannels = append(r.deletedChannels, channelID)
	return nil
}

type searchDeleteCall struct {
	workspaceID  uuid.UUID
	resourceType searchsvc.ResourceType
	resourceID   uuid.UUID
}

type recordingSearchQueue struct {
	upserts []searchsvc.Document
	deletes []searchDeleteCall
}

func (r *recordingSearchQueue) EnqueueUpsert(_ context.Context, doc searchsvc.Document) error {
	r.upserts = append(r.upserts, doc)
	return nil
}

func (r *recordingSearchQueue) EnqueueDelete(_ context.Context, workspaceID uuid.UUID, resourceType searchsvc.ResourceType, resourceID uuid.UUID) error {
	r.deletes = append(r.deletes, searchDeleteCall{workspaceID: workspaceID, resourceType: resourceType, resourceID: resourceID})
	return nil
}

type fakeWorkspaceRepo struct {
	members     map[[2]uuid.UUID]*entity.WorkspaceMember
	listMembers []entity.WorkspaceMember
}

func (r *fakeWorkspaceRepo) Create(context.Context, *entity.Workspace) error { return nil }
func (r *fakeWorkspaceRepo) GetByID(context.Context, uuid.UUID) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}
func (r *fakeWorkspaceRepo) GetBySlug(context.Context, string) (*entity.Workspace, error) {
	return nil, cerrors.NotFound("workspace not found")
}
func (r *fakeWorkspaceRepo) ListByUser(context.Context, uuid.UUID) ([]entity.Workspace, error) {
	return nil, nil
}
func (r *fakeWorkspaceRepo) Update(context.Context, *entity.Workspace) error          { return nil }
func (r *fakeWorkspaceRepo) AddMember(context.Context, *entity.WorkspaceMember) error { return nil }
func (r *fakeWorkspaceRepo) GetMember(_ context.Context, workspaceID, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	if member := r.members[[2]uuid.UUID{workspaceID, userID}]; member != nil {
		return member, nil
	}
	return nil, cerrors.NotFound("workspace member not found")
}
func (r *fakeWorkspaceRepo) ListMembers(_ context.Context, workspaceID uuid.UUID, p pagination.Params, _ string) ([]entity.WorkspaceMember, error) {
	if len(r.listMembers) == 0 {
		return nil, nil
	}

	members := make([]entity.WorkspaceMember, 0, len(r.listMembers))
	for _, member := range r.listMembers {
		if member.WorkspaceID == workspaceID {
			members = append(members, member)
		}
	}
	sort.Slice(members, func(i, j int) bool {
		return members[i].ID.String() > members[j].ID.String()
	})
	return paginateWorkspaceMemberFakes(members, p), nil
}
func (r *fakeWorkspaceRepo) UpdateMemberRole(context.Context, uuid.UUID, uuid.UUID, entity.WorkspaceRole) error {
	return nil
}
func (r *fakeWorkspaceRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type fakeGuestAccessRepo struct {
	grants []entity.GuestAccessGrant
}

func (r *fakeGuestAccessRepo) CreateGrant(context.Context, *entity.GuestAccessGrant) error {
	return nil
}
func (r *fakeGuestAccessRepo) ListActiveByUserWorkspace(_ context.Context, userID, workspaceID uuid.UUID, now time.Time) ([]entity.GuestAccessGrant, error) {
	var active []entity.GuestAccessGrant
	for _, grant := range r.grants {
		if grant.UserID == userID && grant.WorkspaceID == workspaceID && grant.ExpiresAt.After(now) {
			active = append(active, grant)
		}
	}
	return active, nil
}

type fakeChannelGrantRepo struct {
	created []entity.ChannelAccessGrant
	grants  map[[2]uuid.UUID]*entity.ChannelAccessGrant
}

func (r *fakeChannelGrantRepo) CreateGrant(_ context.Context, grant *entity.ChannelAccessGrant) error {
	if r.grants == nil {
		r.grants = map[[2]uuid.UUID]*entity.ChannelAccessGrant{}
	}
	r.created = append(r.created, *grant)
	r.grants[[2]uuid.UUID{grant.ChannelID, grant.UserID}] = grant
	return nil
}

func (r *fakeChannelGrantRepo) GetGrant(_ context.Context, channelID, userID uuid.UUID) (*entity.ChannelAccessGrant, error) {
	if grant := r.grants[[2]uuid.UUID{channelID, userID}]; grant != nil {
		return grant, nil
	}
	return nil, cerrors.NotFound("channel access grant not found")
}

func (r *fakeChannelGrantRepo) ListByChannel(_ context.Context, channelID uuid.UUID) ([]entity.ChannelAccessGrant, error) {
	var grants []entity.ChannelAccessGrant
	for key, grant := range r.grants {
		if key[0] == channelID {
			grants = append(grants, *grant)
		}
	}
	return grants, nil
}

type fakeCollabChecker struct {
	decision collabaccess.Decision
	err      error
}

func (f fakeCollabChecker) AuthorizeChannel(context.Context, uuid.UUID, uuid.UUID) (collabaccess.Decision, error) {
	return f.decision, f.err
}

type fakeContactAuthorizer struct {
	err error
}

func (f fakeContactAuthorizer) CanShareChannel(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return f.err
}

type fakeChannelRepo struct {
	channels map[uuid.UUID]*entity.Channel
	members  map[[2]uuid.UUID]*entity.ChannelMember
	created  []entity.Channel
}

func (r *fakeChannelRepo) Create(_ context.Context, ch *entity.Channel) error {
	if r.channels == nil {
		r.channels = map[uuid.UUID]*entity.Channel{}
	}
	r.created = append(r.created, *ch)
	r.channels[ch.ID] = ch
	return nil
}
func (r *fakeChannelRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Channel, error) {
	if ch := r.channels[id]; ch != nil {
		return ch, nil
	}
	return nil, cerrors.NotFound("channel not found")
}
func (r *fakeChannelRepo) ListByWorkspace(_ context.Context, workspaceID uuid.UUID, p pagination.Params) ([]entity.Channel, error) {
	var channels []entity.Channel
	for _, ch := range r.channels {
		if ch.WorkspaceID != nil && *ch.WorkspaceID == workspaceID {
			channels = append(channels, *ch)
		}
	}
	sort.Slice(channels, func(i, j int) bool {
		return channels[i].ID.String() > channels[j].ID.String()
	})
	if p.Limit > 0 || p.Cursor != uuid.Nil {
		return paginateChannelFakes(channels, p), nil
	}
	return channels, nil
}

func paginateWorkspaceMemberFakes(members []entity.WorkspaceMember, p pagination.Params) []entity.WorkspaceMember {
	p.Normalize()
	page := make([]entity.WorkspaceMember, 0, p.Limit+1)
	for _, member := range members {
		if p.Cursor != uuid.Nil && member.ID.String() >= p.Cursor.String() {
			continue
		}
		page = append(page, member)
		if len(page) >= p.Limit+1 {
			return page
		}
	}
	return page
}

func paginateChannelFakes(channels []entity.Channel, p pagination.Params) []entity.Channel {
	p.Normalize()
	page := make([]entity.Channel, 0, p.Limit+1)
	for _, ch := range channels {
		if p.Cursor != uuid.Nil && ch.ID.String() >= p.Cursor.String() {
			continue
		}
		page = append(page, ch)
		if len(page) >= p.Limit+1 {
			return page
		}
	}
	return page
}

func (r *fakeChannelRepo) ListByUser(_ context.Context, workspaceID, userID uuid.UUID) ([]entity.Channel, error) {
	var channels []entity.Channel
	for key := range r.members {
		if key[1] != userID {
			continue
		}
		if ch := r.channels[key[0]]; ch != nil && ch.WorkspaceID != nil && *ch.WorkspaceID == workspaceID {
			channels = append(channels, *ch)
		}
	}
	return channels, nil
}
func (r *fakeChannelRepo) ListArchivedByUser(_ context.Context, _, _ uuid.UUID) ([]entity.ArchivedChannelInfo, error) {
	return []entity.ArchivedChannelInfo{}, nil
}
func (r *fakeChannelRepo) Update(_ context.Context, ch *entity.Channel) error {
	if r.channels == nil {
		r.channels = map[uuid.UUID]*entity.Channel{}
	}
	stored := *ch
	r.channels[ch.ID] = &stored
	return nil
}
func (r *fakeChannelRepo) Archive(context.Context, uuid.UUID) error { return nil }
func (r *fakeChannelRepo) AddMember(_ context.Context, member *entity.ChannelMember) error {
	if r.members == nil {
		r.members = map[[2]uuid.UUID]*entity.ChannelMember{}
	}
	r.members[[2]uuid.UUID{member.ChannelID, member.UserID}] = member
	return nil
}
func (r *fakeChannelRepo) GetMember(_ context.Context, channelID, userID uuid.UUID) (*entity.ChannelMember, error) {
	if member := r.members[[2]uuid.UUID{channelID, userID}]; member != nil {
		return member, nil
	}
	return nil, cerrors.NotFound("channel member not found")
}
func (r *fakeChannelRepo) ListMembers(_ context.Context, channelID uuid.UUID) ([]entity.ChannelMember, error) {
	members := make([]entity.ChannelMember, 0)
	for key, member := range r.members {
		if key[0] == channelID && member != nil {
			members = append(members, *member)
		}
	}
	return members, nil
}
func (r *fakeChannelRepo) RemoveMember(_ context.Context, channelID, userID uuid.UUID) error {
	key := [2]uuid.UUID{channelID, userID}
	if _, ok := r.members[key]; !ok {
		return cerrors.NotFound("channel member not found")
	}
	delete(r.members, key)
	return nil
}
func (r *fakeChannelRepo) UpdateLastRead(_ context.Context, channelID, userID uuid.UUID) error {
	if member := r.members[[2]uuid.UUID{channelID, userID}]; member != nil {
		member.LastReadAt = time.Now().UTC()
		return nil
	}
	return nil
}
func (r *fakeChannelRepo) GetDMChannel(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*entity.Channel, error) {
	return nil, cerrors.NotFound("dm channel not found")
}

type fakeMessageRepo struct {
	messages                           map[uuid.UUID]*entity.Message
	reactions                          map[uuid.UUID]entity.Reaction
	listReactionsCalls                 int
	listReactionsByMessageIDsCalls     int
	lastListReactionsByMessageIDsArg   []uuid.UUID
	resolveMentionsFunc                func(context.Context, uuid.UUID, uuid.UUID, string) ([]uuid.UUID, error)
	resolveMentionsCalls               []resolveMentionsCall
	resolveMentionsByMessageIDsCalls   int
	lastResolveMentionsByMessageIDsArg []uuid.UUID
	mentionsByMessageID                map[uuid.UUID][]uuid.UUID
}

type resolveMentionsCall struct {
	channelID uuid.UUID
	authorID  uuid.UUID
	content   string
}

func (r *fakeMessageRepo) Create(_ context.Context, msg *entity.Message) error {
	if r.messages == nil {
		r.messages = map[uuid.UUID]*entity.Message{}
	}
	r.messages[msg.ID] = msg
	return nil
}
func (r *fakeMessageRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Message, error) {
	if msg := r.messages[id]; msg != nil {
		return msg, nil
	}
	return nil, cerrors.NotFound("message not found")
}
func (r *fakeMessageRepo) ListByChannel(_ context.Context, channelID uuid.UUID, _ pagination.Params) ([]entity.Message, error) {
	var messages []entity.Message
	for _, msg := range r.messages {
		if msg.ChannelID == channelID {
			messages = append(messages, *msg)
		}
	}
	return messages, nil
}
func (r *fakeMessageRepo) ListThreadReplies(context.Context, uuid.UUID, pagination.Params) ([]entity.Message, error) {
	return nil, nil
}
func (r *fakeMessageRepo) ResolveMentions(ctx context.Context, channelID, authorID uuid.UUID, content string) ([]uuid.UUID, error) {
	r.resolveMentionsCalls = append(r.resolveMentionsCalls, resolveMentionsCall{
		channelID: channelID,
		authorID:  authorID,
		content:   content,
	})
	if r.resolveMentionsFunc != nil {
		return r.resolveMentionsFunc(ctx, channelID, authorID, content)
	}
	return []uuid.UUID{}, nil
}
func (r *fakeMessageRepo) ResolveMentionsByMessageIDs(_ context.Context, messageIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error) {
	r.resolveMentionsByMessageIDsCalls++
	r.lastResolveMentionsByMessageIDsArg = append([]uuid.UUID(nil), messageIDs...)
	mentionsByMessageID := make(map[uuid.UUID][]uuid.UUID, len(messageIDs))
	messageIDSet := make(map[uuid.UUID]struct{}, len(messageIDs))
	for _, messageID := range messageIDs {
		messageIDSet[messageID] = struct{}{}
	}
	for messageID, mentions := range r.mentionsByMessageID {
		if _, ok := messageIDSet[messageID]; ok {
			mentionsByMessageID[messageID] = append([]uuid.UUID(nil), mentions...)
		}
	}
	return mentionsByMessageID, nil
}
func (r *fakeMessageRepo) HasActiveMessage(_ context.Context, channelID uuid.UUID) (bool, error) {
	for _, msg := range r.messages {
		if msg.ChannelID == channelID && msg.DeletedAt == nil {
			return true, nil
		}
	}
	return false, nil
}
func (r *fakeMessageRepo) LastActivityByChannels(_ context.Context, channelIDs []uuid.UUID) (map[uuid.UUID]time.Time, error) {
	allowed := make(map[uuid.UUID]struct{}, len(channelIDs))
	for _, channelID := range channelIDs {
		allowed[channelID] = struct{}{}
	}
	activity := make(map[uuid.UUID]time.Time, len(channelIDs))
	for _, msg := range r.messages {
		if _, ok := allowed[msg.ChannelID]; !ok || msg.DeletedAt != nil {
			continue
		}
		if current, found := activity[msg.ChannelID]; !found || msg.CreatedAt.After(current) {
			activity[msg.ChannelID] = msg.CreatedAt
		}
	}
	return activity, nil
}
func (r *fakeMessageRepo) Update(context.Context, *entity.Message) error { return nil }
func (r *fakeMessageRepo) Move(_ context.Context, msg *entity.Message) error {
	if stored := r.messages[msg.ID]; stored != nil {
		*stored = *msg
		return nil
	}
	return cerrors.NotFound("message not found")
}
func (r *fakeMessageRepo) Pin(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (r *fakeMessageRepo) Unpin(context.Context, uuid.UUID) error          { return nil }
func (r *fakeMessageRepo) SoftDelete(_ context.Context, id uuid.UUID) error {
	msg := r.messages[id]
	if msg == nil {
		return cerrors.NotFound("message not found")
	}
	now := time.Now().UTC()
	msg.Content = ""
	msg.Edited = false
	msg.EditedAt = nil
	msg.Pinned = false
	msg.PinnedBy = nil
	msg.PinnedAt = nil
	msg.ForwardedFrom = nil
	msg.QuotedMessageID = nil
	msg.QuotedSnapshot = nil
	msg.ProfileShare = nil
	msg.UpdatedAt = now
	msg.DeletedAt = &now
	return nil
}
func (r *fakeMessageRepo) SoftDeleteWithCascade(ctx context.Context, id uuid.UUID) ([]uuid.UUID, error) {
	if err := r.SoftDelete(ctx, id); err != nil {
		return nil, err
	}
	deleted := true
	var affected []uuid.UUID
	for _, msg := range r.messages {
		if msg.QuotedMessageID == nil || *msg.QuotedMessageID != id {
			continue
		}
		if msg.QuotedSnapshot == nil {
			msg.QuotedSnapshot = &entity.QuotedSnapshot{}
		}
		msg.QuotedSnapshot.Deleted = &deleted
		msg.UpdatedAt = time.Now().UTC()
		affected = append(affected, msg.ID)
	}
	return affected, nil
}
func (r *fakeMessageRepo) ListPinned(context.Context, uuid.UUID) ([]entity.Message, error) {
	return nil, nil
}
func (r *fakeMessageRepo) AddReaction(_ context.Context, reaction *entity.Reaction) error {
	if r.reactions == nil {
		r.reactions = map[uuid.UUID]entity.Reaction{}
	}
	for _, existing := range r.reactions {
		if existing.MessageID == reaction.MessageID && existing.UserID == reaction.UserID && existing.Emoji == reaction.Emoji {
			return cerrors.AlreadyExists("reaction already exists")
		}
	}
	r.reactions[reaction.ID] = *reaction
	return nil
}
func (r *fakeMessageRepo) GetReactionByID(_ context.Context, id uuid.UUID) (*entity.Reaction, error) {
	if reaction, ok := r.reactions[id]; ok {
		return &reaction, nil
	}
	return nil, cerrors.NotFound("reaction not found")
}
func (r *fakeMessageRepo) GetReactionByMessageUserEmoji(_ context.Context, messageID, userID uuid.UUID, emoji string) (*entity.Reaction, error) {
	for _, reaction := range r.reactions {
		if reaction.MessageID == messageID && reaction.UserID == userID && reaction.Emoji == emoji {
			return &reaction, nil
		}
	}
	return nil, cerrors.NotFound("reaction not found")
}
func (r *fakeMessageRepo) RemoveReaction(_ context.Context, messageID, userID uuid.UUID, emoji string) error {
	for id, reaction := range r.reactions {
		if reaction.MessageID == messageID && reaction.UserID == userID && reaction.Emoji == emoji {
			delete(r.reactions, id)
			return nil
		}
	}
	return cerrors.NotFound("reaction not found")
}
func (r *fakeMessageRepo) RemoveReactionByID(_ context.Context, id uuid.UUID) error {
	if _, ok := r.reactions[id]; !ok {
		return cerrors.NotFound("reaction not found")
	}
	delete(r.reactions, id)
	return nil
}
func (r *fakeMessageRepo) ListReactions(_ context.Context, messageID uuid.UUID) ([]entity.Reaction, error) {
	r.listReactionsCalls++
	var reactions []entity.Reaction
	for _, reaction := range r.reactions {
		if reaction.MessageID == messageID {
			reactions = append(reactions, reaction)
		}
	}
	return reactions, nil
}
func (r *fakeMessageRepo) ListReactionsByMessageIDs(_ context.Context, messageIDs []uuid.UUID) (map[uuid.UUID][]entity.Reaction, error) {
	r.listReactionsByMessageIDsCalls++
	r.lastListReactionsByMessageIDsArg = append([]uuid.UUID(nil), messageIDs...)
	messageIDSet := make(map[uuid.UUID]struct{}, len(messageIDs))
	for _, messageID := range messageIDs {
		messageIDSet[messageID] = struct{}{}
	}

	reactionsByMessageID := make(map[uuid.UUID][]entity.Reaction, len(messageIDs))
	for _, reaction := range r.reactions {
		if _, ok := messageIDSet[reaction.MessageID]; ok {
			reactionsByMessageID[reaction.MessageID] = append(reactionsByMessageID[reaction.MessageID], reaction)
		}
	}
	return reactionsByMessageID, nil
}
func (r *fakeMessageRepo) CreateAttachment(context.Context, *entity.Attachment) error { return nil }
func (r *fakeMessageRepo) DeleteAttachment(context.Context, uuid.UUID) error          { return nil }
func (r *fakeMessageRepo) GetAttachmentByStoragePath(context.Context, string) (*entity.Attachment, error) {
	return nil, cerrors.NotFound("attachment not found")
}
func (r *fakeMessageRepo) ListAttachments(context.Context, uuid.UUID) ([]entity.Attachment, error) {
	return nil, nil
}
func (r *fakeMessageRepo) CountUnread(_ context.Context, channelID, userID uuid.UUID, since time.Time) (int, error) {
	count := 0
	for _, msg := range r.messages {
		if msg.ChannelID != channelID || msg.UserID == userID {
			continue
		}
		if msg.CreatedAt.After(since) {
			count++
		}
	}
	return count, nil
}
func (r *fakeMessageRepo) BatchUnreadCounts(context.Context, uuid.UUID, uuid.UUID) ([]repository.UnreadSummary, error) {
	return nil, nil
}
func (r *fakeMessageRepo) CountThreadReplies(context.Context, uuid.UUID) (int, error) { return 0, nil }

type fakeChatFileShare struct {
	fileID uuid.UUID
	opts   repository.FileShareOptions
}

type fakeChatFileRepo struct {
	shareErr error
	shares   []fakeChatFileShare
}

func (r *fakeChatFileRepo) CreateFile(context.Context, *entity.LibraryFile) error {
	return nil
}

func (r *fakeChatFileRepo) GetAccessibleFile(context.Context, uuid.UUID, uuid.UUID) (*entity.LibraryFile, error) {
	return nil, cerrors.NotFound("file not found")
}

func (r *fakeChatFileRepo) GetAccessibleFileByStoragePath(context.Context, string, uuid.UUID) (*entity.LibraryFile, error) {
	return nil, cerrors.NotFound("file not found")
}

func (r *fakeChatFileRepo) ListFiles(context.Context, repository.FileListParams) (entity.FileListResult, error) {
	return entity.FileListResult{}, nil
}

func (r *fakeChatFileRepo) SetFavorite(context.Context, uuid.UUID, uuid.UUID, bool) error {
	return nil
}

func (r *fakeChatFileRepo) StorageUsedBytes(context.Context, uuid.UUID) (int64, error) {
	return 0, nil
}

func (r *fakeChatFileRepo) DeleteFile(context.Context, uuid.UUID, uuid.UUID) (*entity.LibraryFile, error) {
	return nil, cerrors.NotFound("file not found")
}

func (r *fakeChatFileRepo) ShareFile(_ context.Context, fileID uuid.UUID, opts repository.FileShareOptions) error {
	r.shares = append(r.shares, fakeChatFileShare{fileID: fileID, opts: opts})
	if r.shareErr != nil {
		return r.shareErr
	}
	return nil
}

func (r *fakeChatFileRepo) RevokeFileShare(context.Context, uuid.UUID, repository.FileShareOptions) error {
	return nil
}

func (r *fakeChatFileRepo) ListFileShares(context.Context, uuid.UUID, uuid.UUID) ([]entity.FileShare, error) {
	return nil, nil
}

func (r *fakeChatFileRepo) ResolveMessageFiles(_ context.Context, fileIDs []uuid.UUID) ([]entity.MessageFile, error) {
	files := make([]entity.MessageFile, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		files = append(files, entity.MessageFile{ID: fileID})
	}
	return files, nil
}

type fakeChannelAccessStateRepo struct {
	states map[[2]uuid.UUID]*entity.ChannelAccessState
}

func (r *fakeChannelAccessStateRepo) GetState(_ context.Context, channelID, userID uuid.UUID) (*entity.ChannelAccessState, error) {
	if state := r.states[[2]uuid.UUID{channelID, userID}]; state != nil {
		return state, nil
	}
	return nil, cerrors.NotFound("channel access state not found")
}

func (r *fakeChannelAccessStateRepo) UpsertState(_ context.Context, state *entity.ChannelAccessState) error {
	if r.states == nil {
		r.states = map[[2]uuid.UUID]*entity.ChannelAccessState{}
	}
	copy := *state
	r.states[[2]uuid.UUID{state.ChannelID, state.UserID}] = &copy
	return nil
}

func hasCode(err error, code cerrors.Code) bool {
	appErr, ok := cerrors.AsAppError(err)
	return ok && appErr.Code == code
}

type fakeChatTxManager struct {
	scope txscope.Scope
	calls int
}

func (m *fakeChatTxManager) WithinTx(ctx context.Context, fn func(context.Context, txscope.Scope) error) error {
	m.calls++
	return fn(ctx, m.scope)
}

type fakeChatTxScope struct {
	messages repository.MessageRepository
	files    repository.FileRepository
	channels repository.ChannelRepository
	search   searchsvc.Indexer
	events   []eventpkg.Event
}

func (s *fakeChatTxScope) Users() repository.UserRepository                       { return nil }
func (s *fakeChatTxScope) Workspaces() repository.WorkspaceRepository             { return nil }
func (s *fakeChatTxScope) Messages() repository.MessageRepository                 { return s.messages }
func (s *fakeChatTxScope) Files() repository.FileRepository                       { return s.files }
func (s *fakeChatTxScope) Channels() repository.ChannelRepository                 { return s.channels }
func (s *fakeChatTxScope) ChannelGrants() repository.ChannelAccessGrantRepository { return nil }
func (s *fakeChatTxScope) Calls() repository.CallRepository                       { return nil }
func (s *fakeChatTxScope) CallMessages() repository.CallMessageRepository         { return nil }
func (s *fakeChatTxScope) Calendars() repository.CalendarRepository               { return nil }
func (s *fakeChatTxScope) Recordings() repository.RecordingRepository             { return nil }
func (s *fakeChatTxScope) Invites() repository.GuestInviteRepository              { return nil }
func (s *fakeChatTxScope) GuestGrants() repository.GuestAccessRepository          { return nil }
func (s *fakeChatTxScope) Roles() repository.WorkspaceRoleRepository              { return nil }
func (s *fakeChatTxScope) Audit() repository.AuditRepository                      { return nil }
func (s *fakeChatTxScope) SearchIndexer() searchsvc.Indexer                       { return s.search }
func (s *fakeChatTxScope) EnqueueRealtime(_ context.Context, evt eventpkg.Event, _ []byte) error {
	s.events = append(s.events, evt)
	return nil
}

func (r *fakeMessageRepo) HardDelete(context.Context, uuid.UUID) error { return nil }
