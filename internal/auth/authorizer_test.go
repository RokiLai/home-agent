package auth

import (
	"net/http"
	"testing"
)

type mockDeviceScopeResolver struct {
	devices map[string]bool                 // deviceID -> exists
	owners  map[string]string               // deviceID -> ownerUserID
	grants  map[string]map[string]GrantType // deviceID -> userID -> GrantType
}

type GrantType string

const (
	GrantRead    GrantType = "read"
	GrantOperate GrantType = "operate"
	GrantManage  GrantType = "manage"
)

func (m *mockDeviceScopeResolver) IsDeviceVisible(userID string, deviceID string) (visible bool, exists bool) {
	if !m.devices[deviceID] {
		return false, false
	}
	if m.owners[deviceID] == userID {
		return true, true
	}
	if userGrants, ok := m.grants[deviceID]; ok {
		if _, hasGrant := userGrants[userID]; hasGrant {
			return true, true
		}
	}
	return false, true
}

func (m *mockDeviceScopeResolver) IsDeviceOwner(userID string, deviceID string) bool {
	return m.owners[deviceID] == userID
}

func (m *mockDeviceScopeResolver) HasDevicePermission(userID string, deviceID string, perm Permission) bool {
	if m.owners[deviceID] == userID {
		return true
	}
	userGrants, ok := m.grants[deviceID]
	if !ok {
		return false
	}
	level, hasGrant := userGrants[userID]
	if !hasGrant {
		return false
	}

	switch perm {
	case PermDevicesRead, PermCommandsRead, PermHealthRead, PermAlertsRead:
		return true
	case PermDevicesSync, PermDevicesWake, PermDevicesShutdown, PermDevicesUpgrade, PermCommandsCancel:
		return level == GrantOperate || level == GrantManage
	case PermDevicesUpdate, PermAlertsManage:
		return level == GrantManage
	default:
		return false
	}
}

func TestAuthorizer_OwnerPermissions(t *testing.T) {
	resolver := &mockDeviceScopeResolver{
		devices: map[string]bool{"dev-1": true},
		owners:  map[string]string{"dev-1": "other-user"},
		grants:  map[string]map[string]GrantType{},
	}
	authorizer := NewAuthorizer(resolver)
	ownerActor := Actor{UserID: "owner-1", Username: "boss", Role: RoleOwner}

	// 1. Owner 访问存在的设备（即使不是设备 Owner）-> 全部允许
	d1 := authorizer.Authorize(AuthorizationRequest{
		Actor:      ownerActor,
		Permission: PermDevicesShutdown,
		Resource:   DeviceResource("dev-1"),
	})
	if !d1.Allowed || d1.StatusCode != http.StatusOK {
		t.Fatalf("Owner should be allowed to shutdown dev-1: %+v", d1)
	}

	// 2. Owner 访问不存在的设备 -> 404
	d2 := authorizer.Authorize(AuthorizationRequest{
		Actor:      ownerActor,
		Permission: PermDevicesRead,
		Resource:   DeviceResource("dev-non-existent"),
	})
	if d2.Allowed || d2.StatusCode != http.StatusNotFound {
		t.Fatalf("Owner accessing non-existent device must get 404: %+v", d2)
	}

	// 3. Owner 访问全局管理权限 -> 200
	d3 := authorizer.Authorize(AuthorizationRequest{
		Actor:      ownerActor,
		Permission: PermUsersCreate,
		Resource:   GlobalResource(),
	})
	if !d3.Allowed || d3.StatusCode != http.StatusOK {
		t.Fatalf("Owner should be allowed to create users: %+v", d3)
	}
}

func TestAuthorizer_AdminIDORProtectionAndGrants(t *testing.T) {
	resolver := &mockDeviceScopeResolver{
		devices: map[string]bool{
			"dev-alice-owned":   true,
			"dev-shared-to-bob": true,
			"dev-secret-charlie": true,
		},
		owners: map[string]string{
			"dev-alice-owned":   "alice",
			"dev-shared-to-bob": "alice",
			"dev-secret-charlie": "charlie",
		},
		grants: map[string]map[string]GrantType{
			"dev-shared-to-bob": {
				"bob": GrantOperate,
			},
		},
	}
	authorizer := NewAuthorizer(resolver)
	bobAdmin := Actor{UserID: "bob", Username: "bob", Role: RoleAdmin}

	// 1. Bob 访问完全未授权的 Charlie 设备 -> 必须统一返回 404 resource_not_found（防 IDOR 探测）
	d1 := authorizer.Authorize(AuthorizationRequest{
		Actor:      bobAdmin,
		Permission: PermDevicesRead,
		Resource:   DeviceResource("dev-secret-charlie"),
	})
	if d1.Allowed || d1.StatusCode != http.StatusNotFound || d1.ErrorCode != "resource_not_found" {
		t.Fatalf("Unauthorized device must return 404 for IDOR protection, got: %+v", d1)
	}

	// 2. Bob 访问不存在的设备 -> 404
	d2 := authorizer.Authorize(AuthorizationRequest{
		Actor:      bobAdmin,
		Permission: PermDevicesRead,
		Resource:   DeviceResource("dev-unknown"),
	})
	if d2.Allowed || d2.StatusCode != http.StatusNotFound {
		t.Fatalf("Unknown device must return 404, got: %+v", d2)
	}

	// 3. Bob 访问获 operate 共享授权的设备 -> 允许操作（shutdown）
	d3 := authorizer.Authorize(AuthorizationRequest{
		Actor:      bobAdmin,
		Permission: PermDevicesShutdown,
		Resource:   DeviceResource("dev-shared-to-bob"),
	})
	if !d3.Allowed || d3.StatusCode != http.StatusOK {
		t.Fatalf("Bob should be allowed to operate shared device: %+v", d3)
	}

	// 4. Bob 尝试删除获共享授权但非所有者的设备 -> 403 Forbidden
	d4 := authorizer.Authorize(AuthorizationRequest{
		Actor:      bobAdmin,
		Permission: PermDevicesDelete,
		Resource:   DeviceResource("dev-shared-to-bob"),
	})
	if d4.Allowed || d4.StatusCode != http.StatusForbidden || d4.ErrorCode != "forbidden" {
		t.Fatalf("Non-owner must not be allowed to delete device (403 expected): %+v", d4)
	}

	// 5. Bob 尝试修改未获得 manage 级别的属性 -> 403 Forbidden
	d5 := authorizer.Authorize(AuthorizationRequest{
		Actor:      bobAdmin,
		Permission: PermDevicesUpdate,
		Resource:   DeviceResource("dev-shared-to-bob"),
	})
	if d5.Allowed || d5.StatusCode != http.StatusForbidden {
		t.Fatalf("Operate-only grant cannot update device metadata (403 expected): %+v", d5)
	}

	// 6. Bob 尝试创建用户 (全局权限) -> 403 Forbidden
	d6 := authorizer.Authorize(AuthorizationRequest{
		Actor:      bobAdmin,
		Permission: PermUsersCreate,
		Resource:   GlobalResource(),
	})
	if d6.Allowed || d6.StatusCode != http.StatusForbidden {
		t.Fatalf("Admin must not be allowed to create users: %+v", d6)
	}
}

func TestAuthorizer_ViewerReadonlyEnforcement(t *testing.T) {
	resolver := &mockDeviceScopeResolver{
		devices: map[string]bool{"dev-shared": true},
		owners:  map[string]string{"dev-shared": "alice"},
		grants: map[string]map[string]GrantType{
			// 即使 Grant 误配为 manage，Viewer 角色本身仍受只读约束
			"dev-shared": {"viewer-tom": GrantManage},
		},
	}
	authorizer := NewAuthorizer(resolver)
	tomViewer := Actor{UserID: "viewer-tom", Username: "tom", Role: RoleViewer}

	// 1. Viewer 读设备 -> 200 OK
	d1 := authorizer.Authorize(AuthorizationRequest{
		Actor:      tomViewer,
		Permission: PermDevicesRead,
		Resource:   DeviceResource("dev-shared"),
	})
	if !d1.Allowed || d1.StatusCode != http.StatusOK {
		t.Fatalf("Viewer should be allowed to read: %+v", d1)
	}

	// 2. Viewer 试图关机 -> 403 Forbidden（角色无写权限）
	d2 := authorizer.Authorize(AuthorizationRequest{
		Actor:      tomViewer,
		Permission: PermDevicesShutdown,
		Resource:   DeviceResource("dev-shared"),
	})
	if d2.Allowed || d2.StatusCode != http.StatusForbidden {
		t.Fatalf("Viewer must be rejected for write operations even with manage grant: %+v", d2)
	}
}
