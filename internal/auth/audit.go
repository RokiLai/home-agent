package auth

import (
	"log/slog"
	"sync"
	"time"
)

// Action 常量定义敏感安全审计动作
const (
	ActionAuthLogin         = "auth.login"
	ActionAuthLogout        = "auth.logout"
	ActionAuthLogoutAll     = "auth.logout_all"
	ActionAuthChangePass    = "auth.change_password"
	ActionUserCreate        = "user.create"
	ActionUserUpdateRole    = "user.update_role"
	ActionUserDisable       = "user.disable"
	ActionUserEnable        = "user.enable"
	ActionUserResetPass     = "user.reset_password"
	ActionUserDelete        = "user.delete"
	ActionDeviceGrant       = "device.grant"
	ActionDeviceRevokeGrant = "device.revoke_grant"
	ActionDeviceTransfer    = "device.transfer"
	ActionDeviceDelete      = "device.delete"
	ActionDeviceUpgradeAll  = "device.upgrade_all"
	ActionDeviceSyncAll     = "device.sync_all"
)

// AuditEvent 表示一条结构化安全审计事件记录
type AuditEvent struct {
	Timestamp    time.Time `json:"timestamp"`
	ActorUserID  string    `json:"actor_user_id"`
	ActorRole    Role      `json:"actor_role"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	ClientIP     string    `json:"client_ip"`
	Status       string    `json:"status"` // "success", "denied", "failed"
	Detail       string    `json:"detail,omitempty"`
}

// AuditLogger 定义安全审计日志记录接口
type AuditLogger interface {
	Record(event AuditEvent)
	Recent(limit int) []AuditEvent
}

// MemoryAuditLogger 提供线程安全的内存环形审计日志缓冲与 slog 输出
type MemoryAuditLogger struct {
	mu       sync.RWMutex
	capacity int
	events   []AuditEvent
}

// NewMemoryAuditLogger 创建指定容量的内存环形审计记录器
func NewMemoryAuditLogger(capacity int) *MemoryAuditLogger {
	if capacity <= 0 {
		capacity = 500
	}
	return &MemoryAuditLogger{
		capacity: capacity,
		events:   make([]AuditEvent, 0, capacity),
	}
}

// Record 记录一条审计事件并输出结构化 slog 日志
func (l *MemoryAuditLogger) Record(event AuditEvent) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	// 1. 结构化日志输出
	slog.Info("security_audit_event",
		"actor_user_id", event.ActorUserID,
		"actor_role", event.ActorRole,
		"action", event.Action,
		"resource_type", event.ResourceType,
		"resource_id", event.ResourceID,
		"client_ip", event.ClientIP,
		"status", event.Status,
		"detail", event.Detail,
	)

	// 2. 环形缓冲持久存储
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.events) >= l.capacity {
		// 丢弃最早一条
		l.events = l.events[1:]
	}
	l.events = append(l.events, event)
}

// Recent 返回最近发生的 N 条审计事件（按发生时间倒序排列）
func (l *MemoryAuditLogger) Recent(limit int) []AuditEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if limit <= 0 || limit > len(l.events) {
		limit = len(l.events)
	}

	result := make([]AuditEvent, limit)
	total := len(l.events)
	for i := 0; i < limit; i++ {
		result[i] = l.events[total-1-i]
	}
	return result
}
