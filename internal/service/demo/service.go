package demo

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/pkg/pagination"
	calendarsvc "aloqa/internal/service/calendar"
)

const (
	demoWorkspaceName       = "Demo Workspace"
	demoWorkspaceSlugPrefix = "demo-"
	demoContactLimit        = 50
	demoOriginatorTZ        = "Asia/Tashkent"
)

type UserLister interface {
	ListActiveExcept(ctx context.Context, excludedID uuid.UUID, limit int) ([]entity.User, error)
}

type CalendarService interface {
	ListUserCalendars(ctx context.Context, workspaceID, ownerID uuid.UUID) ([]entity.UserCalendar, error)
	CreateEvent(ctx context.Context, workspaceID, organizerID uuid.UUID, input calendarsvc.CreateEventInput) (*entity.CalendarEvent, error)
}

type Service struct {
	users      UserLister
	workspaces repository.WorkspaceRepository
	channels   repository.ChannelRepository
	calendars  CalendarService
	now        func() time.Time
}

type workspaceOwnerCreator interface {
	CreateWithOwner(ctx context.Context, ws *entity.Workspace, owner *entity.WorkspaceMember) error
}

type channelSpec struct {
	name  string
	topic string
}

var demoChannelSpecs = []channelSpec{
	{name: "demo-general", topic: "Demo: общие обсуждения, объявления и быстрый контекст"},
	{name: "demo-product", topic: "Demo: продуктовые планы, задачи и решения"},
	{name: "demo-support", topic: "Demo: вопросы, помощь и разбор обращений"},
}

func NewService(
	users UserLister,
	workspaces repository.WorkspaceRepository,
	channels repository.ChannelRepository,
	calendars CalendarService,
) *Service {
	return &Service{
		users:      users,
		workspaces: workspaces,
		channels:   channels,
		calendars:  calendars,
		now:        time.Now,
	}
}

func (s *Service) SeedNewUser(ctx context.Context, user *entity.User) error {
	if user == nil || user.ID == uuid.Nil {
		return cerrors.InvalidInput("user is required")
	}
	if s == nil || s.workspaces == nil || s.channels == nil {
		return cerrors.Unavailable("demo seeding is not configured")
	}

	workspace, err := s.ensureDemoWorkspace(ctx, user)
	if err != nil {
		return err
	}

	contacts, err := s.demoContacts(ctx, user.ID)
	if err != nil {
		return err
	}
	if err := s.ensureWorkspaceMembers(ctx, workspace.ID, contacts); err != nil {
		return err
	}

	channels, err := s.ensureDemoChannels(ctx, workspace.ID, user.ID, contacts)
	if err != nil {
		return err
	}
	if err := s.ensureDemoDMs(ctx, workspace.ID, user.ID, contacts); err != nil {
		return err
	}
	if s.calendars != nil {
		if err := s.seedCalendarEvents(ctx, workspace.ID, user.ID, contacts, channels); err != nil {
			return err
		}
	}

	slog.InfoContext(ctx, "demo workspace seeded", "workspace_id", workspace.ID, "user_id", user.ID, "contacts", len(contacts))
	return nil
}

func (s *Service) ensureDemoWorkspace(ctx context.Context, user *entity.User) (*entity.Workspace, error) {
	slug := demoWorkspaceSlug(user.ID)
	workspace, err := s.workspaces.GetBySlug(ctx, slug)
	if err == nil {
		if err := s.ensureWorkspaceMember(ctx, workspace.ID, user.ID, entity.WorkspaceRoleOwner); err != nil {
			return nil, err
		}
		return workspace, nil
	}
	if !isCode(err, cerrors.CodeNotFound) {
		return nil, cerrors.Internal("failed to fetch demo workspace", err)
	}

	now := s.currentTime()
	workspace = &entity.Workspace{
		ID:        id.New(),
		Name:      demoWorkspaceName,
		Slug:      slug,
		Kind:      entity.WorkspaceKindOrganization,
		CreatedBy: user.ID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	owner := &entity.WorkspaceMember{
		ID:          id.New(),
		WorkspaceID: workspace.ID,
		UserID:      user.ID,
		Role:        entity.WorkspaceRoleOwner,
		JoinedAt:    now,
	}

	if creator, ok := s.workspaces.(workspaceOwnerCreator); ok {
		if err := creator.CreateWithOwner(ctx, workspace, owner); err != nil {
			return nil, wrapDemoWrite("failed to create demo workspace", err)
		}
		return workspace, nil
	}
	if err := s.workspaces.Create(ctx, workspace); err != nil {
		return nil, wrapDemoWrite("failed to create demo workspace", err)
	}
	if err := s.workspaces.AddMember(ctx, owner); err != nil {
		return nil, wrapDemoWrite("failed to add demo workspace owner", err)
	}
	return workspace, nil
}

func (s *Service) demoContacts(ctx context.Context, userID uuid.UUID) ([]entity.User, error) {
	if s.users == nil {
		return nil, nil
	}
	users, err := s.users.ListActiveExcept(ctx, userID, demoContactLimit)
	if err != nil {
		return nil, cerrors.Internal("failed to list demo contacts", err)
	}
	contacts := make([]entity.User, 0, len(users))
	for _, user := range users {
		if user.ID == uuid.Nil || user.ID == userID || user.Status != entity.UserStatusActive {
			continue
		}
		contacts = append(contacts, user)
	}
	return contacts, nil
}

func (s *Service) ensureWorkspaceMembers(ctx context.Context, workspaceID uuid.UUID, contacts []entity.User) error {
	for _, contact := range contacts {
		if err := s.ensureWorkspaceMember(ctx, workspaceID, contact.ID, entity.WorkspaceRoleMember); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ensureWorkspaceMember(ctx context.Context, workspaceID, userID uuid.UUID, role entity.WorkspaceRole) error {
	member := &entity.WorkspaceMember{
		ID:          id.New(),
		WorkspaceID: workspaceID,
		UserID:      userID,
		Role:        role,
		JoinedAt:    s.currentTime(),
	}
	if err := s.workspaces.AddMember(ctx, member); err != nil && !isCode(err, cerrors.CodeAlreadyExists) {
		return wrapDemoWrite("failed to add demo workspace member", err)
	}
	return nil
}

func (s *Service) ensureDemoChannels(
	ctx context.Context,
	workspaceID uuid.UUID,
	userID uuid.UUID,
	contacts []entity.User,
) (map[string]entity.Channel, error) {
	existing, err := s.channels.ListByWorkspace(ctx, workspaceID, pagination.Params{Limit: pagination.MaxLimit})
	if err != nil {
		return nil, cerrors.Internal("failed to list demo channels", err)
	}

	byName := make(map[string]entity.Channel, len(existing)+len(demoChannelSpecs))
	for i := range existing {
		if existing[i].Type != entity.ChannelTypePublic {
			continue
		}
		byName[existing[i].Name] = existing[i]
	}

	for _, spec := range demoChannelSpecs {
		channel, ok := byName[spec.name]
		if !ok {
			created, err := s.createChannel(ctx, workspaceID, userID, spec)
			if err != nil {
				return nil, err
			}
			channel = *created
			byName[spec.name] = channel
		}

		if err := s.ensureChannelMember(ctx, channel.ID, userID, entity.ChannelRoleOwner); err != nil {
			return nil, err
		}
		for _, contact := range contacts {
			if err := s.ensureChannelMember(ctx, channel.ID, contact.ID, entity.ChannelRoleMember); err != nil {
				return nil, err
			}
		}
	}
	return byName, nil
}

func (s *Service) createChannel(ctx context.Context, workspaceID, userID uuid.UUID, spec channelSpec) (*entity.Channel, error) {
	now := s.currentTime()
	channel := &entity.Channel{
		ID:          id.New(),
		WorkspaceID: &workspaceID,
		Name:        spec.name,
		Topic:       spec.topic,
		Type:        entity.ChannelTypePublic,
		CreatedBy:   userID,
		Archived:    false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.channels.Create(ctx, channel); err != nil {
		return nil, wrapDemoWrite("failed to create demo channel", err)
	}
	return channel, nil
}

func (s *Service) ensureDemoDMs(ctx context.Context, workspaceID, userID uuid.UUID, contacts []entity.User) error {
	for _, contact := range contacts {
		existing, err := s.channels.GetDMChannel(ctx, workspaceID, userID, contact.ID)
		if err != nil && !isCode(err, cerrors.CodeNotFound) {
			return cerrors.Internal("failed to find demo direct message", err)
		}
		if existing != nil {
			continue
		}
		if err := s.createDM(ctx, workspaceID, userID, contact.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) createDM(ctx context.Context, workspaceID, ownerID, contactID uuid.UUID) error {
	now := s.currentTime()
	channel := &entity.Channel{
		ID:          id.New(),
		WorkspaceID: &workspaceID,
		Name:        "",
		Topic:       "Demo: direct message",
		Type:        entity.ChannelTypeDM,
		CreatedBy:   ownerID,
		Archived:    false,
		CreatedAt:   now,
		UpdatedAt:   now,
		Members:     []uuid.UUID{ownerID, contactID},
	}
	if err := s.channels.Create(ctx, channel); err != nil {
		return wrapDemoWrite("failed to create demo direct message", err)
	}
	if err := s.ensureChannelMember(ctx, channel.ID, ownerID, entity.ChannelRoleMember); err != nil {
		return err
	}
	return s.ensureChannelMember(ctx, channel.ID, contactID, entity.ChannelRoleMember)
}

func (s *Service) ensureChannelMember(ctx context.Context, channelID, userID uuid.UUID, role entity.ChannelRole) error {
	member := &entity.ChannelMember{
		ID:         id.New(),
		ChannelID:  channelID,
		UserID:     userID,
		Role:       role,
		LastReadAt: s.currentTime(),
		JoinedAt:   s.currentTime(),
	}
	if err := s.channels.AddMember(ctx, member); err != nil && !isCode(err, cerrors.CodeAlreadyExists) {
		return wrapDemoWrite("failed to add demo channel member", err)
	}
	return nil
}

func (s *Service) seedCalendarEvents(
	ctx context.Context,
	workspaceID uuid.UUID,
	userID uuid.UUID,
	contacts []entity.User,
	channels map[string]entity.Channel,
) error {
	calendars, err := s.calendars.ListUserCalendars(ctx, workspaceID, userID)
	if err != nil {
		return cerrors.Internal("failed to list demo calendars", err)
	}
	calendar, ok := defaultCalendar(calendars)
	if !ok {
		return cerrors.NotFound("default calendar not found")
	}

	attendeeIDs := userIDs(contacts)
	requiredIDs := append([]uuid.UUID(nil), attendeeIDs...)
	reminders := []entity.EventReminder{{OffsetMinutes: 10, Channel: entity.ReminderChannelInApp}}
	events := []calendarsvc.CreateEventInput{
		{
			CalendarID:      calendar.ID,
			ChannelID:       channelID(channels, "demo-general"),
			Title:           "Demo: ежедневный синк",
			Description:     stringPtr("Demo: короткая встреча для проверки календаря, участников и напоминаний."),
			Location:        entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt:     s.demoStart(1, 10),
			OriginatorTZ:    demoOriginatorTZ,
			DurationMinutes: 30,
			AttendeeUserIDs: attendeeIDs,
			RequiredUserIDs: requiredIDs,
			Reminders:       reminders,
		},
		{
			CalendarID:      calendar.ID,
			ChannelID:       channelID(channels, "demo-product"),
			Title:           "Demo: планирование спринта",
			Description:     stringPtr("Demo: тестовая встреча с привязкой к продуктовому каналу."),
			Location:        entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt:     s.demoStart(2, 11),
			OriginatorTZ:    demoOriginatorTZ,
			DurationMinutes: 60,
			AttendeeUserIDs: attendeeIDs,
			RequiredUserIDs: requiredIDs,
			Reminders:       reminders,
		},
		{
			CalendarID:      calendar.ID,
			ChannelID:       channelID(channels, "demo-support"),
			Title:           "Demo: разбор обратной связи",
			Description:     stringPtr("Demo: встреча для проверки отображения будущих событий."),
			Location:        entity.EventLocation{Type: entity.EventLocationAloqaMeet},
			ScheduledAt:     s.demoStart(3, 15),
			OriginatorTZ:    demoOriginatorTZ,
			DurationMinutes: 45,
			AttendeeUserIDs: attendeeIDs,
			RequiredUserIDs: requiredIDs,
			Reminders:       reminders,
		},
	}

	for _, eventInput := range events {
		if _, err := s.calendars.CreateEvent(ctx, workspaceID, userID, eventInput); err != nil {
			return cerrors.Internal("failed to create demo calendar event", err)
		}
	}
	return nil
}

func defaultCalendar(calendars []entity.UserCalendar) (entity.UserCalendar, bool) {
	for _, calendar := range calendars {
		if calendar.IsDefault {
			return calendar, true
		}
	}
	if len(calendars) == 0 {
		return entity.UserCalendar{}, false
	}
	return calendars[0], true
}

func userIDs(users []entity.User) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(users))
	for _, user := range users {
		if user.ID != uuid.Nil {
			ids = append(ids, user.ID)
		}
	}
	return ids
}

func channelID(channels map[string]entity.Channel, name string) *uuid.UUID {
	channel, ok := channels[name]
	if !ok || channel.ID == uuid.Nil {
		return nil
	}
	channelID := channel.ID
	return &channelID
}

func (s *Service) demoStart(daysFromNow int, localHour int) time.Time {
	location := time.FixedZone(demoOriginatorTZ, 5*60*60)
	now := s.currentTime().In(location)
	target := time.Date(now.Year(), now.Month(), now.Day(), localHour, 0, 0, 0, location).AddDate(0, 0, daysFromNow)
	if !target.After(now) {
		target = target.AddDate(0, 0, 1)
	}
	return target
}

func (s *Service) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func demoWorkspaceSlug(userID uuid.UUID) string {
	return demoWorkspaceSlugPrefix + strings.ReplaceAll(userID.String(), "-", "")
}

func stringPtr(value string) *string {
	return &value
}

func isCode(err error, code cerrors.Code) bool {
	appErr, ok := cerrors.AsAppError(err)
	return ok && appErr.Code == code
}

func wrapDemoWrite(message string, err error) error {
	if appErr, ok := cerrors.AsAppError(err); ok {
		return appErr
	}
	return cerrors.Internal(message, fmt.Errorf("demo seed: %w", err))
}
