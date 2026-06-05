package chat

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
)

// fakeMentionsRepo decorates a fakeMessageRepo with the MentionsLister
// extension so we can drive Service.ListMentions through its narrow extension
// interface without touching the broader repository fake.
type fakeMentionsRepo struct {
	*fakeMessageRepo
	rows         []entity.MentionRow
	lastWorkspID uuid.UUID
	lastUserID   uuid.UUID
	lastLimit    int
}

func (r *fakeMentionsRepo) ListMentions(_ context.Context, workspaceID, userID uuid.UUID, limit int) ([]entity.MentionRow, error) {
	r.lastWorkspID = workspaceID
	r.lastUserID = userID
	r.lastLimit = limit
	return r.rows, nil
}

func TestListMentionsRejectsNonMembers(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	intruderID := uuid.New()

	channels := &fakeChannelRepo{}
	messages := &fakeMentionsRepo{fakeMessageRepo: &fakeMessageRepo{}}
	workspaces := &fakeWorkspaceRepo{}
	svc := NewService(channels, messages, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)

	if _, err := svc.ListMentions(ctx, workspaceID, intruderID, 50); !hasCode(err, cerrors.CodeForbidden) {
		t.Fatalf("ListMentions non-member error = %v, want FORBIDDEN", err)
	}
	if messages.lastLimit != 0 {
		t.Fatalf("repo.ListMentions called for non-member; got limit=%d", messages.lastLimit)
	}
}

func TestListMentionsForwardsToRepoAndClampsLimit(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()

	channels := &fakeChannelRepo{}
	messages := &fakeMentionsRepo{
		fakeMessageRepo: &fakeMessageRepo{},
		rows: []entity.MentionRow{
			{MessageID: uuid.New(), ChannelID: uuid.New(), Body: "@you ping"},
		},
	}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	svc := NewService(channels, messages, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)

	// Negative limit collapses to the 50-default.
	got, err := svc.ListMentions(ctx, workspaceID, userID, -1)
	if err != nil {
		t.Fatalf("ListMentions returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(got))
	}
	if messages.lastLimit != 50 {
		t.Fatalf("default limit = %d, want 50", messages.lastLimit)
	}
	if messages.lastWorkspID != workspaceID {
		t.Fatalf("workspace forwarded = %s, want %s", messages.lastWorkspID, workspaceID)
	}
	if messages.lastUserID != userID {
		t.Fatalf("user forwarded = %s, want %s", messages.lastUserID, userID)
	}

	// Over-cap limit gets clamped to 50, not silently passed through.
	if _, err := svc.ListMentions(ctx, workspaceID, userID, 9999); err != nil {
		t.Fatalf("ListMentions over-cap returned error: %v", err)
	}
	if messages.lastLimit != 50 {
		t.Fatalf("clamped limit = %d, want 50", messages.lastLimit)
	}

	// In-range limit forwarded verbatim.
	if _, err := svc.ListMentions(ctx, workspaceID, userID, 25); err != nil {
		t.Fatalf("ListMentions in-range returned error: %v", err)
	}
	if messages.lastLimit != 25 {
		t.Fatalf("in-range limit = %d, want 25", messages.lastLimit)
	}
}

func TestListMentionsReturnsEmptySliceForNilRepoResponse(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()

	channels := &fakeChannelRepo{}
	messages := &fakeMentionsRepo{fakeMessageRepo: &fakeMessageRepo{}, rows: nil}
	workspaces := &fakeWorkspaceRepo{members: map[[2]uuid.UUID]*entity.WorkspaceMember{
		{workspaceID, userID}: {WorkspaceID: workspaceID, UserID: userID, Role: entity.WorkspaceRoleMember},
	}}
	svc := NewService(channels, messages, workspaces, nil, noopPublisher{}, nil, nil, nil, nil)

	rows, err := svc.ListMentions(ctx, workspaceID, userID, 50)
	if err != nil {
		t.Fatalf("ListMentions returned error: %v", err)
	}
	if rows == nil {
		t.Fatal("ListMentions returned nil, want non-nil empty slice")
	}
	if len(rows) != 0 {
		t.Fatalf("len(rows) = %d, want 0", len(rows))
	}
}

func (r *fakeMentionsRepo) HardDelete(context.Context, uuid.UUID) error { return nil }
