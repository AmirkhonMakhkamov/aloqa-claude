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

// EvaluateCallAccess loads the active grants once and derives both the is-guest
// flag and per-call access in a single pass. A call-scoped grant yields isGuest
// true + hasCallAccess true for the granted call, and hasCallAccess false for
// another call. A legacy channel grant yields isGuest true + hasCallAccess false.
func TestCheckerEvaluateCallAccess(t *testing.T) {
	ctx := context.Background()
	workspaceID := uuid.New()
	userID := uuid.New()
	callID := uuid.New()
	otherCall := uuid.New()

	t.Run("call-scoped grant", func(t *testing.T) {
		checker := NewChecker(&fakeGrantRepo{grants: []entity.GuestAccessGrant{{
			ID:          uuid.New(),
			WorkspaceID: workspaceID,
			UserID:      userID,
			CallID:      &callID,
			ExpiresAt:   time.Now().Add(time.Hour),
		}}})

		isGuest, hasCall, err := checker.EvaluateCallAccess(ctx, workspaceID, callID, userID)
		if err != nil || !isGuest || !hasCall {
			t.Fatalf("EvaluateCallAccess(own) = (%v, %v, %v), want (true, true, nil)", isGuest, hasCall, err)
		}

		isGuest, hasCall, err = checker.EvaluateCallAccess(ctx, workspaceID, otherCall, userID)
		if err != nil || !isGuest || hasCall {
			t.Fatalf("EvaluateCallAccess(other) = (%v, %v, %v), want (true, false, nil)", isGuest, hasCall, err)
		}
	})

	t.Run("legacy channel grant", func(t *testing.T) {
		channelID := uuid.New()
		checker := NewChecker(&fakeGrantRepo{grants: []entity.GuestAccessGrant{{
			ID:          uuid.New(),
			WorkspaceID: workspaceID,
			UserID:      userID,
			ChannelIDs:  []uuid.UUID{channelID},
			ExpiresAt:   time.Now().Add(time.Hour),
		}}})

		isGuest, hasCall, err := checker.EvaluateCallAccess(ctx, workspaceID, callID, userID)
		if err != nil || !isGuest || hasCall {
			t.Fatalf("EvaluateCallAccess(legacy) = (%v, %v, %v), want (true, false, nil)", isGuest, hasCall, err)
		}
	})

	t.Run("no grant", func(t *testing.T) {
		checker := NewChecker(&fakeGrantRepo{})
		isGuest, hasCall, err := checker.EvaluateCallAccess(ctx, workspaceID, callID, userID)
		if err != nil || isGuest || hasCall {
			t.Fatalf("EvaluateCallAccess(no grant) = (%v, %v, %v), want (false, false, nil)", isGuest, hasCall, err)
		}
	})
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
