package collabaccess

import (
	"context"

	"github.com/google/uuid"

	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/service/collaboration"
)

type Decision struct {
	Managed bool
	Allowed bool
}

type Checker struct {
	grants        repository.ChannelAccessGrantRepository
	collaboration *collaboration.Service
}

func NewChecker(grants repository.ChannelAccessGrantRepository, collaboration *collaboration.Service) *Checker {
	return &Checker{grants: grants, collaboration: collaboration}
}

func (c *Checker) AuthorizeChannel(ctx context.Context, channelID, userID uuid.UUID) (Decision, error) {
	return c.authorize(ctx, channelID, userID, false)
}

func (c *Checker) AuthorizeCall(ctx context.Context, channelID, userID uuid.UUID) (Decision, error) {
	return c.authorize(ctx, channelID, userID, true)
}

func (c *Checker) authorize(ctx context.Context, channelID, userID uuid.UUID, requireCalls bool) (Decision, error) {
	if c == nil || c.grants == nil || c.collaboration == nil {
		return Decision{}, nil
	}

	grants, err := c.grants.ListByChannel(ctx, channelID)
	if err != nil {
		return Decision{}, cerrors.Internal("failed to verify collaboration access", err)
	}
	if len(grants) == 0 {
		return Decision{}, nil
	}

	decision := Decision{Managed: true}
	for _, grant := range grants {
		if userID != grant.UserID && userID != grant.SourceUserID {
			continue
		}

		if requireCalls && !grant.AllowCalls {
			continue
		}

		var authErr error
		if requireCalls {
			authErr = c.collaboration.CanShareCall(ctx, grant.WorkspaceID, grant.RemoteWorkspaceID, grant.SourceUserID, grant.UserID)
		} else {
			authErr = c.collaboration.CanShareChannel(ctx, grant.WorkspaceID, grant.RemoteWorkspaceID, grant.SourceUserID, grant.UserID)
		}
		if authErr == nil {
			decision.Allowed = true
			return decision, nil
		}

		appErr, ok := cerrors.AsAppError(authErr)
		if !ok || (appErr.Code != cerrors.CodeForbidden && appErr.Code != cerrors.CodeNotFound) {
			return Decision{}, authErr
		}
	}

	return decision, nil
}
