// Package health 定义健康服务所需的外部依赖端口。
package health

import (
	"context"
	"time"
)

// Clock 提供可替换的时间获取接口。
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now().UTC()
}

// NewRealClock 返回基于真实 UTC 时间的时钟实现。
func NewRealClock() Clock {
	return realClock{}
}

// ClockFunc 允许以函数形式定义 Clock。
type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time {
	return f()
}

// DeviceFactSummary 包含单台设备参与健康评估所需的事实信息。
type DeviceFactSummary struct {
	ID                string
	Hostname          string
	AgentVersion      string
	OS                string
	Arch              string
	LastSeenAt        time.Time
	Connected         bool
	SyncStatus        string
	AppliedVersion    int64
	AppliedHash       string
	SyncError         string
	SyncUpdatedAt     time.Time
	GitHubSyncEnabled bool
	GitHubStatus      string
	RuntimeFacts      *RuntimeFacts
}

// DeviceFactsPort 提供查询设备基础事实的端口契约。
type DeviceFactsPort interface {
	GetDeviceFacts(ctx context.Context, deviceID string) (*DeviceFactSummary, error)
	ListAllDeviceIDs(ctx context.Context) ([]string, error)
}

// SSHDesiredPort 提供查询 SSH KeySet 期望状态的端口契约。
type SSHDesiredPort interface {
	GetDesiredKeySet(ctx context.Context) (version int64, hash string, enabled bool, err error)
}

// DDNSDeviceState 包含单台设备 DDNS 同步的事实信息。
type DDNSDeviceState struct {
	Enabled       bool
	SyncStatus    string
	SyncError     string
	DesiredIPv6   string
	AppliedIPv6   string
	InGracePeriod bool
	GraceUntil    time.Time
	PrefixStale   bool
}

// DDNSPort 提供查询设备 DDNS 状态的端口契约。
type DDNSPort interface {
	GetDeviceDDNSState(ctx context.Context, deviceID string) (*DDNSDeviceState, error)
}

// CommandSummary 包含设备最近命令的执行摘要。
type CommandSummary struct {
	CommandID  string
	Kind       string
	Status     string
	CreatedAt  time.Time
	FinishedAt *time.Time
	ErrorCode  string
}

// CommandPort 提供查询设备命令执行状态的端口契约。
type CommandPort interface {
	GetLatestCommand(ctx context.Context, deviceID string, kind string) (*CommandSummary, error)
}

// VersionPolicyPort 提供版本基线与推荐策略端口。
type VersionPolicyPort interface {
	GetVersionPolicy(ctx context.Context) (recommended string, minSupported string, err error)
}

