package search

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
	"aloqa/internal/security/accesspolicy"
	"aloqa/internal/security/guestaccess"
)

func TestSearchRequiresUserWorkspaceAndChannelAccess(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	privateChannelID := uuid.New()
	memberID := uuid.New()
	intruderID := uuid.New()

	searcher := &capturingSearcher{}
	svc := NewService(nil, searcher,
		&fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
			{workspaceID, memberID}: {WorkspaceID: workspaceID, UserID: memberID, Role: entity.WorkspaceRoleMember},
		}},
		&fakeChannelRepo{
			channels: map[uuid.UUID]*entity.Channel{
				privateChannelID: {ID: privateChannelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePrivate},
			},
			members: map[[2]uuid.UUID]*entity.ChannelMember{
				{privateChannelID, memberID}: {ChannelID: privateChannelID, UserID: memberID, Role: entity.ChannelRoleMember},
			},
		},
		nil,
	)

	if _, err := svc.Search(ctx, Params{WorkspaceID: workspaceID, Query: "secret"}); !hasCode(err, cerrors.CodeUnauthorized) {
		t.Fatalf("Search without user error = %v, want UNAUTHORIZED", err)
	}

	if _, err := svc.Search(ctx, Params{
		WorkspaceID: workspaceID,
		ChannelID:   &privateChannelID,
		UserID:      &intruderID,
		Query:       "secret",
	}); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("Search private channel intruder error = %v, want FORBIDDEN", err)
	}
	if searcher.called {
		t.Fatalf("search backend should not be called when access is denied")
	}

	if _, err := svc.Search(ctx, Params{
		WorkspaceID: workspaceID,
		ChannelID:   &privateChannelID,
		UserID:      &memberID,
		Query:       "secret",
	}); err != nil {
		t.Fatalf("Search member returned error: %v", err)
	}
	if !searcher.called {
		t.Fatalf("search backend was not called for authorized search")
	}
	if searcher.params.Limit != 20 {
		t.Fatalf("default limit = %d, want 20", searcher.params.Limit)
	}
}

func TestSearchGuestScopeUsesAccessibleChannelsOnly(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	privateChannelID := uuid.New()
	guestID := uuid.New()

	searcher := &capturingSearcher{}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{}}
	channels := &fakeChannelRepo{
		channels: map[uuid.UUID]*entity.Channel{
			privateChannelID: {ID: privateChannelID, WorkspaceID: &workspaceID, Type: entity.ChannelTypePrivate},
		},
		members: map[[2]uuid.UUID]*entity.ChannelMember{},
	}
	guests := guestaccess.NewChecker(&fakeGuestAccessRepo{grants: []entity.GuestAccessGrant{{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		UserID:      guestID,
		ChannelIDs:  []uuid.UUID{privateChannelID},
		ExpiresAt:   time.Now().Add(time.Hour),
	}}})
	svc := NewService(nil, searcher, workspaces, channels, nil)
	svc.SetAccessPolicy(accesspolicy.NewChecker(workspaces, channels, guests, nil))

	if _, err := svc.Search(ctx, Params{
		WorkspaceID: workspaceID,
		UserID:      &guestID,
		Query:       "secret",
	}); err != nil {
		t.Fatalf("Search guest returned error: %v", err)
	}
	if len(searcher.params.AccessibleChannelIDs) != 1 || searcher.params.AccessibleChannelIDs[0] != privateChannelID {
		t.Fatalf("accessible channels = %+v, want [%s]", searcher.params.AccessibleChannelIDs, privateChannelID)
	}
	if searcher.params.AllowUserResults {
		t.Fatalf("guest search should not enable workspace user results")
	}
}

func TestSearchIncludesUserResultsForWorkspaceMembers(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	requesterID := uuid.New()
	resultUserID := uuid.New()

	for _, role := range []entity.WorkspaceRole{
		entity.WorkspaceRoleMember,
		entity.WorkspaceRoleAdmin,
		entity.WorkspaceRoleOwner,
	} {
		t.Run(string(role), func(t *testing.T) {
			searcher := &capturingSearcher{
				results: &SearchResults{
					Results: []Result{{
						Type:        string(ResourceTypeUser),
						ID:          resultUserID,
						WorkspaceID: workspaceID,
						Title:       "Bob Builder",
						Snippet:     "<mark>Bob</mark> Builder",
						Score:       1,
						CreatedAt:   time.Now(),
						UpdatedAt:   time.Now(),
						User: &entity.User{
							ID:          resultUserID,
							Email:       "bob@example.com",
							DisplayName: "Bob Builder",
							Status:      entity.UserStatusActive,
						},
					}},
					Total: 1,
				},
			}
			workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
				{workspaceID, requesterID}: {WorkspaceID: workspaceID, UserID: requesterID, Role: role},
			}}
			channels := &fakeChannelRepo{
				channels: map[uuid.UUID]*entity.Channel{},
				members:  map[[2]uuid.UUID]*entity.ChannelMember{},
			}
			svc := NewService(nil, searcher, workspaces, channels, nil)
			svc.SetAccessPolicy(accesspolicy.NewChecker(workspaces, channels, nil, nil))

			results, err := svc.Search(ctx, Params{
				WorkspaceID: workspaceID,
				UserID:      &requesterID,
				Query:       "Bob",
				Type:        string(ResourceTypeUser),
				Limit:       5,
			})
			if err != nil {
				t.Fatalf("Search returned error: %v", err)
			}
			if !searcher.params.AllowUserResults {
				t.Fatalf("AllowUserResults = false, want true for workspace role %s", role)
			}
			if len(results.Results) != 1 {
				t.Fatalf("result count = %d, want 1", len(results.Results))
			}
			result := results.Results[0]
			if result.Type != string(ResourceTypeUser) || result.ID != resultUserID {
				t.Fatalf("result = %+v, want user %s", result, resultUserID)
			}
			if result.User == nil || result.User.DisplayName != "Bob Builder" {
				t.Fatalf("result user = %+v, want hydrated Bob user", result.User)
			}
		})
	}
}

type capturingSearcher struct {
	called  bool
	params  Params
	results *SearchResults
}

func (s *capturingSearcher) Search(_ context.Context, params Params) (*SearchResults, error) {
	s.called = true
	s.params = params
	if s.results != nil {
		return s.results, nil
	}
	return &SearchResults{}, nil
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

type fakeChannelRepo struct {
	channels map[uuid.UUID]*entity.Channel
	members  map[[2]uuid.UUID]*entity.ChannelMember
}

func (r *fakeChannelRepo) Create(context.Context, *entity.Channel) error { return nil }
func (r *fakeChannelRepo) GetByID(_ context.Context, id uuid.UUID) (*entity.Channel, error) {
	if ch := r.channels[id]; ch != nil {
		return ch, nil
	}
	return nil, cerrors.NotFound("channel not found")
}
func (r *fakeChannelRepo) ListByWorkspace(_ context.Context, workspaceID uuid.UUID, _ pagination.Params) ([]entity.Channel, error) {
	var channels []entity.Channel
	for _, ch := range r.channels {
		if ch.WorkspaceID != nil && *ch.WorkspaceID == workspaceID {
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
		if ch := r.channels[key[0]]; ch != nil && ch.WorkspaceID != nil && *ch.WorkspaceID == workspaceID {
			channels = append(channels, *ch)
		}
	}
	return channels, nil
}
func (r *fakeChannelRepo) Update(context.Context, *entity.Channel) error          { return nil }
func (r *fakeChannelRepo) Archive(context.Context, uuid.UUID) error               { return nil }
func (r *fakeChannelRepo) AddMember(context.Context, *entity.ChannelMember) error { return nil }
func (r *fakeChannelRepo) GetMember(_ context.Context, channelID, userID uuid.UUID) (*entity.ChannelMember, error) {
	if member := r.members[[2]uuid.UUID{channelID, userID}]; member != nil {
		return member, nil
	}
	return nil, cerrors.NotFound("channel member not found")
}
func (r *fakeChannelRepo) ListMembers(context.Context, uuid.UUID) ([]entity.ChannelMember, error) {
	return nil, nil
}
func (r *fakeChannelRepo) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (r *fakeChannelRepo) UpdateLastRead(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *fakeChannelRepo) GetDMChannel(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*entity.Channel, error) {
	return nil, cerrors.NotFound("dm channel not found")
}

func hasCode(err error, code cerrors.Code) bool {
	appErr, ok := cerrors.AsAppError(err)
	return ok && appErr.Code == code
}
