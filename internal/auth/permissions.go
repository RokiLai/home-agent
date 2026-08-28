package auth

// Permission 定义细粒度权限常量字符串
type Permission string

const (
	// 用户与会话权限
	PermUsersRead          Permission = "users.read"
	PermUsersCreate        Permission = "users.create"
	PermUsersUpdateRole    Permission = "users.update_role"
	PermUsersDisable       Permission = "users.disable"
	PermUsersDelete        Permission = "users.delete"
	PermUsersResetPassword Permission = "users.reset_password"
	PermSessionsReadAll    Permission = "sessions.read_all"
	PermSessionsRevokeAll  Permission = "sessions.revoke_all"

	// 设备操作与管理权限
	PermDevicesRead             Permission = "devices.read"
	PermDevicesUpdate           Permission = "devices.update"
	PermDevicesDelete           Permission = "devices.delete"
	PermDevicesShare            Permission = "devices.share"
	PermDevicesTransfer         Permission = "devices.transfer"
	PermDevicesClaimTokenCreate Permission = "devices.claim_token.create"
	PermDevicesClaimTokenRead   Permission = "devices.claim_token.read"
	PermDevicesClaimTokenRevoke Permission = "devices.claim_token.revoke"
	PermDevicesSync             Permission = "devices.sync"
	PermDevicesWake             Permission = "devices.wake"
	PermDevicesShutdown         Permission = "devices.shutdown"
	PermDevicesUpgrade          Permission = "devices.upgrade"

	// 命令控制与监控告警权限
	PermCommandsRead           Permission = "commands.read"
	PermCommandsCancel         Permission = "commands.cancel"
	PermHealthRead             Permission = "health.read"
	PermAlertsRead             Permission = "alerts.read"
	PermAlertsManage           Permission = "alerts.manage"
	PermGitHubManage           Permission = "github.manage"
	PermInstanceSettingsRead   Permission = "instance.settings.read"
	PermInstanceSettingsManage Permission = "instance.settings.manage"
	PermAuditRead              Permission = "audit.read"
)

var allPermissions = []Permission{
	PermUsersRead,
	PermUsersCreate,
	PermUsersUpdateRole,
	PermUsersDisable,
	PermUsersDelete,
	PermUsersResetPassword,
	PermSessionsReadAll,
	PermSessionsRevokeAll,
	PermDevicesRead,
	PermDevicesUpdate,
	PermDevicesDelete,
	PermDevicesShare,
	PermDevicesTransfer,
	PermDevicesClaimTokenCreate,
	PermDevicesClaimTokenRead,
	PermDevicesClaimTokenRevoke,
	PermDevicesSync,
	PermDevicesWake,
	PermDevicesShutdown,
	PermDevicesUpgrade,
	PermCommandsRead,
	PermCommandsCancel,
	PermHealthRead,
	PermAlertsRead,
	PermAlertsManage,
	PermGitHubManage,
	PermInstanceSettingsRead,
	PermInstanceSettingsManage,
	PermAuditRead,
}

var ownerPermissions = map[Permission]bool{}
var adminPermissions = map[Permission]bool{}
var viewerPermissions = map[Permission]bool{}

func init() {
	// Owner 拥有全量权限
	for _, p := range allPermissions {
		ownerPermissions[p] = true
	}

	// Admin 拥有设备级管理、操作与查看权限，以及自身相关操作
	adminPermList := []Permission{
		PermUsersRead,
		PermUsersResetPassword,
		PermDevicesRead,
		PermDevicesUpdate,
		PermDevicesDelete,
		PermDevicesShare,
		PermDevicesClaimTokenCreate,
		PermDevicesClaimTokenRead,
		PermDevicesClaimTokenRevoke,
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
	for _, p := range adminPermList {
		adminPermissions[p] = true
	}

	// Viewer 仅具备只读查看权限与修改自己密码的能力
	viewerPermList := []Permission{
		PermUsersRead,
		PermUsersResetPassword,
		PermDevicesRead,
		PermCommandsRead,
		PermHealthRead,
		PermAlertsRead,
	}
	for _, p := range viewerPermList {
		viewerPermissions[p] = true
	}
}

// RoleHasPermission 校验指定角色是否具备该权限常量的执行资质
func RoleHasPermission(role Role, perm Permission) bool {
	switch role {
	case RoleOwner:
		return ownerPermissions[perm]
	case RoleAdmin:
		return adminPermissions[perm]
	case RoleViewer:
		return viewerPermissions[perm]
	default:
		return false
	}
}

// PermissionsForRole 返回该角色所具备的所有权限常量列表（用于前端展示和接口返回）
func PermissionsForRole(role Role) []Permission {
	var list []Permission
	for _, p := range allPermissions {
		if RoleHasPermission(role, p) {
			list = append(list, p)
		}
	}
	return list
}
