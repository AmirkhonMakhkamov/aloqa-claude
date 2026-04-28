package admin

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/pkg/pagination"
	"aloqa/internal/platform/txscope"
	"aloqa/internal/security/rbac"
	searchsvc "aloqa/internal/service/search"
)

// Service provides workspace administration operations.
type Service struct {
	users       repository.UserRepository
	workspaces  repository.WorkspaceRepository
	roles       repository.WorkspaceRoleRepository
	channels    repository.ChannelRepository
	recordings  repository.RecordingRepository
	audit       repository.AuditRepository
	permissions rbac.Checker
	search      interface {
		IndexUser(ctx context.Context, workspaceID, userID uuid.UUID, displayName, email string, createdAt, updatedAt time.Time) error
		DeleteUserFromWorkspace(ctx context.Context, workspaceID, userID uuid.UUID) error
	}
	media interface {
		ListNodes(ctx context.Context) ([]entity.MediaNodeSnapshot, error)
		GetWorkspaceTopology(ctx context.Context, workspaceID uuid.UUID) (*entity.WorkspaceMediaTopology, error)
		GetCallQoSHistory(ctx context.Context, workspaceID, callID uuid.UUID, limit int) (*entity.CallQoSHistory, error)
		GetCallQualityReport(ctx context.Context, workspaceID, callID uuid.UUID, limit int) (*entity.CallQualityReport, error)
		GetCallQualityPolicy(ctx context.Context, workspaceID, callID uuid.UUID) (*entity.MediaQualityPolicy, error)
		UpdateCallQualityPolicy(ctx context.Context, policy *entity.MediaQualityPolicy) (*entity.MediaQualityPolicy, error)
		ListQualityAlerts(ctx context.Context, workspaceID, callID uuid.UUID, limit int) ([]entity.MediaQualityAlert, error)
	}
	storage interface {
		RuntimeReport(ctx context.Context) (*entity.StorageRuntimeReport, error)
		Audit(ctx context.Context) (*entity.StorageAuditReport, error)
	}
	observability interface {
		Dashboard(ctx context.Context) (*entity.ObservabilityDashboard, error)
		Alerts(ctx context.Context) ([]entity.OperationalAlert, error)
		SLOs(ctx context.Context) ([]entity.ObservabilitySLO, error)
		Metrics(ctx context.Context) (string, error)
	}
	tx txscope.Manager
}

type InviteMemberInput struct {
	Email string
	Role  entity.WorkspaceRole
}

type RoleDefinitionInput struct {
	Name        string
	BaseRole    entity.WorkspaceRole
	Permissions []string
}

// NewService creates a new admin service.
func NewService(
	users repository.UserRepository,
	workspaces repository.WorkspaceRepository,
	roles repository.WorkspaceRoleRepository,
	channels repository.ChannelRepository,
	recordings repository.RecordingRepository,
	audit repository.AuditRepository,
	permissions rbac.Checker,
	search interface {
		IndexUser(ctx context.Context, workspaceID, userID uuid.UUID, displayName, email string, createdAt, updatedAt time.Time) error
		DeleteUserFromWorkspace(ctx context.Context, workspaceID, userID uuid.UUID) error
	},
) *Service {
	return &Service{
		users:       users,
		workspaces:  workspaces,
		roles:       roles,
		channels:    channels,
		recordings:  recordings,
		audit:       audit,
		permissions: permissions,
		search:      search,
	}
}

func (s *Service) SetMediaObserver(media interface {
	ListNodes(ctx context.Context) ([]entity.MediaNodeSnapshot, error)
	GetWorkspaceTopology(ctx context.Context, workspaceID uuid.UUID) (*entity.WorkspaceMediaTopology, error)
	GetCallQoSHistory(ctx context.Context, workspaceID, callID uuid.UUID, limit int) (*entity.CallQoSHistory, error)
	GetCallQualityReport(ctx context.Context, workspaceID, callID uuid.UUID, limit int) (*entity.CallQualityReport, error)
	GetCallQualityPolicy(ctx context.Context, workspaceID, callID uuid.UUID) (*entity.MediaQualityPolicy, error)
	UpdateCallQualityPolicy(ctx context.Context, policy *entity.MediaQualityPolicy) (*entity.MediaQualityPolicy, error)
	ListQualityAlerts(ctx context.Context, workspaceID, callID uuid.UUID, limit int) ([]entity.MediaQualityAlert, error)
}) {
	s.media = media
}

func (s *Service) SetStorageObserver(storage interface {
	RuntimeReport(ctx context.Context) (*entity.StorageRuntimeReport, error)
	Audit(ctx context.Context) (*entity.StorageAuditReport, error)
}) {
	s.storage = storage
}

func (s *Service) SetObservabilityObserver(observer interface {
	Dashboard(ctx context.Context) (*entity.ObservabilityDashboard, error)
	Alerts(ctx context.Context) ([]entity.OperationalAlert, error)
	SLOs(ctx context.Context) ([]entity.ObservabilitySLO, error)
	Metrics(ctx context.Context) (string, error)
}) {
	s.observability = observer
}

func (s *Service) SetTransactionManager(manager txscope.Manager) {
	s.tx = manager
}

func (s *Service) requirePermission(ctx context.Context, workspaceID, userID uuid.UUID, permission rbac.Permission) error {
	if err := s.ensureOrganizationWorkspace(ctx, workspaceID); err != nil {
		return err
	}
	if s.permissions != nil {
		return s.permissions.Require(ctx, workspaceID, userID, permission)
	}
	member, err := s.member(ctx, workspaceID, userID)
	if err != nil {
		return err
	}
	if !rbac.Has(member.Role, permission) {
		return cerrors.Forbidden("workspace role does not have required permission")
	}
	return nil
}

func (s *Service) ensureOrganizationWorkspace(ctx context.Context, workspaceID uuid.UUID) error {
	workspace, err := s.workspaces.GetByID(ctx, workspaceID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return cerrors.NotFound("workspace not found")
		}
		return cerrors.Internal("failed to load workspace", err)
	}
	if isPersonalWorkspace(workspace) {
		return cerrors.Forbidden("personal spaces do not support workspace administration")
	}
	return nil
}

func (s *Service) member(ctx context.Context, workspaceID, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	member, err := s.workspaces.GetMember(ctx, workspaceID, userID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.Forbidden("not a workspace member")
		}
		return nil, cerrors.Internal("failed to get workspace membership", err)
	}
	return member, nil
}

func isPersonalWorkspace(workspace *entity.Workspace) bool {
	if workspace == nil || workspace.CreatedBy == uuid.Nil {
		return false
	}
	return workspace.Slug == personalWorkspaceSlug(workspace.CreatedBy)
}

func personalWorkspaceSlug(userID uuid.UUID) string {
	return "personal-" + strings.ReplaceAll(userID.String(), "-", "")
}

// ListMembers returns paginated workspace members.
func (s *Service) ListMembers(ctx context.Context, workspaceID, actorID uuid.UUID, p pagination.Params) ([]entity.WorkspaceMember, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionMemberRead); err != nil {
		return nil, err
	}
	return s.workspaces.ListMembers(ctx, workspaceID, p)
}

func (s *Service) InviteMember(ctx context.Context, workspaceID, actorID uuid.UUID, input InviteMemberInput) (*entity.WorkspaceMember, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionMemberInvite); err != nil {
		return nil, err
	}

	email := strings.TrimSpace(strings.ToLower(input.Email))
	if email == "" {
		return nil, cerrors.InvalidInput("email is required")
	}

	role := input.Role
	if role == "" {
		role = entity.WorkspaceRoleMember
	}
	switch role {
	case entity.WorkspaceRoleOwner, entity.WorkspaceRoleAdmin, entity.WorkspaceRoleMember, entity.WorkspaceRoleGuest:
	default:
		return nil, cerrors.InvalidInput("invalid role")
	}

	actor, err := s.member(ctx, workspaceID, actorID)
	if err != nil {
		return nil, err
	}
	if (role == entity.WorkspaceRoleAdmin || role == entity.WorkspaceRoleOwner) && actor.Role != entity.WorkspaceRoleOwner {
		return nil, cerrors.Forbidden("only owners can invite admins or owners")
	}

	user, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeNotFound {
			return nil, cerrors.NotFound("user not found")
		}
		return nil, cerrors.Internal("failed to lookup user", err)
	}
	if user.Status != entity.UserStatusActive {
		return nil, cerrors.Forbidden("user account is not active")
	}
	if _, err := s.workspaces.GetMember(ctx, workspaceID, user.ID); err == nil {
		return nil, cerrors.AlreadyExists("user is already a member of this workspace")
	} else if appErr, ok := cerrors.AsAppError(err); !ok || appErr.Code != cerrors.CodeNotFound {
		return nil, cerrors.Internal("failed to verify existing membership", err)
	}

	member := &entity.WorkspaceMember{
		ID:          id.New(),
		WorkspaceID: workspaceID,
		UserID:      user.ID,
		Role:        role,
		JoinedAt:    time.Now().UTC(),
	}
	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			workspaceRepo := s.workspaces
			if scope.Workspaces() != nil {
				workspaceRepo = scope.Workspaces()
			}
			if err := workspaceRepo.AddMember(ctx, member); err != nil {
				return err
			}
			if err := s.syncUserSearchDocsTx(ctx, scope, user, false); err != nil {
				return err
			}
			return s.createAuditTx(ctx, scope, workspaceID, actorID, entity.AuditActionMemberInvited, "user", user.ID.String(), map[string]any{
				"email": email,
				"role":  string(role),
			})
		}); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return nil, appErr
			}
			return nil, cerrors.Internal("failed to add member", err)
		}
	} else {
		if err := s.workspaces.AddMember(ctx, member); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return nil, appErr
			}
			return nil, cerrors.Internal("failed to add member", err)
		}
		if s.search != nil {
			if err := s.search.IndexUser(ctx, workspaceID, user.ID, user.DisplayName, user.Email, user.CreatedAt, user.UpdatedAt); err != nil {
				slog.ErrorContext(ctx, "failed to enqueue invited member search index", "workspace_id", workspaceID, "user_id", user.ID, "error", err)
			}
		}

		s.logAudit(ctx, workspaceID, actorID, entity.AuditActionMemberInvited, "user", user.ID.String(), map[string]any{
			"email": email,
			"role":  string(role),
		})
	}
	return member, nil
}

// UpdateMemberRole changes a member's role. Only owners can promote to admin.
func (s *Service) UpdateMemberRole(ctx context.Context, workspaceID, actorID, targetUserID uuid.UUID, newRole entity.WorkspaceRole) error {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionMemberManage); err != nil {
		return err
	}

	actor, err := s.member(ctx, workspaceID, actorID)
	if err != nil {
		return err
	}

	// Only owners can promote to admin or owner.
	if (newRole == entity.WorkspaceRoleAdmin || newRole == entity.WorkspaceRoleOwner) && actor.Role != entity.WorkspaceRoleOwner {
		return cerrors.Forbidden("only owners can promote to admin or owner")
	}

	// Prevent self-demotion of the last owner.
	if actorID == targetUserID && actor.Role == entity.WorkspaceRoleOwner && newRole != entity.WorkspaceRoleOwner {
		return cerrors.Forbidden("cannot demote yourself as the last owner")
	}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			workspaceRepo := s.workspaces
			if scope.Workspaces() != nil {
				workspaceRepo = scope.Workspaces()
			}
			if err := workspaceRepo.UpdateMemberRole(ctx, workspaceID, targetUserID, newRole); err != nil {
				return err
			}
			return s.createAuditTx(ctx, scope, workspaceID, actorID, entity.AuditActionRoleChanged, "user", targetUserID.String(), map[string]any{
				"new_role": string(newRole),
			})
		}); err != nil {
			return cerrors.Internal("failed to update role", err)
		}
	} else {
		if err := s.workspaces.UpdateMemberRole(ctx, workspaceID, targetUserID, newRole); err != nil {
			return cerrors.Internal("failed to update role", err)
		}

		s.logAudit(ctx, workspaceID, actorID, entity.AuditActionRoleChanged, "user", targetUserID.String(), map[string]any{
			"new_role": string(newRole),
		})
	}

	return nil
}

// RemoveMember removes a member from the workspace.
func (s *Service) RemoveMember(ctx context.Context, workspaceID, actorID, targetUserID uuid.UUID) error {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionMemberManage); err != nil {
		return err
	}

	if actorID == targetUserID {
		return cerrors.Forbidden("cannot remove yourself")
	}

	target, err := s.workspaces.GetMember(ctx, workspaceID, targetUserID)
	if err != nil {
		return cerrors.NotFound("member not found")
	}

	actor, err := s.member(ctx, workspaceID, actorID)
	if err != nil {
		return err
	}

	// Admins cannot remove owners.
	if target.Role == entity.WorkspaceRoleOwner && actor.Role != entity.WorkspaceRoleOwner {
		return cerrors.Forbidden("cannot remove an owner")
	}

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			workspaceRepo := s.workspaces
			if scope.Workspaces() != nil {
				workspaceRepo = scope.Workspaces()
			}
			if err := workspaceRepo.RemoveMember(ctx, workspaceID, targetUserID); err != nil {
				return err
			}
			if searchIndexer := scope.SearchIndexer(); searchIndexer != nil {
				if err := searchIndexer.EnqueueDelete(ctx, workspaceID, searchsvc.ResourceTypeUser, targetUserID); err != nil {
					return err
				}
			}
			return s.createAuditTx(ctx, scope, workspaceID, actorID, entity.AuditActionMemberRemoved, "user", targetUserID.String(), nil)
		}); err != nil {
			return cerrors.Internal("failed to remove member", err)
		}
	} else {
		if err := s.workspaces.RemoveMember(ctx, workspaceID, targetUserID); err != nil {
			return cerrors.Internal("failed to remove member", err)
		}
		if s.search != nil {
			if err := s.search.DeleteUserFromWorkspace(ctx, workspaceID, targetUserID); err != nil {
				slog.ErrorContext(ctx, "failed to enqueue removed member search delete", "workspace_id", workspaceID, "user_id", targetUserID, "error", err)
			}
		}

		s.logAudit(ctx, workspaceID, actorID, entity.AuditActionMemberRemoved, "user", targetUserID.String(), nil)
	}
	return nil
}

func (s *Service) ListMediaNodes(ctx context.Context, workspaceID, actorID uuid.UUID) ([]entity.MediaNodeSnapshot, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionCallModerate); err != nil {
		return nil, err
	}
	if s.media == nil {
		return nil, nil
	}
	return s.media.ListNodes(ctx)
}

func (s *Service) GetWorkspaceMediaTopology(ctx context.Context, workspaceID, actorID uuid.UUID) (*entity.WorkspaceMediaTopology, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionCallModerate); err != nil {
		return nil, err
	}
	if s.media == nil {
		return &entity.WorkspaceMediaTopology{WorkspaceID: workspaceID}, nil
	}
	return s.media.GetWorkspaceTopology(ctx, workspaceID)
}

func (s *Service) GetCallQoSHistory(ctx context.Context, workspaceID, actorID, callID uuid.UUID, limit int) (*entity.CallQoSHistory, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionCallModerate); err != nil {
		return nil, err
	}
	if s.media == nil {
		return &entity.CallQoSHistory{
			WorkspaceID: workspaceID,
			CallID:      callID,
			Summary: entity.MediaQoSSummary{
				WorkspaceID: workspaceID,
				CallID:      callID,
			},
		}, nil
	}
	return s.media.GetCallQoSHistory(ctx, workspaceID, callID, limit)
}

func (s *Service) GetCallQualityReport(ctx context.Context, workspaceID, actorID, callID uuid.UUID, limit int) (*entity.CallQualityReport, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionCallModerate); err != nil {
		return nil, err
	}
	if s.media == nil {
		return &entity.CallQualityReport{
			WorkspaceID: workspaceID,
			CallID:      callID,
			Policy: entity.MediaQualityPolicy{
				WorkspaceID: workspaceID,
				CallID:      callID,
				Mode:        entity.MediaQualityPolicyAuto,
			},
		}, nil
	}
	return s.media.GetCallQualityReport(ctx, workspaceID, callID, limit)
}

func (s *Service) GetCallQualityPolicy(ctx context.Context, workspaceID, actorID, callID uuid.UUID) (*entity.MediaQualityPolicy, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionCallModerate); err != nil {
		return nil, err
	}
	if s.media == nil {
		return &entity.MediaQualityPolicy{
			WorkspaceID: workspaceID,
			CallID:      callID,
			Mode:        entity.MediaQualityPolicyAuto,
		}, nil
	}
	return s.media.GetCallQualityPolicy(ctx, workspaceID, callID)
}

func (s *Service) UpdateCallQualityPolicy(ctx context.Context, workspaceID, actorID, callID uuid.UUID, policy entity.MediaQualityPolicy) (*entity.MediaQualityPolicy, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionCallModerate); err != nil {
		return nil, err
	}
	if s.media == nil {
		return nil, cerrors.Unavailable("media quality policy management is not available")
	}
	policy.WorkspaceID = workspaceID
	policy.CallID = callID
	policy.UpdatedBy = actorID
	return s.media.UpdateCallQualityPolicy(ctx, &policy)
}

func (s *Service) ListCallQualityAlerts(ctx context.Context, workspaceID, actorID, callID uuid.UUID, limit int) ([]entity.MediaQualityAlert, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionCallModerate); err != nil {
		return nil, err
	}
	if s.media == nil {
		return nil, nil
	}
	return s.media.ListQualityAlerts(ctx, workspaceID, callID, limit)
}

func (s *Service) GetStorageRuntimeReport(ctx context.Context, workspaceID, actorID uuid.UUID) (*entity.StorageRuntimeReport, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionWorkspaceSettings); err != nil {
		return nil, err
	}
	if s.storage == nil {
		return &entity.StorageRuntimeReport{GeneratedAt: time.Now().UTC()}, nil
	}
	return s.storage.RuntimeReport(ctx)
}

func (s *Service) GetStorageAudit(ctx context.Context, workspaceID, actorID uuid.UUID) (*entity.StorageAuditReport, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionWorkspaceSettings); err != nil {
		return nil, err
	}
	if s.storage == nil {
		return &entity.StorageAuditReport{GeneratedAt: time.Now().UTC()}, nil
	}
	return s.storage.Audit(ctx)
}

func (s *Service) GetObservabilityDashboard(ctx context.Context, workspaceID, actorID uuid.UUID) (*entity.ObservabilityDashboard, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionWorkspaceSettings); err != nil {
		return nil, err
	}
	if s.observability == nil {
		return &entity.ObservabilityDashboard{GeneratedAt: time.Now().UTC()}, nil
	}
	return s.observability.Dashboard(ctx)
}

func (s *Service) GetObservabilityAlerts(ctx context.Context, workspaceID, actorID uuid.UUID) ([]entity.OperationalAlert, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionWorkspaceSettings); err != nil {
		return nil, err
	}
	if s.observability == nil {
		return nil, nil
	}
	return s.observability.Alerts(ctx)
}

func (s *Service) GetObservabilitySLOs(ctx context.Context, workspaceID, actorID uuid.UUID) ([]entity.ObservabilitySLO, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionWorkspaceSettings); err != nil {
		return nil, err
	}
	if s.observability == nil {
		return nil, nil
	}
	return s.observability.SLOs(ctx)
}

// SuspendUser suspends a user account.
func (s *Service) SuspendUser(ctx context.Context, workspaceID, actorID, targetUserID uuid.UUID) error {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionMemberManage); err != nil {
		return err
	}

	target, err := s.workspaces.GetMember(ctx, workspaceID, targetUserID)
	if err != nil {
		return cerrors.NotFound("member not found")
	}
	actor, err := s.member(ctx, workspaceID, actorID)
	if err != nil {
		return err
	}
	if target.Role == entity.WorkspaceRoleOwner && actor.Role != entity.WorkspaceRoleOwner {
		return cerrors.Forbidden("cannot suspend an owner")
	}
	if actorID == targetUserID {
		return cerrors.Forbidden("cannot suspend yourself")
	}

	user, err := s.users.GetByID(ctx, targetUserID)
	if err != nil {
		return cerrors.NotFound("user not found")
	}
	user.Status = entity.UserStatusSuspended
	user.UpdatedAt = time.Now()

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			userRepo := s.users
			if scope.Users() != nil {
				userRepo = scope.Users()
			}
			if err := userRepo.Update(ctx, user); err != nil {
				return err
			}
			if err := s.syncUserSearchDocsTx(ctx, scope, user, true); err != nil {
				return err
			}
			return s.createAuditTx(ctx, scope, workspaceID, actorID, entity.AuditActionUserSuspended, "user", targetUserID.String(), nil)
		}); err != nil {
			return cerrors.Internal("failed to suspend user", err)
		}
	} else {
		if err := s.users.Update(ctx, user); err != nil {
			return cerrors.Internal("failed to suspend user", err)
		}
		s.syncUserSearchDocs(ctx, user, true)

		s.logAudit(ctx, workspaceID, actorID, entity.AuditActionUserSuspended, "user", targetUserID.String(), nil)
	}
	return nil
}

// ReactivateUser reactivates a suspended user.
func (s *Service) ReactivateUser(ctx context.Context, workspaceID, actorID, targetUserID uuid.UUID) error {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionMemberManage); err != nil {
		return err
	}

	if _, err := s.workspaces.GetMember(ctx, workspaceID, targetUserID); err != nil {
		return cerrors.NotFound("member not found")
	}

	user, err := s.users.GetByID(ctx, targetUserID)
	if err != nil {
		return cerrors.NotFound("user not found")
	}
	user.Status = entity.UserStatusActive
	user.UpdatedAt = time.Now()

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			userRepo := s.users
			if scope.Users() != nil {
				userRepo = scope.Users()
			}
			if err := userRepo.Update(ctx, user); err != nil {
				return err
			}
			if err := s.syncUserSearchDocsTx(ctx, scope, user, false); err != nil {
				return err
			}
			return s.createAuditTx(ctx, scope, workspaceID, actorID, entity.AuditActionUserReactivated, "user", targetUserID.String(), nil)
		}); err != nil {
			return cerrors.Internal("failed to reactivate user", err)
		}
	} else {
		if err := s.users.Update(ctx, user); err != nil {
			return cerrors.Internal("failed to reactivate user", err)
		}
		s.syncUserSearchDocs(ctx, user, false)

		s.logAudit(ctx, workspaceID, actorID, entity.AuditActionUserReactivated, "user", targetUserID.String(), nil)
	}
	return nil
}

// UpdateWorkspace updates workspace settings.
func (s *Service) UpdateWorkspace(ctx context.Context, workspaceID, actorID uuid.UUID, name, avatarURL string) error {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionWorkspaceSettings); err != nil {
		return err
	}

	ws, err := s.workspaces.GetByID(ctx, workspaceID)
	if err != nil {
		return cerrors.NotFound("workspace not found")
	}

	if name != "" {
		ws.Name = name
	}
	if avatarURL != "" {
		ws.AvatarURL = avatarURL
	}
	ws.UpdatedAt = time.Now()

	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			workspaceRepo := s.workspaces
			if scope.Workspaces() != nil {
				workspaceRepo = scope.Workspaces()
			}
			if err := workspaceRepo.Update(ctx, ws); err != nil {
				return err
			}
			return s.createAuditTx(ctx, scope, workspaceID, actorID, entity.AuditActionWorkspaceUpdated, "workspace", workspaceID.String(), nil)
		}); err != nil {
			return cerrors.Internal("failed to update workspace", err)
		}
	} else {
		if err := s.workspaces.Update(ctx, ws); err != nil {
			return cerrors.Internal("failed to update workspace", err)
		}

		s.logAudit(ctx, workspaceID, actorID, entity.AuditActionWorkspaceUpdated, "workspace", workspaceID.String(), nil)
	}
	return nil
}

// WorkspaceStats contains aggregated workspace metrics.
type WorkspaceStats struct {
	TotalMembers    int `json:"total_members"`
	TotalChannels   int `json:"total_channels"`
	TotalRecordings int `json:"total_recordings"`
	ActiveCalls     int `json:"active_calls"`
}

// GetAuditLog returns paginated audit entries.
func (s *Service) GetAuditLog(ctx context.Context, workspaceID, actorID uuid.UUID, p pagination.Params) ([]entity.AuditEntry, int, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionWorkspaceSettings); err != nil {
		return nil, 0, err
	}
	return s.audit.List(ctx, workspaceID, p)
}

func (s *Service) syncUserSearchDocs(ctx context.Context, user *entity.User, remove bool) {
	if s.search == nil || user == nil {
		return
	}
	workspaces, err := s.workspaces.ListByUser(ctx, user.ID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list user workspaces for search sync", "user_id", user.ID, "error", err)
		return
	}
	for _, workspace := range workspaces {
		if remove {
			if err := s.search.DeleteUserFromWorkspace(ctx, workspace.ID, user.ID); err != nil {
				slog.ErrorContext(ctx, "failed to enqueue user search delete", "workspace_id", workspace.ID, "user_id", user.ID, "error", err)
			}
			continue
		}
		if err := s.search.IndexUser(ctx, workspace.ID, user.ID, user.DisplayName, user.Email, user.CreatedAt, user.UpdatedAt); err != nil {
			slog.ErrorContext(ctx, "failed to enqueue user search index", "workspace_id", workspace.ID, "user_id", user.ID, "error", err)
		}
	}
}

func (s *Service) syncUserSearchDocsTx(ctx context.Context, scope txscope.Scope, user *entity.User, remove bool) error {
	if user == nil {
		return nil
	}
	txIndexer := scope.SearchIndexer()
	if txIndexer == nil {
		return nil
	}
	workspaceRepo := s.workspaces
	if scope != nil && scope.Workspaces() != nil {
		workspaceRepo = scope.Workspaces()
	}
	workspaces, err := workspaceRepo.ListByUser(ctx, user.ID)
	if err != nil {
		return err
	}
	for _, workspace := range workspaces {
		if remove {
			if err := txIndexer.EnqueueDelete(ctx, workspace.ID, searchsvc.ResourceTypeUser, user.ID); err != nil {
				return err
			}
			continue
		}
		if err := txIndexer.EnqueueUpsert(ctx, searchsvc.Document{
			WorkspaceID: workspace.ID,
			ResourceID:  user.ID,
			Type:        searchsvc.ResourceTypeUser,
			Title:       user.DisplayName,
			Content:     strings.TrimSpace(user.DisplayName + " " + user.Email),
			Metadata: map[string]any{
				"email": user.Email,
			},
			CreatedAt: user.CreatedAt,
			UpdatedAt: user.UpdatedAt,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) createAuditTx(ctx context.Context, scope txscope.Scope, workspaceID, actorID uuid.UUID, action entity.AuditAction, targetType, targetID string, meta map[string]any) error {
	if scope == nil || scope.Audit() == nil {
		return nil
	}
	entry := &entity.AuditEntry{
		ID:          id.New(),
		WorkspaceID: workspaceID,
		ActorID:     actorID,
		Action:      action,
		TargetType:  targetType,
		TargetID:    targetID,
		Metadata:    meta,
		IPAddress:   middleware.IPAddressFromContext(ctx),
		UserAgent:   middleware.UserAgentFromContext(ctx),
		CreatedAt:   time.Now(),
	}
	return scope.Audit().Create(ctx, entry)
}

func (s *Service) ListRoleDefinitions(ctx context.Context, workspaceID, actorID uuid.UUID) ([]entity.WorkspaceRoleDefinition, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionMemberRead); err != nil {
		return nil, err
	}
	if s.roles == nil {
		return nil, cerrors.Unavailable("workspace role management is not configured")
	}
	return s.roles.ListDefinitions(ctx, workspaceID)
}

func (s *Service) CreateRoleDefinition(ctx context.Context, workspaceID, actorID uuid.UUID, input RoleDefinitionInput) (*entity.WorkspaceRoleDefinition, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionMemberManage); err != nil {
		return nil, err
	}
	if s.roles == nil {
		return nil, cerrors.Unavailable("workspace role management is not configured")
	}
	permissions, err := normalizeRolePermissions(input.Permissions)
	if err != nil {
		return nil, err
	}
	name, baseRole, err := validateRoleDefinitionInput(input.Name, input.BaseRole)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	role := &entity.WorkspaceRoleDefinition{
		ID:          id.New(),
		WorkspaceID: workspaceID,
		Name:        name,
		BaseRole:    baseRole,
		Permissions: permissions,
		System:      false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			roleRepo := s.roles
			if scope.Roles() != nil {
				roleRepo = scope.Roles()
			}
			if err := roleRepo.CreateDefinition(ctx, role); err != nil {
				return err
			}
			return s.createAuditTx(ctx, scope, workspaceID, actorID, entity.AuditActionWorkspaceUpdated, "workspace_role_definition", role.ID.String(), map[string]any{
				"name":        role.Name,
				"base_role":   string(role.BaseRole),
				"permissions": role.Permissions,
				"system":      role.System,
			})
		}); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return nil, appErr
			}
			return nil, cerrors.Internal("failed to create workspace role definition", err)
		}
	} else {
		if err := s.roles.CreateDefinition(ctx, role); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return nil, appErr
			}
			return nil, cerrors.Internal("failed to create workspace role definition", err)
		}
		s.logAudit(ctx, workspaceID, actorID, entity.AuditActionWorkspaceUpdated, "workspace_role_definition", role.ID.String(), map[string]any{
			"name":        role.Name,
			"base_role":   string(role.BaseRole),
			"permissions": role.Permissions,
			"system":      role.System,
		})
	}
	return role, nil
}

func (s *Service) UpdateRoleDefinition(ctx context.Context, workspaceID, actorID, roleID uuid.UUID, input RoleDefinitionInput) (*entity.WorkspaceRoleDefinition, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionMemberManage); err != nil {
		return nil, err
	}
	if s.roles == nil {
		return nil, cerrors.Unavailable("workspace role management is not configured")
	}
	role, err := s.roles.GetDefinition(ctx, workspaceID, roleID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok {
			return nil, appErr
		}
		return nil, cerrors.Internal("failed to get workspace role definition", err)
	}
	if role.System {
		return nil, cerrors.Forbidden("system workspace roles cannot be modified")
	}
	permissions, err := normalizeRolePermissions(input.Permissions)
	if err != nil {
		return nil, err
	}
	name, baseRole, err := validateRoleDefinitionInput(input.Name, input.BaseRole)
	if err != nil {
		return nil, err
	}
	role.Name = name
	role.BaseRole = baseRole
	role.Permissions = permissions
	role.UpdatedAt = time.Now().UTC()
	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			roleRepo := s.roles
			if scope.Roles() != nil {
				roleRepo = scope.Roles()
			}
			if err := roleRepo.UpdateDefinition(ctx, role); err != nil {
				return err
			}
			return s.createAuditTx(ctx, scope, workspaceID, actorID, entity.AuditActionWorkspaceUpdated, "workspace_role_definition", role.ID.String(), map[string]any{
				"name":        role.Name,
				"base_role":   string(role.BaseRole),
				"permissions": role.Permissions,
			})
		}); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return nil, appErr
			}
			return nil, cerrors.Internal("failed to update workspace role definition", err)
		}
	} else {
		if err := s.roles.UpdateDefinition(ctx, role); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return nil, appErr
			}
			return nil, cerrors.Internal("failed to update workspace role definition", err)
		}
		s.logAudit(ctx, workspaceID, actorID, entity.AuditActionWorkspaceUpdated, "workspace_role_definition", role.ID.String(), map[string]any{
			"name":        role.Name,
			"base_role":   string(role.BaseRole),
			"permissions": role.Permissions,
		})
	}
	return role, nil
}

func (s *Service) DeleteRoleDefinition(ctx context.Context, workspaceID, actorID, roleID uuid.UUID) error {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionMemberManage); err != nil {
		return err
	}
	if s.roles == nil {
		return cerrors.Unavailable("workspace role management is not configured")
	}
	role, err := s.roles.GetDefinition(ctx, workspaceID, roleID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok {
			return appErr
		}
		return cerrors.Internal("failed to get workspace role definition", err)
	}
	if role.System {
		return cerrors.Forbidden("system workspace roles cannot be deleted")
	}
	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			roleRepo := s.roles
			if scope.Roles() != nil {
				roleRepo = scope.Roles()
			}
			if err := roleRepo.DeleteDefinition(ctx, workspaceID, roleID); err != nil {
				return err
			}
			return s.createAuditTx(ctx, scope, workspaceID, actorID, entity.AuditActionWorkspaceUpdated, "workspace_role_definition", roleID.String(), map[string]any{
				"deleted": true,
			})
		}); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return appErr
			}
			return cerrors.Internal("failed to delete workspace role definition", err)
		}
	} else {
		if err := s.roles.DeleteDefinition(ctx, workspaceID, roleID); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return appErr
			}
			return cerrors.Internal("failed to delete workspace role definition", err)
		}
		s.logAudit(ctx, workspaceID, actorID, entity.AuditActionWorkspaceUpdated, "workspace_role_definition", roleID.String(), map[string]any{
			"deleted": true,
		})
	}
	return nil
}

func (s *Service) ListMemberRoles(ctx context.Context, workspaceID, actorID, targetUserID uuid.UUID) ([]entity.WorkspaceRoleDefinition, error) {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionMemberRead); err != nil {
		return nil, err
	}
	if s.roles == nil {
		return nil, cerrors.Unavailable("workspace role management is not configured")
	}
	if _, err := s.member(ctx, workspaceID, targetUserID); err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeForbidden {
			return nil, cerrors.NotFound("member not found")
		}
		return nil, err
	}
	return s.roles.ListAssignedDefinitions(ctx, workspaceID, targetUserID)
}

func (s *Service) AssignMemberRole(ctx context.Context, workspaceID, actorID, targetUserID, roleID uuid.UUID) error {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionMemberManage); err != nil {
		return err
	}
	if s.roles == nil {
		return cerrors.Unavailable("workspace role management is not configured")
	}
	actor, err := s.member(ctx, workspaceID, actorID)
	if err != nil {
		return err
	}
	target, err := s.member(ctx, workspaceID, targetUserID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeForbidden {
			return cerrors.NotFound("member not found")
		}
		return err
	}
	role, err := s.roles.GetDefinition(ctx, workspaceID, roleID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok {
			return appErr
		}
		return cerrors.Internal("failed to get workspace role definition", err)
	}
	if target.Role != role.BaseRole {
		return cerrors.InvalidInput("workspace member base role does not match role definition")
	}
	if (target.Role == entity.WorkspaceRoleOwner || target.Role == entity.WorkspaceRoleAdmin) && actor.Role != entity.WorkspaceRoleOwner {
		return cerrors.Forbidden("only owners can manage owner or admin custom roles")
	}
	now := time.Now().UTC()
	assignedBy := actorID
	assignment := &entity.WorkspaceRoleAssignment{
		ID:          id.New(),
		WorkspaceID: workspaceID,
		UserID:      targetUserID,
		RoleID:      roleID,
		AssignedBy:  &assignedBy,
		AssignedAt:  now,
	}
	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			roleRepo := s.roles
			if scope.Roles() != nil {
				roleRepo = scope.Roles()
			}
			if err := roleRepo.AssignRole(ctx, assignment); err != nil {
				return err
			}
			return s.createAuditTx(ctx, scope, workspaceID, actorID, entity.AuditActionRoleChanged, "workspace_role_assignment", assignment.ID.String(), map[string]any{
				"user_id":   targetUserID,
				"role_id":   roleID,
				"role_name": role.Name,
			})
		}); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return appErr
			}
			return cerrors.Internal("failed to assign workspace role", err)
		}
	} else {
		if err := s.roles.AssignRole(ctx, assignment); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return appErr
			}
			return cerrors.Internal("failed to assign workspace role", err)
		}
		s.logAudit(ctx, workspaceID, actorID, entity.AuditActionRoleChanged, "workspace_role_assignment", assignment.ID.String(), map[string]any{
			"user_id":   targetUserID,
			"role_id":   roleID,
			"role_name": role.Name,
		})
	}
	return nil
}

func (s *Service) UnassignMemberRole(ctx context.Context, workspaceID, actorID, targetUserID, roleID uuid.UUID) error {
	if err := s.requirePermission(ctx, workspaceID, actorID, rbac.PermissionMemberManage); err != nil {
		return err
	}
	if s.roles == nil {
		return cerrors.Unavailable("workspace role management is not configured")
	}
	actor, err := s.member(ctx, workspaceID, actorID)
	if err != nil {
		return err
	}
	target, err := s.member(ctx, workspaceID, targetUserID)
	if err != nil {
		if appErr, ok := cerrors.AsAppError(err); ok && appErr.Code == cerrors.CodeForbidden {
			return cerrors.NotFound("member not found")
		}
		return err
	}
	if (target.Role == entity.WorkspaceRoleOwner || target.Role == entity.WorkspaceRoleAdmin) && actor.Role != entity.WorkspaceRoleOwner {
		return cerrors.Forbidden("only owners can manage owner or admin custom roles")
	}
	if s.tx != nil {
		if err := s.tx.WithinTx(ctx, func(ctx context.Context, scope txscope.Scope) error {
			roleRepo := s.roles
			if scope.Roles() != nil {
				roleRepo = scope.Roles()
			}
			if err := roleRepo.UnassignRole(ctx, workspaceID, targetUserID, roleID); err != nil {
				return err
			}
			return s.createAuditTx(ctx, scope, workspaceID, actorID, entity.AuditActionRoleChanged, "workspace_role_assignment", roleID.String(), map[string]any{
				"user_id": targetUserID,
				"removed": true,
			})
		}); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return appErr
			}
			return cerrors.Internal("failed to unassign workspace role", err)
		}
	} else {
		if err := s.roles.UnassignRole(ctx, workspaceID, targetUserID, roleID); err != nil {
			if appErr, ok := cerrors.AsAppError(err); ok {
				return appErr
			}
			return cerrors.Internal("failed to unassign workspace role", err)
		}
		s.logAudit(ctx, workspaceID, actorID, entity.AuditActionRoleChanged, "workspace_role_assignment", roleID.String(), map[string]any{
			"user_id": targetUserID,
			"removed": true,
		})
	}
	return nil
}

func normalizeRolePermissions(raw []string) ([]string, error) {
	permissions, err := rbac.NormalizePermissionStrings(raw)
	if err != nil {
		return nil, cerrors.InvalidInput(err.Error())
	}
	return permissions, nil
}

func validateRoleDefinitionInput(name string, baseRole entity.WorkspaceRole) (string, entity.WorkspaceRole, error) {
	normalizedName := strings.TrimSpace(name)
	if normalizedName == "" {
		return "", "", cerrors.InvalidInput("name is required")
	}
	if len(normalizedName) > 80 {
		return "", "", cerrors.InvalidInput("name must be at most 80 characters")
	}
	switch baseRole {
	case entity.WorkspaceRoleOwner, entity.WorkspaceRoleAdmin, entity.WorkspaceRoleMember, entity.WorkspaceRoleGuest:
	default:
		return "", "", cerrors.InvalidInput("invalid base_role")
	}
	return normalizedName, baseRole, nil
}

// LogAction records an audit event. This is exposed so other services can log through admin.
func (s *Service) LogAction(ctx context.Context, workspaceID, actorID uuid.UUID, action entity.AuditAction, targetType, targetID string, meta map[string]any) {
	s.logAudit(ctx, workspaceID, actorID, action, targetType, targetID, meta)
}

func (s *Service) logAudit(ctx context.Context, workspaceID, actorID uuid.UUID, action entity.AuditAction, targetType, targetID string, meta map[string]any) {
	entry := &entity.AuditEntry{
		ID:          id.New(),
		WorkspaceID: workspaceID,
		ActorID:     actorID,
		Action:      action,
		TargetType:  targetType,
		TargetID:    targetID,
		Metadata:    meta,
		IPAddress:   middleware.IPAddressFromContext(ctx),
		UserAgent:   middleware.UserAgentFromContext(ctx),
		CreatedAt:   time.Now(),
	}

	if err := s.audit.Create(ctx, entry); err != nil {
		slog.ErrorContext(ctx, "failed to create audit entry", "action", action, "error", err)
	}
}
