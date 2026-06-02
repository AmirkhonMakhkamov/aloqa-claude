package guestaccess

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
)

type fakeGrantRepo struct {
	grants []entity.GuestAccessGrant
}

func (r *fakeGrantRepo) CreateGrant(context.Context, *entity.GuestAccessGrant) error { return nil }

func (r *fakeGrantRepo) ListActiveByUserWorkspace(_ context.Context, userID, workspaceID uuid.UUID, now time.Time) ([]entity.GuestAccessGrant, error) {
	var active []entity.GuestAccessGrant
	for _, g := range r.grants {
		if g.UserID == userID && g.WorkspaceID == workspaceID && g.ExpiresAt.After(now) {
			active = append(active, g)
		}
	}
	return active, nil
}

func TestCheckerCallScopedGrant(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	callID := uuid.New()
	otherCall := uuid.New()
	channelID := uuid.New()

	checker := NewChecker(&fakeGrantRepo{grants: []entity.GuestAccessGrant{{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		UserID:      userID,
		CallID:      &callID,
		ExpiresAt:   time.Now().Add(time.Hour),
	}}})

	if ok, err := checker.IsGuest(ctx, workspaceID, userID); err != nil || !ok {
		t.Fatalf("IsGuest = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := checker.HasCallAccess(ctx, workspaceID, callID, userID); err != nil || !ok {
		t.Fatalf("HasCallAccess(own) = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := checker.HasCallAccess(ctx, workspaceID, otherCall, userID); err != nil || ok {
		t.Fatalf("HasCallAccess(other) = (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := checker.HasChannelAccess(ctx, workspaceID, channelID, userID); err != nil || ok {
		t.Fatalf("HasChannelAccess = (%v, %v), want (false, nil) for call-scoped grant", ok, err)
	}
	if ok, err := checker.HasWorkspaceAccess(ctx, workspaceID, userID); err != nil || ok {
		t.Fatalf("HasWorkspaceAccess = (%v, %v), want (false, nil) for call-scoped grant", ok, err)
	}
}

func TestCheckerLegacyChannelGrantUnchanged(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	callID := uuid.New()
	channelID := uuid.New()
	otherChannel := uuid.New()

	checker := NewChecker(&fakeGrantRepo{grants: []entity.GuestAccessGrant{{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		UserID:      userID,
		ChannelIDs:  []uuid.UUID{channelID},
		ExpiresAt:   time.Now().Add(time.Hour),
	}}})

	if ok, err := checker.IsGuest(ctx, workspaceID, userID); err != nil || !ok {
		t.Fatalf("IsGuest legacy = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := checker.HasWorkspaceAccess(ctx, workspaceID, userID); err != nil || !ok {
		t.Fatalf("HasWorkspaceAccess legacy = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := checker.HasChannelAccess(ctx, workspaceID, channelID, userID); err != nil || !ok {
		t.Fatalf("HasChannelAccess(listed) = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := checker.HasChannelAccess(ctx, workspaceID, otherChannel, userID); err != nil || ok {
		t.Fatalf("HasChannelAccess(unlisted) = (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := checker.HasCallAccess(ctx, workspaceID, callID, userID); err != nil || ok {
		t.Fatalf("HasCallAccess legacy = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestCheckerLegacyEmptyChannelGrantAllowsAllChannels(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	channelID := uuid.New()

	checker := NewChecker(&fakeGrantRepo{grants: []entity.GuestAccessGrant{{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		UserID:      userID,
		ChannelIDs:  nil, // legacy "all channels"
		ExpiresAt:   time.Now().Add(time.Hour),
	}}})

	if ok, err := checker.HasWorkspaceAccess(ctx, workspaceID, userID); err != nil || !ok {
		t.Fatalf("HasWorkspaceAccess empty-legacy = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := checker.HasChannelAccess(ctx, workspaceID, channelID, userID); err != nil || !ok {
		t.Fatalf("HasChannelAccess empty-legacy = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestCheckerNoGrant(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()

	checker := NewChecker(&fakeGrantRepo{})
	if ok, err := checker.IsGuest(ctx, workspaceID, userID); err != nil || ok {
		t.Fatalf("IsGuest no-grant = (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := checker.HasWorkspaceAccess(ctx, workspaceID, userID); err != nil || ok {
		t.Fatalf("HasWorkspaceAccess no-grant = (%v, %v), want (false, nil)", ok, err)
	}
}
