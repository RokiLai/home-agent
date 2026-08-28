package auth

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Role 定义系统固定角色枚举
type Role string

const (
	// RoleOwner 实例所有者：拥有全实例管理权限
	RoleOwner Role = "owner"
	// RoleAdmin 设备管理员：管理获授权设备
	RoleAdmin Role = "admin"
	// RoleViewer 只读查看者：仅可查看获授权设备
	RoleViewer Role = "viewer"
)

// IsValidRole 校验角色是否属于固定三角色之一
func IsValidRole(r Role) bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleViewer:
		return true
	default:
		return false
	}
}

// UserStatus 定义账号生命周期状态
type UserStatus string

const (
	// UserStatusActive 活跃启用状态
	UserStatusActive UserStatus = "active"
	// UserStatusDisabled 已禁用状态
	UserStatusDisabled UserStatus = "disabled"
)

// IsValidUserStatus 校验用户状态是否合法
func IsValidUserStatus(s UserStatus) bool {
	switch s {
	case UserStatusActive, UserStatusDisabled:
		return true
	default:
		return false
	}
}

var (
	// ErrLastOwnerRequired 表示禁止禁用、删除或降级系统中最后一个启用的 owner
	ErrLastOwnerRequired = errors.New("cannot disable, delete, or demote the last active owner")
	// ErrUsernameConflict 表示用户名在实例内已存在
	ErrUsernameConflict = errors.New("username already exists")
	// ErrUserNotFound 表示用户不存在
	ErrUserNotFound = errors.New("user not found")
	// ErrUserDisabled 表示用户账号已禁用
	ErrUserDisabled = errors.New("user account is disabled")
	// ErrInvalidRole 表示未知的角色枚举
	ErrInvalidRole = errors.New("invalid role")
	// ErrInvalidUsername 表示用户名格式不符合安全规范
	ErrInvalidUsername = errors.New("invalid username: must be 3-32 chars matching ^[a-zA-Z0-9_.@-]{3,32}$")
	// ErrPasswordTooShort 表示密码长度不足
	ErrPasswordTooShort = errors.New("password must be at least 6 characters")
)

var usernameRegex = regexp.MustCompile(`^[a-zA-Z0-9_.@-]{3,32}$`)

// NormalizeUsernameKey 计算用于大小写折叠唯一性比较的用户名键
func NormalizeUsernameKey(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// ValidateUsernameFormat 校验用户名字符合法性与长度约束
func ValidateUsernameFormat(username string) error {
	trimmed := strings.TrimSpace(username)
	if len(trimmed) < 3 || len(trimmed) > 32 {
		return ErrInvalidUsername
	}
	if !usernameRegex.MatchString(trimmed) {
		return ErrInvalidUsername
	}
	return nil
}

// User 保存服务端独立用户账号的模型
type User struct {
	ID             string     `json:"id"`
	Username       string     `json:"username"`
	UsernameKey    string     `json:"username_key"`
	PasswordHash   string     `json:"password_hash"`
	Role           Role       `json:"role"`
	Status         UserStatus `json:"status"`
	SessionVersion uint64     `json:"session_version"`
	CreatedBy      string     `json:"created_by,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	DisabledAt     *time.Time `json:"disabled_at,omitempty"`
	Revision       uint64     `json:"revision"`
}

// GenerateUserID 生成高强度随机用户唯一标识符 (usr_...)
func GenerateUserID() string {
	token, err := GenerateSecureToken("usr_", 16)
	if err != nil {
		// 备选安全回退
		return fmt.Sprintf("usr_%d", time.Now().UnixNano())
	}
	return token
}
