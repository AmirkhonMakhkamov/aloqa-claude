package accesspolicy

import (
	"context"
	"sort"
	"testing"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
)

func TestSelfChannelWorkspacePolicy(t *testing.T) {
	workspaceID := uuid.New()
	otherWorkspaceID := uuid.New()
	ownerID := uuid.New()
	otherUserID := uuid.New()
	savedChannelID := uuid.New()
	globalChannelID := uuid.New()

	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			savedChannelID: {
				ID:          savedChannelID,
				WorkspaceID: &workspaceID,
				Type:        entity.ChannelTypeSaved,
				OwnerUserID: &ownerID,
			},
			globalChannelID: {
				ID:          globalChannelID,
				WorkspaceID: nil,
				Type:        entity.ChannelTypeSavedGlobal,
				OwnerUserID: &ownerID,
			},
		},
	}
	workspaces := &fakeWorkspaceRepo{
		members: map[[2]uuid.UUID]*entity.WorkspaceMember{
			{workspaceID, ownerID}: {WorkspaceID: workspaceID, UserID: ownerID},
		},
	}
	checker := NewChecker(workspaces, channels, nil, nil)

	t.Run("saved channel owner allowed when workspace matches", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), middleware.WorkspaceIDKey, workspaceID)
		decision, err := checker.Channel(ctx, savedChannelID, ownerID, CapabilityParticipate)
		if err != nil {
			t.Fatalf("Channel returned error: %v", err)
		}
		if decision.Channel == nil || decision.Channel.ID != savedChannelID {
			t.Fatalf("decision channel = %+v, want %s", decision.Channel, savedChannelID)
		}
	})

	t.Run("saved channel workspace mismatch hides existence", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), middleware.WorkspaceIDKey, otherWorkspaceID)
		_, err := checker.Channel(ctx, savedChannelID, ownerID, CapabilityParticipate)
		requireCode(t, err, cerrors.CodeNotFound)
	})

	t.Run("saved channel non-owner hides existence", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), middleware.WorkspaceIDKey, workspaceID)
		_, err := checker.Channel(ctx, savedChannelID, otherUserID, CapabilityParticipate)
		requireCode(t, err, cerrors.CodeNotFound)
	})

	t.Run("saved_global owner allowed when user belongs to path workspace", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), middleware.WorkspaceIDKey, workspaceID)
		decision, err := checker.Channel(ctx, globalChannelID, ownerID, CapabilityParticipate)
		if err != nil {
			t.Fatalf("Channel returned error: %v", err)
		}
		if decision.Channel == nil || decision.Channel.ID != globalChannelID {
			t.Fatalf("decision channel = %+v, want %s", decision.Channel, globalChannelID)
		}
	})

	t.Run("saved_global owner without path workspace membership gets forbidden", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), middleware.WorkspaceIDKey, otherWorkspaceID)
		_, err := checker.Channel(ctx, globalChannelID, ownerID, CapabilityParticipate)
		requireCode(t, err, cerrors.CodeForbidden)
	})

	t.Run("saved_global non-owner hides existence", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), middleware.WorkspaceIDKey, workspaceID)
		_, err := checker.Channel(ctx, globalChannelID, otherUserID, CapabilityParticipate)
		requireCode(t, err, cerrors.CodeNotFound)
	})
}

func TestListChannelsReadsAllPaginatedWorkspaceChannels(t *testing.T) {
	workspaceID := uuid.New()
	userID := uuid.New()

	channels := &fakeChannelRepo{channels: map[uuid.UUID]*entity.Channel{}}
	for range 125 {
		channelID := uuid.New()
		channels.channels[channelID] = &entity.Channel{
			ID:          channelID,
			WorkspaceID: &workspaceID,
			Type:        entity.ChannelTypePublic,
		}
	}
	workspaces := &fakeWorkspaceRepo{
		members: map[[2]uuid.UUID]*entity.WorkspaceMember{
			{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID},
		},
	}
	checker := NewChecker(workspaces, channels, nil, nil)

	got, err := checker.ListChannels(context.Background(), workspaceID, userID, CapabilityView)
	if err != nil {
		t.Fatalf("ListChannels returned error: %v", err)
	}
	if len(got) != 125 {
		t.Fatalf("channels count = %d, want 125", len(got))
	}
}

func requireCode(t *testing.T, err error, code cerrors.Code) {
	t.Helper()
	appErr, ok := cerrors.AsAppError(err)
	if !ok || appErr.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

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
func (r *fakeWorkspaceRepo) Update(context.Context, *entity.Workspace) error { return nil }
func (r *fakeWorkspaceRepo) AddMember(context.Context, *entity.WorkspaceMember) error {
	return nil
}
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
func (r *fakeWorkspaceRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

type fakeChannelRepo struct {
	channels map[uuid.UUID]*entity.Channel
}

func (r *fakeChannelRepo) Create(context.Context, *entity.Channel) error { return nil }
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
	if p.Limit <= 0 && p.Cursor == uuid.Nil {
		return channels, nil
	}
	p.Normalize()
	page := make([]entity.Channel, 0, p.Limit+1)
	for _, ch := range channels {
		if p.Cursor != uuid.Nil && ch.ID.String() >= p.Cursor.String() {
			continue
		}
		page = append(page, ch)
		if len(page) >= p.Limit+1 {
			return page, nil
		}
	}
	return page, nil
}
func (r *fakeChannelRepo) ListByUser(context.Context, uuid.UUID, uuid.UUID) ([]entity.Channel, error) {
	return nil, nil
}
func (r *fakeChannelRepo) ListArchivedByUser(context.Context, uuid.UUID, uuid.UUID) ([]entity.ArchivedChannelInfo, error) {
	return nil, nil
}
func (r *fakeChannelRepo) Update(context.Context, *entity.Channel) error { return nil }
func (r *fakeChannelRepo) Archive(context.Context, uuid.UUID) error      { return nil }
func (r *fakeChannelRepo) AddMember(context.Context, *entity.ChannelMember) error {
	return nil
}
func (r *fakeChannelRepo) GetMember(context.Context, uuid.UUID, uuid.UUID) (*entity.ChannelMember, error) {
	return nil, cerrors.NotFound("channel member not found")
}
func (r *fakeChannelRepo) ListMembers(context.Context, uuid.UUID) ([]entity.ChannelMember, error) {
	return nil, nil
}
func (r *fakeChannelRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *fakeChannelRepo) UpdateLastRead(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *fakeChannelRepo) GetDMChannel(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*entity.Channel, error) {
	return nil, cerrors.NotFound("dm channel not found")
}
