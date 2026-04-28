package entity

import "testing"

func TestWorkspaceConnectionPolicyCanContact(t *testing.T) {
	policy := WorkspaceConnectionPolicy{
		DirectoryVisibility: DirectoryVisibilityDirectory,
		ContactPolicy:       ContactPolicyRoleBased,
		AllowedSourceRoles:  []WorkspaceRole{WorkspaceRoleOwner, WorkspaceRoleAdmin},
		AllowedTargetRoles:  []WorkspaceRole{WorkspaceRoleOwner, WorkspaceRoleAdmin},
	}

	if !policy.CanViewDirectory() {
		t.Fatalf("policy should allow directory visibility")
	}
	if !policy.CanContact(WorkspaceRoleAdmin, WorkspaceRoleOwner) {
		t.Fatalf("admin should be able to contact owner")
	}
	if policy.CanContact(WorkspaceRoleGuest, WorkspaceRoleOwner) {
		t.Fatalf("guest should not be able to contact owner")
	}
	if policy.CanContact(WorkspaceRoleAdmin, WorkspaceRoleMember) {
		t.Fatalf("admin should not contact member when target role is not allowed")
	}
}
