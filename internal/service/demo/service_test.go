package demo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
	calendarsvc "aloqa/internal/service/calendar"
)

func TestSeedNewUserCreatesDemoWorkspaceChannelsDMsAndEvents(t *testing.T) {
	ctx := context.Background()
	newUser := &entity.User{
		ID:          uuid.New(),
		Email:       "new@example.com",
		DisplayName: "New User",
		Status:      entity.UserStatusActive,
	}
	contacts := []entity.User{
		{ID: uuid.New(), Email: "one@example.com", DisplayName: "One", Status: entity.UserStatusActive},
		{ID: uuid.New(), Email: "two@example.com", DisplayName: "Two", Status: entity.UserStatusActive},
	}
	workspaces := newFakeWorkspaceRepo()
	channels := newFakeChannelRepo()
	calendars := &fakeCalendarService{calendarID: uuid.New()}
	svc := NewService(&fakeUserLister{users: contacts}, workspaces, channels, calendars)
	svc.now = func() time.Time {
		return time.Date(2026, time.May, 15, 8, 0, 0, 0, time.UTC)
	}

	if err := svc.SeedNewUser(ctx, newUser); err != nil {
		t.Fatalf("SeedNewUser returned error: %v", err)
	}

	workspace := workspaces.bySlug[demoWorkspaceSlug(newUser.ID)]
	if workspace == nil {
		t.Fatalf("demo workspace was not created")
	}
	if workspace.Name != demoWorkspaceName {
		t.Fatalf("workspace name = %q, want %q", workspace.Name, demoWorkspaceName)
	}
	if role := workspaces.members[[2]uuid.UUID{workspace.ID, newUser.ID}].Role; role != entity.WorkspaceRoleOwner {
		t.Fatalf("new user workspace role = %q, want owner", role)
	}
	for _, contact := range contacts {
		if member := workspaces.members[[2]uuid.UUID{workspace.ID, contact.ID}]; member == nil {
			t.Fatalf("contact %s was not added to demo workspace", contact.ID)
		}
	}

	for _, spec := range demoChannelSpecs {
		channel := channels.channelByName(workspace.ID, spec.name)
		if channel == nil {
			t.Fatalf("demo channel %q was not created", spec.name)
		}
		if !strings.Contains(channel.Topic, "Demo:") {
			t.Fatalf("channel topic = %q, want Demo marker", channel.Topic)
		}
		if member := channels.members[[2]uuid.UUID{channel.ID, newUser.ID}]; member == nil {
			t.Fatalf("new user was not added to channel %q", spec.name)
		}
		for _, contact := range contacts {
			if member := channels.members[[2]uuid.UUID{channel.ID, contact.ID}]; member == nil {
				t.Fatalf("contact %s was not added to channel %q", contact.ID, spec.name)
			}
		}
	}

	if got := channels.countType(entity.ChannelTypeDM); got != len(contacts) {
		t.Fatalf("dm count = %d, want %d", got, len(contacts))
	}
	if got := len(calendars.events); got != 3 {
		t.Fatalf("calendar events = %d, want 3", got)
	}
	for _, eventInput := range calendars.events {
		if !strings.Contains(eventInput.Title, "Demo:") {
			t.Fatalf("event title = %q, want Demo marker", eventInput.Title)
		}
		if len(eventInput.AttendeeUserIDs) != len(contacts) {
			t.Fatalf("event attendees = %d, want %d", len(eventInput.AttendeeUserIDs), len(contacts))
		}
		if eventInput.OriginatorTZ != demoOriginatorTZ {
			t.Fatalf("originator tz = %q, want %q", eventInput.OriginatorTZ, demoOriginatorTZ)
		}
	}
}

type fakeUserLister struct {
	users []entity.User
}

func (l *fakeUserLister) ListActiveExcept(_ context.Context, excludedID uuid.UUID, limit int) ([]entity.User, error) {
	result := make([]entity.User, 0, len(l.users))
	for _, user := range l.users {
		if user.ID == excludedID {
			continue
		}
		result = append(result, user)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

type fakeWorkspaceRepo struct {
	workspaces map[uuid.UUID]*entity.Workspace
	bySlug     map[string]*entity.Workspace
	members    map[[2]uuid.UUID]*entity.WorkspaceMember
}

func newFakeWorkspaceRepo() *fakeWorkspaceRepo {
	return &fakeWorkspaceRepo{
		workspaces: map[uuid.UUID]*entity.Workspace{},
		bySlug:     map[string]*entity.Workspace{},
		members:    map[[2]uuid.UUID]*entity.WorkspaceMember{},
	}
}

func (r *fakeWorkspaceRepo) Create(_ context.Context, workspace *entity.Workspace) error {
	r.workspaces[workspace.ID] = workspace
	r.bySlug[workspace.Slug] = workspace
	return nil
}

func (r *fakeWorkspaceRepo) CreateWithOwner(ctx context.Context, workspace *entity.Workspace, owner *entity.WorkspaceMember) error {
	if err := r.Create(ctx, workspace); err != nil {
		return err
	}
	return r.AddMember(ctx, owner)
}

func (r *fakeWorkspaceRepo) GetByID(_ context.Context, workspaceID uuid.UUID) (*entity.Workspace, error) {
	workspace := r.workspaces[workspaceID]
	if workspace == nil {
		return nil, cerrors.NotFound("workspace not found")
	}
	return workspace, nil
}

func (r *fakeWorkspaceRepo) GetBySlug(_ context.Context, slug string) (*entity.Workspace, error) {
	workspace := r.bySlug[slug]
	if workspace == nil {
		return nil, cerrors.NotFound("workspace not found")
	}
	return workspace, nil
}

func (r *fakeWorkspaceRepo) ListByUser(_ context.Context, userID uuid.UUID) ([]entity.Workspace, error) {
	workspaces := make([]entity.Workspace, 0)
	for key := range r.members {
		if key[1] != userID {
			continue
		}
		if workspace := r.workspaces[key[0]]; workspace != nil {
			workspaces = append(workspaces, *workspace)
		}
	}
	return workspaces, nil
}

func (r *fakeWorkspaceRepo) Update(_ context.Context, workspace *entity.Workspace) error {
	r.workspaces[workspace.ID] = workspace
	r.bySlug[workspace.Slug] = workspace
	return nil
}

func (r *fakeWorkspaceRepo) AddMember(_ context.Context, member *entity.WorkspaceMember) error {
	key := [2]uuid.UUID{member.WorkspaceID, member.UserID}
	if r.members[key] != nil {
		return cerrors.AlreadyExists("member already exists")
	}
	r.members[key] = member
	return nil
}

func (r *fakeWorkspaceRepo) GetMember(_ context.Context, workspaceID, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	member := r.members[[2]uuid.UUID{workspaceID, userID}]
	if member == nil {
		return nil, cerrors.NotFound("workspace member not found")
	}
	return member, nil
}

func (r *fakeWorkspaceRepo) ListMembers(_ context.Context, workspaceID uuid.UUID, _ pagination.Params) ([]entity.WorkspaceMember, error) {
	members := make([]entity.WorkspaceMember, 0)
	for key, member := range r.members {
		if key[0] == workspaceID {
			members = append(members, *member)
		}
	}
	return members, nil
}

func (r *fakeWorkspaceRepo) UpdateMemberRole(_ context.Context, workspaceID, userID uuid.UUID, role entity.WorkspaceRole) error {
	member := r.members[[2]uuid.UUID{workspaceID, userID}]
	if member == nil {
		return cerrors.NotFound("workspace member not found")
	}
	member.Role = role
	return nil
}

func (r *fakeWorkspaceRepo) RemoveMember(_ context.Context, workspaceID, userID uuid.UUID) error {
	delete(r.members, [2]uuid.UUID{workspaceID, userID})
	return nil
}

type fakeChannelRepo struct {
	channels map[uuid.UUID]*entity.Channel
	members  map[[2]uuid.UUID]*entity.ChannelMember
}

func newFakeChannelRepo() *fakeChannelRepo {
	return &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{},
		members:  map[[2]uuid.UUID]*entity.ChannelMember{},
	}
}

func (r *fakeChannelRepo) Create(_ context.Context, channel *entity.Channel) error {
	r.channels[channel.ID] = channel
	return nil
}

func (r *fakeChannelRepo) GetByID(_ context.Context, channelID uuid.UUID) (*entity.Channel, error) {
	channel := r.channels[channelID]
	if channel == nil {
		return nil, cerrors.NotFound("channel not found")
	}
	return channel, nil
}

func (r *fakeChannelRepo) ListByWorkspace(_ context.Context, workspaceID uuid.UUID, _ pagination.Params) ([]entity.Channel, error) {
	channels := make([]entity.Channel, 0)
	for _, channel := range r.channels {
		if channel.WorkspaceID != nil && *channel.WorkspaceID == workspaceID {
			channels = append(channels, *channel)
		}
	}
	return channels, nil
}

func (r *fakeChannelRepo) ListByUser(_ context.Context, workspaceID, userID uuid.UUID) ([]entity.Channel, error) {
	channels := make([]entity.Channel, 0)
	for key := range r.members {
		if key[1] != userID {
			continue
		}
		if channel := r.channels[key[0]]; channel != nil && channel.WorkspaceID != nil && *channel.WorkspaceID == workspaceID {
			channels = append(channels, *channel)
		}
	}
	return channels, nil
}

func (r *fakeChannelRepo) Update(_ context.Context, channel *entity.Channel) error {
	r.channels[channel.ID] = channel
	return nil
}

func (r *fakeChannelRepo) Archive(_ context.Context, channelID uuid.UUID) error {
	channel := r.channels[channelID]
	if channel == nil {
		return cerrors.NotFound("channel not found")
	}
	channel.Archived = true
	return nil
}

func (r *fakeChannelRepo) AddMember(_ context.Context, member *entity.ChannelMember) error {
	key := [2]uuid.UUID{member.ChannelID, member.UserID}
	if r.members[key] != nil {
		return cerrors.AlreadyExists("channel member already exists")
	}
	r.members[key] = member
	return nil
}

func (r *fakeChannelRepo) GetMember(_ context.Context, channelID, userID uuid.UUID) (*entity.ChannelMember, error) {
	member := r.members[[2]uuid.UUID{channelID, userID}]
	if member == nil {
		return nil, cerrors.NotFound("channel member not found")
	}
	return member, nil
}

func (r *fakeChannelRepo) ListMembers(_ context.Context, channelID uuid.UUID) ([]entity.ChannelMember, error) {
	members := make([]entity.ChannelMember, 0)
	for key, member := range r.members {
		if key[0] == channelID {
			members = append(members, *member)
		}
	}
	return members, nil
}

func (r *fakeChannelRepo) RemoveMember(_ context.Context, channelID, userID uuid.UUID) error {
	delete(r.members, [2]uuid.UUID{channelID, userID})
	return nil
}

func (r *fakeChannelRepo) UpdateLastRead(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (r *fakeChannelRepo) GetDMChannel(_ context.Context, workspaceID, userA, userB uuid.UUID) (*entity.Channel, error) {
	for _, channel := range r.channels {
		if channel.WorkspaceID == nil || *channel.WorkspaceID != workspaceID || channel.Type != entity.ChannelTypeDM {
			continue
		}
		if r.members[[2]uuid.UUID{channel.ID, userA}] != nil && r.members[[2]uuid.UUID{channel.ID, userB}] != nil {
			return channel, nil
		}
	}
	return nil, cerrors.NotFound("dm channel not found")
}

func (r *fakeChannelRepo) channelByName(workspaceID uuid.UUID, name string) *entity.Channel {
	for _, channel := range r.channels {
		if channel.WorkspaceID != nil && *channel.WorkspaceID == workspaceID && channel.Name == name {
			return channel
		}
	}
	return nil
}

func (r *fakeChannelRepo) countType(channelType entity.ChannelType) int {
	count := 0
	for _, channel := range r.channels {
		if channel.Type == channelType {
			count++
		}
	}
	return count
}

type fakeCalendarService struct {
	calendarID uuid.UUID
	events     []calendarsvc.CreateEventInput
}

func (s *fakeCalendarService) ListUserCalendars(_ context.Context, workspaceID, ownerID uuid.UUID) ([]entity.UserCalendar, error) {
	return []entity.UserCalendar{{
		ID:          s.calendarID,
		OwnerID:     ownerID,
		WorkspaceID: workspaceID,
		Name:        "Рабочий",
		Color:       entity.CalendarColorBrand,
		IsDefault:   true,
		IsVisible:   true,
	}}, nil
}

func (s *fakeCalendarService) CreateEvent(_ context.Context, workspaceID, organizerID uuid.UUID, input calendarsvc.CreateEventInput) (*entity.CalendarEvent, error) {
	s.events = append(s.events, input)
	return &entity.CalendarEvent{
		ID:          uuid.New(),
		CalendarID:  input.CalendarID,
		WorkspaceID: workspaceID,
		OrganizerID: organizerID,
		Title:       input.Title,
	}, nil
}
