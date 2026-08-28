package auth

import (
	"net/http"
)

// ResourceKind 定义资源类型
type ResourceKind string

const (
	// ResourceKindGlobal 全局实例级资源
	ResourceKindGlobal ResourceKind = "global"
	// ResourceKindDevice 设备级资源
	ResourceKindDevice ResourceKind = "device"
	// ResourceKindUser 用户账号级资源
	ResourceKindUser ResourceKind = "user"
)

// ResourceRef 表示本次授权请求的目标资源引用
type ResourceRef struct {
	Kind ResourceKind
	ID   string
}

// GlobalResource 构建全局资源引用
func GlobalResource() ResourceRef {
	return ResourceRef{Kind: ResourceKindGlobal, ID: ""}
}

// DeviceResource 构建设备资源引用
func DeviceResource(deviceID string) ResourceRef {
	return ResourceRef{Kind: ResourceKindDevice, ID: deviceID}
}

// UserResource 构建用户资源引用
func UserResource(userID string) ResourceRef {
	return ResourceRef{Kind: ResourceKindUser, ID: userID}
}

// Actor 表示发起请求的操作主体
type Actor struct {
	UserID   string
	Username string
	Role     Role
}

// AuthorizationRequest 封装统一授权请求参数
type AuthorizationRequest struct {
	Actor      Actor
	Permission Permission
	Resource   ResourceRef
}

// Decision 表示授权决策结果
type Decision struct {
	Allowed    bool
	StatusCode int    // HTTP 状态码：200, 401, 403, 404
	ErrorCode  string // 机器识别错误码：unauthorized, forbidden, resource_not_found
	ReasonCode string
}

// DeviceScopeResolver 定义设备归属与共享授权范围查询接口
type DeviceScopeResolver interface {
	// IsDeviceVisible 判定设备对指定用户是否可见，以及该设备在系统内是否存在
	IsDeviceVisible(userID string, deviceID string) (visible bool, exists bool)
	// IsDeviceOwner 判定指定用户是否为该设备的所有者
	IsDeviceOwner(userID string, deviceID string) bool
	// HasDevicePermission 判定指定用户对该设备是否具备特定操作权限（基于设备授权级别）
	HasDevicePermission(userID string, deviceID string, perm Permission) bool
}

// Authorizer 统一执行基于角色矩阵与设备资源范围的授权判定
type Authorizer struct {
	resolver DeviceScopeResolver
}

// NewAuthorizer 初始化授权执行器
func NewAuthorizer(resolver DeviceScopeResolver) *Authorizer {
	return &Authorizer{resolver: resolver}
}

// SetResolver 动态设置设备范围解析器
func (a *Authorizer) SetResolver(resolver DeviceScopeResolver) {
	a.resolver = resolver
}

// Authorize 执行统一授权判定流程
func (a *Authorizer) Authorize(req AuthorizationRequest) Decision {
	// 1. 校验 Actor 身份合法性
	if req.Actor.UserID == "" || !IsValidRole(req.Actor.Role) {
		return Decision{
			Allowed:    false,
			StatusCode: http.StatusUnauthorized,
			ErrorCode:  "unauthorized",
			ReasonCode: "invalid_or_missing_actor",
		}
	}

	// 2. 校验角色是否包含该权限常量
	hasRolePerm := RoleHasPermission(req.Actor.Role, req.Permission)

	// 3. 处理设备资源访问
	if req.Resource.Kind == ResourceKindDevice {
		deviceID := req.Resource.ID
		if deviceID == "" {
			return Decision{
				Allowed:    false,
				StatusCode: http.StatusBadRequest,
				ErrorCode:  "invalid_request",
				ReasonCode: "missing_device_id",
			}
		}

		// 若未配置解析器（如部分单元测试），默认在角色有权限时放行
		if a.resolver != nil {
			// Owner 角色对系统中存在的全部设备拥有完全访问与操作权限
			if req.Actor.Role == RoleOwner {
				_, exists := a.resolver.IsDeviceVisible(req.Actor.UserID, deviceID)
				if !exists {
					return Decision{
						Allowed:    false,
						StatusCode: http.StatusNotFound,
						ErrorCode:  "resource_not_found",
						ReasonCode: "device_not_found",
					}
				}
				if !hasRolePerm {
					return Decision{
						Allowed:    false,
						StatusCode: http.StatusForbidden,
						ErrorCode:  "forbidden",
						ReasonCode: "role_permission_denied",
					}
				}
				return Decision{
					Allowed:    true,
					StatusCode: http.StatusOK,
					ReasonCode: "authorized",
				}
			}

			// Admin / Viewer 角色：
			visible, exists := a.resolver.IsDeviceVisible(req.Actor.UserID, deviceID)
			// 防 IDOR 探测：设备不存在或对该用户不可见时，统一返回 404
			if !exists || !visible {
				return Decision{
					Allowed:    false,
					StatusCode: http.StatusNotFound,
					ErrorCode:  "resource_not_found",
					ReasonCode: "device_not_found_or_unauthorized",
				}
			}

			// 设备可见，但角色无此基础权限（例如 Viewer 试图关机）：返回 403
			if !hasRolePerm {
				return Decision{
					Allowed:    false,
					StatusCode: http.StatusForbidden,
					ErrorCode:  "forbidden",
					ReasonCode: "role_permission_denied",
				}
			}

			// 检查设备级细粒度权限：
			// 删除与共享设备必须是设备的所有者 (Owner)
			if req.Permission == PermDevicesDelete || req.Permission == PermDevicesShare {
				if !a.resolver.IsDeviceOwner(req.Actor.UserID, deviceID) {
					return Decision{
						Allowed:    false,
						StatusCode: http.StatusForbidden,
						ErrorCode:  "forbidden",
						ReasonCode: "device_ownership_required",
					}
				}
			} else if !a.resolver.HasDevicePermission(req.Actor.UserID, deviceID, req.Permission) {
				// 设备更新/操作/告警管理等需检查 Grant 授权级别
				return Decision{
					Allowed:    false,
					StatusCode: http.StatusForbidden,
					ErrorCode:  "forbidden",
					ReasonCode: "device_grant_insufficient",
				}
			}

			return Decision{
				Allowed:    true,
				StatusCode: http.StatusOK,
				ReasonCode: "authorized",
			}
		}

		// 无 resolver 时的回退逻辑
		if !hasRolePerm {
			return Decision{
				Allowed:    false,
				StatusCode: http.StatusForbidden,
				ErrorCode:  "forbidden",
				ReasonCode: "role_permission_denied",
			}
		}
		return Decision{
			Allowed:    true,
			StatusCode: http.StatusOK,
			ReasonCode: "authorized",
		}
	}

	// 4. 处理用户账号级资源访问
	if req.Resource.Kind == ResourceKindUser {
		targetUserID := req.Resource.ID
		if !hasRolePerm {
			return Decision{
				Allowed:    false,
				StatusCode: http.StatusForbidden,
				ErrorCode:  "forbidden",
				ReasonCode: "role_permission_denied",
			}
		}
		// 非 Owner 角色仅允许访问与操作自己的用户记录
		if req.Actor.Role != RoleOwner && targetUserID != "" && targetUserID != req.Actor.UserID {
			return Decision{
				Allowed:    false,
				StatusCode: http.StatusForbidden,
				ErrorCode:  "forbidden",
				ReasonCode: "cannot_access_other_users",
			}
		}
		return Decision{
			Allowed:    true,
			StatusCode: http.StatusOK,
			ReasonCode: "authorized",
		}
	}

	// 5. 处理全局资源访问
	if !hasRolePerm {
		return Decision{
			Allowed:    false,
			StatusCode: http.StatusForbidden,
			ErrorCode:  "forbidden",
			ReasonCode: "role_permission_denied",
		}
	}

	return Decision{
		Allowed:    true,
		StatusCode: http.StatusOK,
		ReasonCode: "authorized",
	}
}
