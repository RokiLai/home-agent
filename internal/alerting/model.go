// Package alerting 提供设备异常告警去重、静默管理、多通道通知分发与投递重试。
package alerting

import (
	"crypto/sha256"
	"fmt"
	"time"

	"homeagent/internal/health"
)

// AlertState 表示告警实例的当前生命周期状态。
type AlertState string

const (
	AlertFiring   AlertState = "firing"
	AlertResolved AlertState = "resolved"
)

// Alert 代表一个唯一的告警实例（由规范化 Fingerprint 标识）。
type Alert struct {
	ID                   string          `json:"id"`
	Fingerprint          string          `json:"fingerprint"`
	DeviceID             string          `json:"device_id"`
	ReasonCode           string          `json:"reason_code"`
	Severity             health.Severity `json:"severity"`
	State                AlertState      `json:"state"`
	OpenedAt             time.Time       `json:"opened_at"`
	LastObservedAt       time.Time       `json:"last_observed_at"`
	ResolvedAt           *time.Time      `json:"resolved_at,omitempty"`
	Summary              string          `json:"summary"`
	SuggestedAction      string          `json:"suggested_action"`
	Evidence             map[string]any  `json:"evidence,omitempty"`
	FiringNotified       bool            `json:"firing_notified"`
	NotificationRevision uint64          `json:"notification_revision"`
}

// ComputeFingerprint 计算设备告警的规范化唯一哈希标识。
func ComputeFingerprint(deviceID, reasonCode string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s", deviceID, reasonCode)))
	return fmt.Sprintf("fp_%x", sum[:12])
}

// Silence 表示一个有效时间窗口内的告警静默规则。
type Silence struct {
	ID         string    `json:"id"`
	DeviceID   string    `json:"device_id,omitempty"`   // 为空表示适用于所有设备
	ReasonCode string    `json:"reason_code,omitempty"` // 为空表示适用于所有原因码
	StartsAt   time.Time `json:"starts_at"`
	EndsAt     time.Time `json:"ends_at"`
	CreatedBy  string    `json:"created_by"`
	Comment    string    `json:"comment"`
	CreatedAt  time.Time `json:"created_at"`
}

// Matches 判断指定的设备与原因码在指定时间是否命中该静默规则。
func (s *Silence) Matches(deviceID, reasonCode string, at time.Time) bool {
	if at.Before(s.StartsAt) || at.After(s.EndsAt) {
		return false
	}
	if s.DeviceID != "" && s.DeviceID != deviceID {
		return false
	}
	if s.ReasonCode != "" && s.ReasonCode != reasonCode {
		return false
	}
	return true
}

// DeliveryAttempt 记录单次通知投递尝试的审计结果。
type DeliveryAttempt struct {
	ID            string     `json:"id"`
	AlertID       string     `json:"alert_id"`
	ChannelID     string     `json:"channel_id"`
	Event         string     `json:"event"`
	DeliveryID    string     `json:"delivery_id"`
	AttemptNumber int        `json:"attempt_number"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    time.Time  `json:"finished_at"`
	StatusCode    int        `json:"status_code"`
	ErrorCode     string     `json:"error_code,omitempty"`
	ErrorMessage  string     `json:"error_message,omitempty"`
	NextRetryAt   *time.Time `json:"next_retry_at,omitempty"`
	Delivered     bool       `json:"delivered"`
}

// Notification 封装投递给各通道的标准化告警载荷。
type Notification struct {
	SchemaVersion int             `json:"schema_version"`
	Event         string          `json:"event"` // alert.firing, alert.resolved, alert.test
	DeliveryID    string          `json:"delivery_id"`
	SentAt        time.Time       `json:"sent_at"`
	Alert         NotificationAlert `json:"alert"`
	Device        NotificationDev `json:"device"`
}

// NotificationAlert 包含脱敏后的告警详情。
type NotificationAlert struct {
	ID              string          `json:"id"`
	Status          string          `json:"status"` // firing, resolved
	Severity        health.Severity `json:"severity"`
	ReasonCode      string          `json:"reason_code"`
	Summary         string          `json:"summary"`
	OpenedAt        time.Time       `json:"opened_at"`
	ResolvedAt      *time.Time      `json:"resolved_at,omitempty"`
	SuggestedAction string          `json:"suggested_action"`
}

// NotificationDev 包含脱敏后的设备上下文信息。
type NotificationDev struct {
	ID           string        `json:"id"`
	DisplayName  string        `json:"display_name"`
	HealthStatus health.Status `json:"health_status"`
	LastSeenAt   *time.Time    `json:"last_seen_at,omitempty"`
}

