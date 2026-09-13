package device

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"time"
)

// GrantLevel 定义设备共享授权级别枚举
type GrantLevel string

const (
	// GrantLevelRead 只读权限：允许查看设备信息、健康状态、告警与命令历史
	GrantLevelRead GrantLevel = "read"
	// GrantLevelOperate 操作权限：包含只读，另允许同步、唤醒、关机、升级及取消命令
	GrantLevelOperate GrantLevel = "operate"
	// GrantLevelManage 管理权限：包含操作，另允许修改别名等元数据及设备级告警管理
	GrantLevelManage GrantLevel = "manage"
)

// IsValidGrantLevel 校验授权级别是否合法
func IsValidGrantLevel(level GrantLevel) bool {
	switch level {
	case GrantLevelRead, GrantLevelOperate, GrantLevelManage:
		return true
	default:
		return false
	}
}

var (
	// ErrInvalidGrantLevel 表示无效的授权级别
	ErrInvalidGrantLevel = errors.New("invalid grant level: must be read, operate, or manage")
	// ErrGrantToOwner 表示禁止向设备所有者创建冗余授权记录
	ErrGrantToOwner = errors.New("cannot create grant for device owner")
	// ErrGrantNotFound 表示指定的授权记录不存在
	ErrGrantNotFound = errors.New("device grant not found")
)

// DeviceGrant 表示单台设备向某个用户授予的资源访问权限记录
type DeviceGrant struct {
	ID        string     `json:"id"`
	DeviceID  string     `json:"device_id"`
	UserID    string     `json:"user_id"`
	Level     GrantLevel `json:"level"`
	GrantedBy string     `json:"granted_by"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	Revision  uint64     `json:"revision"`
}

// GenerateGrantID 生成唯一授权标识符 (grant_...)
func GenerateGrantID() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().Nanosecond())))
	return fmt.Sprintf("grant_%x", sum[:8])
}
