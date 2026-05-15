package chat

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	eventpkg "aloqa/internal/domain/event"
	"aloqa/internal/domain/repository"
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
			publicChannelID:  {ID: publicChannelID, WorkspaceID: workspaceID, Type: entity.ChannelTypePublic},
			privateChannelID: {ID: privateChannelID, WorkspaceID: workspaceID, Type: entity.ChannelTypePrivate},
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

func TestGuestGrantAllowsChannelAccessWithoutWorkspaceMembership(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	guestID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: workspaceID, Type: entity.ChannelTypePrivate},
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
	if channel == nil || channel.WorkspaceID != workspaceA {
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
			channelID: {ID: channelID, WorkspaceID: workspaceA, Type: entity.ChannelTypeDM},
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
			channelID: {ID: channelID, WorkspaceID: workspaceID, Type: entity.ChannelTypePrivate},
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

	if _, err := svc.SendMessage(ctx, channelID, guestID, "hi team", nil); err != nil {
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

func TestCollaboratorCanSendWithSharedAccessPolicy(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	localUserID := uuid.New()
	remoteUserID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: workspaceID, Type: entity.ChannelTypeDM},
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

	msg, err := svc.SendMessage(ctx, channelID, remoteUserID, "from remote", nil)
	if err != nil {
		t.Fatalf("SendMessage collaborator returned error: %v", err)
	}
	if msg.UserID != remoteUserID {
		t.Fatalf("message user = %s, want %s", msg.UserID, remoteUserID)
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
			channelID: {ID: channelID, WorkspaceID: workspaceID, Type: entity.ChannelTypePrivate},
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

	if err := svc.AddReaction(ctx, messageID, guestID, ":+1:"); err != nil {
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
			channelID: {ID: channelID, WorkspaceID: workspaceID, Type: entity.ChannelTypePublic},
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

func TestJoinAndLeaveChannelUseTransactionalEventEnqueue(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	channelID := uuid.New()
	userID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			channelID: {ID: channelID, WorkspaceID: workspaceID, Type: entity.ChannelTypePublic},
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
	if len(txScope.events) != 2 || txScope.events[1].Type != eventpkg.TypeMemberLeft {
		t.Fatalf("events after leave = %+v, want member.left", txScope.events)
	}
}

type noopPublisher struct{}

func (noopPublisher) Publish(context.Context, string, []byte) error { return nil }

type fakeWorkspaceRepo struct {
	members map[[2]uuid.UUID]*entity.WorkspaceMember
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
func (r *fakeWorkspaceRepo) ListMembers(context.Context, uuid.UUID, pagination.Params) ([]entity.WorkspaceMember, error) {
	return nil, nil
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
func (r *fakeChannelRepo) ListByWorkspace(_ context.Context, workspaceID uuid.UUID, _ pagination.Params) ([]entity.Channel, error) {
	var channels []entity.Channel
	for _, ch := range r.channels {
		if ch.WorkspaceID == workspaceID {
			channels = append(channels, *ch)
		}
	}
	return channels, nil
}
func (r *fakeChannelRepo) ListByUser(_ context.Context, workspaceID, userID uuid.UUID) ([]entity.Channel, error) {
	var channels []entity.Channel
	for key := range r.members {
		if key[1] != userID {
			continue
		}
		if ch := r.channels[key[0]]; ch != nil && ch.WorkspaceID == workspaceID {
			channels = append(channels, *ch)
		}
	}
	return channels, nil
}
func (r *fakeChannelRepo) Update(context.Context, *entity.Channel) error { return nil }
func (r *fakeChannelRepo) Archive(context.Context, uuid.UUID) error      { return nil }
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
func (r *fakeChannelRepo) ListMembers(context.Context, uuid.UUID) ([]entity.ChannelMember, error) {
	return nil, nil
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
	messages map[uuid.UUID]*entity.Message
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
func (r *fakeMessageRepo) Update(context.Context, *entity.Message) error   { return nil }
func (r *fakeMessageRepo) SoftDelete(context.Context, uuid.UUID) error     { return nil }
func (r *fakeMessageRepo) Pin(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (r *fakeMessageRepo) Unpin(context.Context, uuid.UUID) error          { return nil }
func (r *fakeMessageRepo) ListPinned(context.Context, uuid.UUID) ([]entity.Message, error) {
	return nil, nil
}
func (r *fakeMessageRepo) AddReaction(context.Context, *entity.Reaction) error { return nil }
func (r *fakeMessageRepo) RemoveReaction(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}
func (r *fakeMessageRepo) ListReactions(context.Context, uuid.UUID) ([]entity.Reaction, error) {
	return nil, nil
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
	channels repository.ChannelRepository
	events   []eventpkg.Event
}

func (s *fakeChatTxScope) Users() repository.UserRepository                       { return nil }
func (s *fakeChatTxScope) Workspaces() repository.WorkspaceRepository             { return nil }
func (s *fakeChatTxScope) Messages() repository.MessageRepository                 { return nil }
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
func (s *fakeChatTxScope) SearchIndexer() searchsvc.Indexer                       { return nil }
func (s *fakeChatTxScope) EnqueueRealtime(_ context.Context, evt eventpkg.Event, _ []byte) error {
	s.events = append(s.events, evt)
	return nil
}
