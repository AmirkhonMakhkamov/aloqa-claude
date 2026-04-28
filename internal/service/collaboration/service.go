package collaboration

import (
	"context"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/security/rbac"
)

type Service struct {
	workspaces    repository.WorkspaceRepository
	collaboration repository.WorkspaceCollaborationRepository
	permissions   rbac.Checker
}

func NewService(workspaces repository.WorkspaceRepository, collaboration repository.WorkspaceCollaborationRepository, permissions rbac.Checker) *Service {
	return &Service{workspaces: workspaces, collaboration: collaboration, permissions: permissions}
}

func (s *Service) RequestConnection(ctx context.Context, sourceWorkspaceID, targetWorkspaceID, actorID uuid.UUID, policy entity.WorkspaceConnectionPolicy) (*entity.WorkspaceConnection, error) {
	if sourceWorkspaceID == targetWorkspaceID {
		return nil, cerrors.InvalidInput("cannot connect a workspace to itself")
	}
	if err := s.requirePermission(ctx, sourceWorkspaceID, actorID, rbac.PermissionCollaborationManage); err != nil {
		return nil, err
	}
	if err := validatePolicy(policy); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	connection := &entity.WorkspaceConnection{
		ID:                id.New(),
		SourceWorkspaceID: sourceWorkspaceID,
		TargetWorkspaceID: targetWorkspaceID,
		Status:            entity.WorkspaceConnectionPending,
		Policy:            policy,
		CreatedBy:         actorID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := s.collaboration.CreateConnection(ctx, connection); err != nil {
		return nil, cerrors.Internal("failed to create workspace connection", err)
	}
	return connection, nil
}

func (s *Service) ApproveConnection(ctx context.Context, sourceWorkspaceID, targetWorkspaceID, actorID uuid.UUID) error {
	if err := s.requirePermission(ctx, targetWorkspaceID, actorID, rbac.PermissionCollaborationManage); err != nil {
		return err
	}
	connection, err := s.collaboration.GetConnection(ctx, sourceWorkspaceID, targetWorkspaceID)
	if err != nil {
		return cerrors.NotFound("workspace connection not found")
	}
	if connection.Status != entity.WorkspaceConnectionPending {
		return cerrors.Conflict("workspace connection is not pending")
	}
	return s.collaboration.UpdateConnectionStatus(ctx, connection.ID, entity.WorkspaceConnectionActive, &actorID)
}

func (s *Service) CanContact(ctx context.Context, sourceWorkspaceID, targetWorkspaceID, sourceUserID, targetUserID uuid.UUID) error {
	connection, err := s.activeConnection(ctx, sourceWorkspaceID, targetWorkspaceID)
	if err != nil {
		return err
	}
	return s.authorizeContact(ctx, connection, sourceWorkspaceID, targetWorkspaceID, sourceUserID, targetUserID)
}

func (s *Service) CanShareChannel(ctx context.Context, sourceWorkspaceID, targetWorkspaceID, sourceUserID, targetUserID uuid.UUID) error {
	connection, err := s.activeConnection(ctx, sourceWorkspaceID, targetWorkspaceID)
	if err != nil {
		return err
	}
	if !connection.Policy.SharedChannels {
		return cerrors.Forbidden("workspace connection does not allow shared channels")
	}
	return s.authorizeContact(ctx, connection, sourceWorkspaceID, targetWorkspaceID, sourceUserID, targetUserID)
}

func (s *Service) CanShareCall(ctx context.Context, sourceWorkspaceID, targetWorkspaceID, sourceUserID, targetUserID uuid.UUID) error {
	connection, err := s.activeConnection(ctx, sourceWorkspaceID, targetWorkspaceID)
	if err != nil {
		return err
	}
	if !connection.Policy.SharedCalls {
		return cerrors.Forbidden("workspace connection does not allow shared calls")
	}
	return s.authorizeContact(ctx, connection, sourceWorkspaceID, targetWorkspaceID, sourceUserID, targetUserID)
}

func (s *Service) activeConnection(ctx context.Context, sourceWorkspaceID, targetWorkspaceID uuid.UUID) (*entity.WorkspaceConnection, error) {
	connection, err := s.collaboration.GetConnection(ctx, sourceWorkspaceID, targetWorkspaceID)
	if err != nil {
		return nil, cerrors.Forbidden("workspace connection is not active")
	}
	if connection.Status != entity.WorkspaceConnectionActive {
		return nil, cerrors.Forbidden("workspace connection is not active")
	}
	if connection.Policy.ExpiresAt != nil && !connection.Policy.ExpiresAt.After(time.Now().UTC()) {
		return nil, cerrors.Forbidden("workspace connection has expired")
	}
	return connection, nil
}

func (s *Service) authorizeContact(ctx context.Context, connection *entity.WorkspaceConnection, sourceWorkspaceID, targetWorkspaceID, sourceUserID, targetUserID uuid.UUID) error {
	sourceMember, err := s.workspaces.GetMember(ctx, sourceWorkspaceID, sourceUserID)
	if err != nil {
		return cerrors.Forbidden("source user is not a workspace member")
	}
	targetMember, err := s.workspaces.GetMember(ctx, targetWorkspaceID, targetUserID)
	if err != nil {
		return cerrors.Forbidden("target user is not a workspace member")
	}
	if !connection.Policy.CanContact(sourceMember.Role, targetMember.Role) {
		return cerrors.Forbidden("cross-workspace contact is not permitted by policy")
	}
	return nil
}

func (s *Service) requirePermission(ctx context.Context, workspaceID, userID uuid.UUID, permission rbac.Permission) error {
	if s.permissions != nil {
		return s.permissions.Require(ctx, workspaceID, userID, permission)
	}
	member, err := s.workspaces.GetMember(ctx, workspaceID, userID)
	if err != nil {
		return cerrors.Forbidden("user is not a workspace member")
	}
	if !rbac.Has(member.Role, permission) {
		return cerrors.Forbidden("workspace role does not have required permission")
	}
	return nil
}

func validatePolicy(policy entity.WorkspaceConnectionPolicy) error {
	switch policy.DirectoryVisibility {
	case entity.DirectoryVisibilityNone, entity.DirectoryVisibilityBasic, entity.DirectoryVisibilityDirectory, "":
	default:
		return cerrors.InvalidInput("invalid directory visibility")
	}
	switch policy.ContactPolicy {
	case entity.ContactPolicyNone, entity.ContactPolicyRoleBased, entity.ContactPolicyMutualAllow, entity.ContactPolicyOpen, "":
	default:
		return cerrors.InvalidInput("invalid contact policy")
	}
	if policy.ContactPolicy == entity.ContactPolicyRoleBased &&
		(len(policy.AllowedSourceRoles) == 0 || len(policy.AllowedTargetRoles) == 0) {
		return cerrors.InvalidInput("role-based contact policy requires source and target roles")
	}
	return nil
}
