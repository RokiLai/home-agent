// Package store 定义 HomeAgent 的统一存储抽象提供者（Storage Provider）接口
package store

import (
	"errors"

	"homeagent/internal/auth"
	"homeagent/internal/device"
)

var (
	// ErrNotFound 表示请求的资源不存在
	ErrNotFound = errors.New("resource not found")
	// ErrConflict 表示资源已存在或唯一性冲突
	ErrConflict = errors.New("resource already exists")
	// ErrLastOwnerRequired 表示不能删除或降级最后一位活跃 Owner
	ErrLastOwnerRequired = errors.New("cannot remove or demote the last active owner")
)

// UserStore 定义用户管理与凭据持久化接口
type UserStore interface {
	GetUser(id string) (*auth.User, error)
	GetUserByUsernameKey(key string) (*auth.User, error)
	ListUsers() ([]*auth.User, error)
	SaveUser(user *auth.User) error
	DeleteUser(id string) error
	CountActiveOwners() (int, error)
}

// SessionStore 定义会话管理持久化接口
type SessionStore interface {
	GetSession(tokenHash string) (*auth.Session, error)
	SaveSession(session *auth.Session) error
	DeleteSession(tokenHash string) error
	DeleteSessionsByUser(userID string) error
	CleanExpired() error
}

// DeviceStore 定义设备资产与共享授权持久化接口
type DeviceStore interface {
	GetDevice(id string) (*device.Device, error)
	ListDevices() ([]*device.Device, error)
	SaveDevice(dev *device.Device) error
	DeleteDevice(id string) error
	DeleteDevicesByOwner(ownerUserID string) ([]string, error)

	// Grants 共享授权
	ListGrants(deviceID string) ([]*device.DeviceGrant, error)
	GetGrant(deviceID, userID string) (*device.DeviceGrant, error)
	SaveGrant(grant *device.DeviceGrant) error
	DeleteGrant(deviceID, userID string) error
	DeleteGrantsByDevice(deviceID string) error
	DeleteGrantsByUser(userID string) error
}

// EnrollmentStore 定义 Claim Token 认领凭据持久化接口
type EnrollmentStore interface {
	GetClaimToken(tokenHash string) (*auth.ClaimToken, error)
	ListClaimTokens(ownerUserID string) ([]*auth.ClaimToken, error)
	SaveClaimToken(token *auth.ClaimToken) error
	DeleteClaimToken(tokenHash string) error
}

// AuditStore 定义安全审计日志持久化接口
type AuditStore interface {
	Record(event auth.AuditEvent) error
	Recent(limit int) ([]auth.AuditEvent, error)
}
