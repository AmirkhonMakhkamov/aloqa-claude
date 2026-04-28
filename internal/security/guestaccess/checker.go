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

func (c *Checker) HasWorkspaceAccess(ctx context.Context, workspaceID, userID uuid.UUID) (bool, error) {
	grants, err := c.activeGrants(ctx, workspaceID, userID)
	if err != nil {
		return false, err
	}
	return len(grants) > 0, nil
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
		result = append(result, entityGrant{channelIDs: grant.ChannelIDs})
	}
	return result, nil
}

type entityGrant struct {
	channelIDs []uuid.UUID
}

func (g entityGrant) AllowsChannel(channelID uuid.UUID) bool {
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
