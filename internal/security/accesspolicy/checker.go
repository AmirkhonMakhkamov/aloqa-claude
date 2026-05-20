package accesspolicy

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/pagination"
	"aloqa/internal/security/collabaccess"
)

type Subject string

const (
	SubjectMember       Subject = "member"
	SubjectGuest        Subject = "guest"
	SubjectCollaborator Subject = "collaborator"
)

type Capability string

const (
	CapabilityView        Capability = "view"
	CapabilityParticipate Capability = "participate"
)

type GuestAccess interface {
	HasWorkspaceAccess(ctx context.Context, workspaceID, userID uuid.UUID) (bool, error)
	HasChannelAccess(ctx context.Context, workspaceID, channelID, userID uuid.UUID) (bool, error)
}

type CollaborationAccess interface {
	AuthorizeChannel(ctx context.Context, channelID, userID uuid.UUID) (collabaccess.Decision, error)
}

type ChannelDecision struct {
	Subject         Subject
	Channel         *entity.Channel
	WorkspaceMember *entity.WorkspaceMember
	ChannelMember   *entity.ChannelMember
}

type Checker struct {
	workspaces repository.WorkspaceRepository
	channels   repository.ChannelRepository
	guests     GuestAccess
	collab     CollaborationAccess
}

func NewChecker(
	workspaces repository.WorkspaceRepository,
	channels repository.ChannelRepository,
	guests GuestAccess,
	collab CollaborationAccess,
) *Checker {
	return &Checker{
		workspaces: workspaces,
		channels:   channels,
		guests:     guests,
		collab:     collab,
	}
}

func (c *Checker) WorkspaceAccess(ctx context.Context, workspaceID, userID uuid.UUID) (Subject, error) {
	if c == nil {
		return "", cerrors.Forbidden("workspace access is not configured")
	}
	_, err := c.workspaceMember(ctx, workspaceID, userID)
	if err == nil {
		return SubjectMember, nil
	}
	if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeForbidden {
		return "", err
	}
	if c.guests != nil {
		allowed, guestErr := c.guests.HasWorkspaceAccess(ctx, workspaceID, userID)
		if guestErr != nil {
			return "", guestErr
		}
		if allowed {
			return SubjectGuest, nil
		}
	}
	return "", cerrors.Forbidden("user does not have access to this workspace")
}

func (c *Checker) Channel(ctx context.Context, channelID, userID uuid.UUID, capability Capability) (*ChannelDecision, error) {
	if c == nil {
		return nil, cerrors.Forbidden("channel access is not configured")
	}

	ch, err := c.channels.GetByID(ctx, channelID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.NotFound("channel not found")
		}
		return nil, cerrors.Internal("failed to get channel", err)
	}
	if ch.Archived {
		return nil, cerrors.Forbidden("channel is archived")
	}
	if ch.WorkspaceID == nil {
		return nil, cerrors.NotFound("channel not found")
	}

	member, memberErr := c.workspaceMember(ctx, *ch.WorkspaceID, userID)
	if memberErr == nil {
		return c.channelDecisionForWorkspaceMember(ctx, ch, member, userID, capability)
	}
	if appErr, ok := cerrors.AsAppError(memberErr); !ok || appErr.Code != cerrors.CodeForbidden {
		return nil, memberErr
	}

	if c.guests != nil {
		allowed, err := c.guests.HasChannelAccess(ctx, *ch.WorkspaceID, ch.ID, userID)
		if err != nil {
			return nil, err
		}
		if allowed {
			return &ChannelDecision{
				Subject: SubjectGuest,
				Channel: ch,
			}, nil
		}
	}

	return c.channelDecisionForCollaborator(ctx, ch, userID)
}

func (c *Checker) ListChannels(ctx context.Context, workspaceID, userID uuid.UUID, capability Capability) ([]entity.Channel, error) {
	if c == nil {
		return nil, cerrors.Forbidden("channel access is not configured")
	}

	if capability == CapabilityParticipate {
		if _, err := c.workspaceMember(ctx, workspaceID, userID); err == nil {
			channels, err := c.channels.ListByUser(ctx, workspaceID, userID)
			if err != nil {
				return nil, cerrors.Internal("failed to list user channels", err)
			}
			filtered := make([]entity.Channel, 0, len(channels))
			for _, ch := range channels {
				decision, err := c.Channel(ctx, ch.ID, userID, capability)
				if err == nil && decision != nil && decision.Channel != nil {
					filtered = append(filtered, *decision.Channel)
				}
			}
			return dedupeChannels(filtered), nil
		}
	}

	var (
		cursor uuid.UUID
		all    []entity.Channel
	)
	for {
		page, err := c.channels.ListByWorkspace(ctx, workspaceID, pagination.Params{Cursor: cursor, Limit: 200})
		if err != nil {
			return nil, cerrors.Internal("failed to list workspace channels", err)
		}
		for _, ch := range page {
			decision, err := c.Channel(ctx, ch.ID, userID, capability)
			if err == nil && decision != nil && decision.Channel != nil {
				all = append(all, *decision.Channel)
			} else if err != nil {
				if appErr, ok := cerrors.AsAppError(err); ok && (appErr.Code == cerrors.CodeForbidden || appErr.Code == cerrors.CodeNotFound) {
					continue
				}
				return nil, err
			}
		}
		if len(page) < 200 {
			break
		}
		cursor = page[len(page)-1].ID
	}
	return dedupeChannels(all), nil
}

func (c *Checker) workspaceMember(ctx context.Context, workspaceID, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	if c.workspaces == nil {
		return nil, cerrors.Forbidden("workspace access is not configured")
	}
	member, err := c.workspaces.GetMember(ctx, workspaceID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.Forbidden("user is not a workspace member")
		}
		slog.ErrorContext(ctx, "failed to verify workspace membership", "workspace_id", workspaceID, "user_id", userID, "error", err)
		return nil, cerrors.Internal("failed to verify workspace membership", err)
	}
	return member, nil
}

func (c *Checker) channelDecisionForWorkspaceMember(
	ctx context.Context,
	ch *entity.Channel,
	member *entity.WorkspaceMember,
	userID uuid.UUID,
	capability Capability,
) (*ChannelDecision, error) {
	if ch == nil {
		return nil, cerrors.NotFound("channel not found")
	}
	channelMember, err := c.channels.GetMember(ctx, ch.ID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
			return nil, cerrors.Internal("failed to verify channel membership", err)
		}
		channelMember = nil
	}

	switch ch.Type {
	case entity.ChannelTypePublic:
		if capability == CapabilityParticipate && channelMember == nil {
			return nil, cerrors.Forbidden("user is not a member of this channel")
		}
		return &ChannelDecision{
			Subject:         SubjectMember,
			Channel:         ch,
			WorkspaceMember: member,
			ChannelMember:   channelMember,
		}, nil
	case entity.ChannelTypePrivate, entity.ChannelTypeDM, entity.ChannelTypeGroupDM:
		if channelMember == nil {
			return nil, cerrors.Forbidden("you do not have access to this channel")
		}
		if ch.Type == entity.ChannelTypeDM || ch.Type == entity.ChannelTypeGroupDM {
			allowed, err := c.authorizeCollaboration(ctx, ch.ID, userID)
			if err != nil {
				return nil, err
			}
			if !allowed {
				return nil, cerrors.Forbidden("you do not have access to this collaboration channel")
			}
		}
		return &ChannelDecision{
			Subject:         SubjectMember,
			Channel:         ch,
			WorkspaceMember: member,
			ChannelMember:   channelMember,
		}, nil
	default:
		return nil, cerrors.Forbidden("unsupported channel type")
	}
}

func (c *Checker) channelDecisionForCollaborator(ctx context.Context, ch *entity.Channel, userID uuid.UUID) (*ChannelDecision, error) {
	if ch.Type != entity.ChannelTypeDM && ch.Type != entity.ChannelTypeGroupDM {
		return nil, cerrors.Forbidden("you do not have access to this channel")
	}
	channelMember, err := c.channels.GetMember(ctx, ch.ID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.Forbidden("you do not have access to this channel")
		}
		return nil, cerrors.Internal("failed to verify channel membership", err)
	}
	allowed, err := c.authorizeCollaboration(ctx, ch.ID, userID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, cerrors.Forbidden("you do not have access to this collaboration channel")
	}
	return &ChannelDecision{
		Subject:       SubjectCollaborator,
		Channel:       ch,
		ChannelMember: channelMember,
	}, nil
}

func (c *Checker) authorizeCollaboration(ctx context.Context, channelID, userID uuid.UUID) (bool, error) {
	if c.collab == nil {
		return true, nil
	}
	decision, err := c.collab.AuthorizeChannel(ctx, channelID, userID)
	if err != nil {
		return false, err
	}
	if !decision.Managed {
		return true, nil
	}
	return decision.Allowed, nil
}

func dedupeChannels(channels []entity.Channel) []entity.Channel {
	if len(channels) == 0 {
		return nil
	}
	result := make([]entity.Channel, 0, len(channels))
	seen := make(map[uuid.UUID]struct{}, len(channels))
	for _, ch := range channels {
		if _, ok := seen[ch.ID]; ok {
			continue
		}
		seen[ch.ID] = struct{}{}
		result = append(result, ch)
	}
	return result
}
