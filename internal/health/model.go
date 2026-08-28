// Package health 提供设备健康状态评估、历史事件跟踪与健康快照持久化模型。
package health

import (
	"time"
)

// Status 表示设备的综合健康状态。
type Status string

const (
	StatusHealthy  Status = "healthy"
	StatusDegraded Status = "degraded"
	StatusOffline  Status = "offline"
	StatusUnknown  Status = "unknown"
)

// Severity 表示异常原因的严重程度。
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// RuleState 表示单条健康规则的评估状态。
type RuleState string

const (
	RuleOK       RuleState = "ok"
	RuleFiring   RuleState = "firing"
	RuleUnknown  RuleState = "unknown"
	RuleDisabled RuleState = "disabled"
)

// RuntimeFacts 包含 Agent 上报的最小运行状态事实（内存、磁盘、负载、运行时间等）。
type RuntimeFacts struct {
	ObservedAt           time.Time `json:"observed_at"`
	UptimeSeconds        int64     `json:"uptime_seconds,omitempty"`
	Load1                *float64  `json:"load_1,omitempty"`
	LogicalCPUCount      int       `json:"logical_cpu_count,omitempty"`
	MemoryTotalBytes     uint64    `json:"memory_total_bytes,omitempty"`
	MemoryAvailableBytes uint64    `json:"memory_available_bytes,omitempty"`
	DiskTotalBytes       uint64    `json:"disk_total_bytes,omitempty"`
	DiskAvailableBytes   uint64    `json:"disk_available_bytes,omitempty"`
	DiskMount            string    `json:"disk_mount,omitempty"`
}

// Reason 描述导致设备处于非健康或需关注状态的具体规则原因。
type Reason struct {
	Code            string         `json:"code"`
	State           RuleState      `json:"state"`
	Severity        Severity       `json:"severity"`
	Summary         string         `json:"summary"`
	FirstObservedAt time.Time      `json:"first_observed_at"`
	LastObservedAt  time.Time      `json:"last_observed_at"`
	SuggestedAction string         `json:"suggested_action"`
	Evidence        map[string]any `json:"evidence,omitempty"`
}

// HealthSnapshot 代表设备当前计算得出的权威健康快照。
type HealthSnapshot struct {
	DeviceID    string        `json:"device_id"`
	Status      Status        `json:"status"`
	Connected   bool          `json:"connected"`
	EvaluatedAt time.Time     `json:"evaluated_at"`
	LastSeenAt  *time.Time    `json:"last_seen_at,omitempty"`
	Since       time.Time     `json:"since"`
	RuleVersion int           `json:"rule_version"`
	Reasons     []Reason      `json:"reasons"`
	Facts       *RuntimeFacts `json:"facts,omitempty"`
	Revision    uint64        `json:"revision"`
}

// HealthEvent 表示健康状态或具体规则状态发生变更时记录的持久化历史事件。
type HealthEvent struct {
	ID               string         `json:"id"`
	DeviceID         string         `json:"device_id"`
	Type             string         `json:"type"` // opened, changed, resolved
	ReasonCode       string         `json:"reason_code"`
	FromState        RuleState      `json:"from_state"`
	ToState          RuleState      `json:"to_state"`
	Severity         Severity       `json:"severity"`
	OccurredAt       time.Time      `json:"occurred_at"`
	RuleVersion      int            `json:"rule_version"`
	Evidence         map[string]any `json:"evidence,omitempty"`
	SnapshotRevision uint64         `json:"snapshot_revision"`
}

// Summary 表示全局设备健康状态的统计摘要。
type Summary struct {
	Total       int `json:"total"`
	Healthy     int `json:"healthy"`
	Degraded    int `json:"degraded"`
	Offline     int `json:"offline"`
	Unknown     int `json:"unknown"`
	UnhealthyCount int `json:"unhealthy_count"`
}

