package guestaccess

import (
	"context"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
)

type Checker struct {
	grants repository.GuestAccessRepository
}

func NewChecker(grants repository.GuestAccessRepository) *Checker {
	return &Checker{grants: grants}
}

// HasWorkspaceAccess reports whether the user holds an active NON-call-scoped
// guest grant. Call-scoped grants are excluded so a guest invited to a single
// call cannot pass workspace-content / channel-history checks (unified guest
// link).
func (c *Checker) HasWorkspaceAccess(ctx context.Context, workspaceID, userID uuid.UUID) (bool, error) {
	grants, err := c.activeGrants(ctx, workspaceID, userID)
	if err != nil {
		return false, err
	}
	for _, grant := range grants {
		if !grant.IsCallScoped() {
			return true, nil
		}
	}
	return false, nil
}

func (c *Checker) HasChannelAccess(ctx context.Context, workspaceID, channelID, userID uuid.UUID) (bool, error) {
	grants, err := c.activeGrants(ctx, workspaceID, userID)
	if err != nil {
		return false, err
	}
	for _, grant := range grants {
		if grant.AllowsChannel(channelID) {
			return true, nil
		}
	}
	return false, nil
}

// IsGuest reports whether the user holds ANY active guest grant (channel OR
// call scoped). Drives the forced-waiting rule and the is_guest participant
// flag.
func (c *Checker) IsGuest(ctx context.Context, workspaceID, userID uuid.UUID) (bool, error) {
	grants, err := c.activeGrants(ctx, workspaceID, userID)
	if err != nil {
		return false, err
	}
	return len(grants) > 0, nil
}

// HasCallAccess reports whether the user holds an active grant scoped to the
// given call.
func (c *Checker) HasCallAccess(ctx context.Context, workspaceID, callID, userID uuid.UUID) (bool, error) {
	grants, err := c.activeGrants(ctx, workspaceID, userID)
	if err != nil {
		return false, err
	}
	for _, grant := range grants {
		if grant.AllowsCall(callID) {
			return true, nil
		}
	}
	return false, nil
}

// EvaluateCallAccess loads the active grants ONCE and derives both the is-guest
// flag and whether the user holds a grant scoped to the given call. It collapses
// the IsGuest + HasCallAccess pair into a single grants query for the hot
// requireCallAccess guest branch (unified guest link). isGuest is true when the
// user holds ANY active grant (channel OR call scoped); hasCallAccess is true
// only when an active grant is scoped to callID.
func (c *Checker) EvaluateCallAccess(ctx context.Context, workspaceID, callID, userID uuid.UUID) (isGuest, hasCallAccess bool, err error) {
	grants, err := c.activeGrants(ctx, workspaceID, userID)
	if err != nil {
		return false, false, err
	}
	for _, grant := range grants {
		isGuest = true
		if grant.AllowsCall(callID) {
			hasCallAccess = true
			break
		}
	}
	return isGuest, hasCallAccess, nil
}

func (c *Checker) activeGrants(ctx context.Context, workspaceID, userID uuid.UUID) ([]entityGrant, error) {
	if c == nil || c.grants == nil {
		return nil, nil
	}
	grants, err := c.grants.ListActiveByUserWorkspace(ctx, userID, workspaceID, time.Now().UTC())
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok {
			return nil, appErr
		}
		return nil, cerrors.Internal("failed to load guest access grants", err)
	}
	result := make([]entityGrant, 0, len(grants))
	for _, grant := range grants {
		result = append(result, entityGrant{channelIDs: grant.ChannelIDs, callID: grant.CallID})
	}
	return result, nil
}

type entityGrant struct {
	channelIDs []uuid.UUID
	callID     *uuid.UUID
}

func (g entityGrant) IsCallScoped() bool { return g.callID != nil }

func (g entityGrant) AllowsChannel(channelID uuid.UUID) bool {
	if g.callID != nil {
		return false // call-scoped grants never confer channel access
	}
	if len(g.channelIDs) == 0 {
		return true
	}
	for _, allowed := range g.channelIDs {
		if allowed == channelID {
			return true
		}
	}
	return false
}

func (g entityGrant) AllowsCall(callID uuid.UUID) bool {
	return g.callID != nil && *g.callID == callID
}
