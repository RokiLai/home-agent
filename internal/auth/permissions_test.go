package auth

import (
	"testing"
)

func TestPermissionsMatrix(t *testing.T) {
	// 1. Owner 必须拥有全部权限
	for _, p := range allPermissions {
		if !RoleHasPermission(RoleOwner, p) {
			t.Fatalf("RoleOwner must have permission %s", p)
		}
	}
	ownerPerms := PermissionsForRole(RoleOwner)
	if len(ownerPerms) != len(allPermissions) {
		t.Fatalf("PermissionsForRole(RoleOwner) count mismatch: %d != %d", len(ownerPerms), len(allPermissions))
	}

	// 2. Admin 权限正反例
	allowedForAdmin := []Permission{
		PermDevicesRead,
		PermDevicesUpdate,
		PermDevicesDelete,
		PermDevicesShare,
		PermDevicesSync,
		PermDevicesWake,
		PermDevicesShutdown,
		PermDevicesUpgrade,
		PermCommandsRead,
		PermCommandsCancel,
		PermHealthRead,
		PermAlertsRead,
		PermAlertsManage,
		PermInstanceSettingsRead,
		PermAuditRead,
	}
	for _, p := range allowedForAdmin {
		if !RoleHasPermission(RoleAdmin, p) {
			t.Fatalf("RoleAdmin should have permission %s", p)
		}
	}

	deniedForAdmin := []Permission{
		PermUsersCreate,
		PermUsersUpdateRole,
		PermUsersDisable,
		PermUsersDelete,
		PermSessionsReadAll,
		PermSessionsRevokeAll,
		PermDevicesTransfer,
		PermGitHubManage,
		PermInstanceSettingsManage,
	}
	for _, p := range deniedForAdmin {
		if RoleHasPermission(RoleAdmin, p) {
			t.Fatalf("RoleAdmin must NOT have permission %s", p)
		}
	}

	// 3. Viewer 权限正反例
	allowedForViewer := []Permission{
		PermUsersRead,
		PermUsersResetPassword,
		PermDevicesRead,
		PermCommandsRead,
		PermHealthRead,
		PermAlertsRead,
	}
	for _, p := range allowedForViewer {
		if !RoleHasPermission(RoleViewer, p) {
			t.Fatalf("RoleViewer should have permission %s", p)
		}
	}

	deniedForViewer := []Permission{
		PermUsersCreate,
		PermUsersDisable,
		PermDevicesUpdate,
		PermDevicesDelete,
		PermDevicesShare,
		PermDevicesSync,
		PermDevicesWake,
		PermDevicesShutdown,
		PermDevicesUpgrade,
		PermCommandsCancel,
		PermAlertsManage,
		PermGitHubManage,
		PermInstanceSettingsRead,
		PermInstanceSettingsManage,
		PermAuditRead,
	}
	for _, p := range deniedForViewer {
		if RoleHasPermission(RoleViewer, p) {
			t.Fatalf("RoleViewer must NOT have permission %s", p)
		}
	}

	// 4. 未知角色与未知权限必须默认拒绝
	if RoleHasPermission("superadmin", PermDevicesRead) {
		t.Fatal("Unknown role must not have any permissions")
	}
	if RoleHasPermission(RoleOwner, "custom.unknown.permission") {
		t.Fatal("Unknown permission must be rejected")
	}
}
