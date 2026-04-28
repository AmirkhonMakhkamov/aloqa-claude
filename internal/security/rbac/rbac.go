package rbac

import (
	"fmt"
	"sort"
	"strings"

	"aloqa/internal/domain/entity"
)

type Permission string

const (
	PermissionWorkspaceManage      Permission = "workspace.manage"
	PermissionWorkspaceSettings    Permission = "workspace.settings.manage"
	PermissionMemberRead           Permission = "member.read"
	PermissionMemberInvite         Permission = "member.invite"
	PermissionMemberManage         Permission = "member.manage"
	PermissionChannelCreate        Permission = "channel.create"
	PermissionChannelManage        Permission = "channel.manage"
	PermissionMessageSend          Permission = "message.send"
	PermissionMessageModerate      Permission = "message.moderate"
	PermissionCallStart            Permission = "call.start"
	PermissionCallModerate         Permission = "call.moderate"
	PermissionRecordingStart       Permission = "recording.start"
	PermissionRecordingRead        Permission = "recording.read"
	PermissionMarketplaceManage    Permission = "marketplace.manage"
	PermissionAIUse                Permission = "ai.use"
	PermissionTelephonyUse         Permission = "telephony.use"
	PermissionWhiteLabelManage     Permission = "white_label.manage"
	PermissionCollaborationManage  Permission = "collaboration.manage"
	PermissionDirectoryReadShared  Permission = "directory.shared.read"
	PermissionDirectMessageShared  Permission = "direct_message.shared.start"
	PermissionCrossWorkspaceInvite Permission = "workspace.external.invite"
)

type RoleDefinition struct {
	Name        string
	BaseRole    entity.WorkspaceRole
	Permissions map[Permission]bool
}

func (r RoleDefinition) Has(permission Permission) bool {
	return r.Permissions[permission]
}

func DefaultWorkspaceRole(role entity.WorkspaceRole) RoleDefinition {
	return RoleDefinition{
		Name:        string(role),
		BaseRole:    role,
		Permissions: DefaultWorkspacePermissions(role),
	}
}

func DefaultWorkspacePermissions(role entity.WorkspaceRole) map[Permission]bool {
	permissions := map[Permission]bool{
		PermissionMemberRead:          true,
		PermissionChannelCreate:       true,
		PermissionMessageSend:         true,
		PermissionCallStart:           true,
		PermissionRecordingRead:       true,
		PermissionAIUse:               true,
		PermissionDirectoryReadShared: true,
		PermissionDirectMessageShared: true,
	}

	switch role {
	case entity.WorkspaceRoleOwner:
		for _, p := range adminPermissions() {
			permissions[p] = true
		}
		permissions[PermissionWorkspaceManage] = true
		permissions[PermissionWhiteLabelManage] = true
	case entity.WorkspaceRoleAdmin:
		for _, p := range adminPermissions() {
			permissions[p] = true
		}
	case entity.WorkspaceRoleMember:
	case entity.WorkspaceRoleGuest:
		permissions = map[Permission]bool{
			PermissionMemberRead:    true,
			PermissionMessageSend:   true,
			PermissionCallStart:     true,
			PermissionRecordingRead: true,
		}
	default:
		return map[Permission]bool{}
	}

	return permissions
}

func adminPermissions() []Permission {
	return []Permission{
		PermissionWorkspaceSettings,
		PermissionMemberRead,
		PermissionMemberInvite,
		PermissionMemberManage,
		PermissionChannelCreate,
		PermissionChannelManage,
		PermissionMessageSend,
		PermissionMessageModerate,
		PermissionCallStart,
		PermissionCallModerate,
		PermissionRecordingStart,
		PermissionRecordingRead,
		PermissionMarketplaceManage,
		PermissionAIUse,
		PermissionTelephonyUse,
		PermissionCollaborationManage,
		PermissionDirectoryReadShared,
		PermissionDirectMessageShared,
		PermissionCrossWorkspaceInvite,
	}
}

func Has(role entity.WorkspaceRole, permission Permission) bool {
	return DefaultWorkspaceRole(role).Has(permission)
}

func AllPermissions() []Permission {
	permissions := []Permission{
		PermissionWorkspaceManage,
		PermissionWorkspaceSettings,
		PermissionMemberRead,
		PermissionMemberInvite,
		PermissionMemberManage,
		PermissionChannelCreate,
		PermissionChannelManage,
		PermissionMessageSend,
		PermissionMessageModerate,
		PermissionCallStart,
		PermissionCallModerate,
		PermissionRecordingStart,
		PermissionRecordingRead,
		PermissionMarketplaceManage,
		PermissionAIUse,
		PermissionTelephonyUse,
		PermissionWhiteLabelManage,
		PermissionCollaborationManage,
		PermissionDirectoryReadShared,
		PermissionDirectMessageShared,
		PermissionCrossWorkspaceInvite,
	}
	sort.Slice(permissions, func(i, j int) bool {
		return permissions[i] < permissions[j]
	})
	return permissions
}

func ParsePermission(raw string) (Permission, bool) {
	permission := Permission(strings.TrimSpace(raw))
	for _, candidate := range AllPermissions() {
		if candidate == permission {
			return permission, true
		}
	}
	return "", false
}

func NormalizePermissionStrings(raw []string) ([]string, error) {
	unique := make(map[string]struct{}, len(raw))
	normalized := make([]string, 0, len(raw))
	for _, value := range raw {
		permission, ok := ParsePermission(value)
		if !ok {
			return nil, fmt.Errorf("invalid permission: %s", value)
		}
		key := string(permission)
		if _, exists := unique[key]; exists {
			continue
		}
		unique[key] = struct{}{}
		normalized = append(normalized, key)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func RoleDefinitionFromEntity(role entity.WorkspaceRoleDefinition) (RoleDefinition, error) {
	permissions := make(map[Permission]bool, len(role.Permissions))
	for _, raw := range role.Permissions {
		permission, ok := ParsePermission(raw)
		if !ok {
			return RoleDefinition{}, fmt.Errorf("invalid permission in stored role definition: %s", raw)
		}
		permissions[permission] = true
	}
	return RoleDefinition{
		Name:        role.Name,
		BaseRole:    role.BaseRole,
		Permissions: permissions,
	}, nil
}

func Merge(base RoleDefinition, overlays ...RoleDefinition) RoleDefinition {
	merged := RoleDefinition{
		Name:        base.Name,
		BaseRole:    base.BaseRole,
		Permissions: make(map[Permission]bool, len(base.Permissions)),
	}
	for permission, allowed := range base.Permissions {
		merged.Permissions[permission] = allowed
	}
	for _, overlay := range overlays {
		for permission, allowed := range overlay.Permissions {
			if allowed {
				merged.Permissions[permission] = true
			}
		}
	}
	return merged
}
