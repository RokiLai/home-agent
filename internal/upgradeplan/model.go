// Package upgradeplan 实现了两跳升级编排状态机（UpgradePlan）与持久化存储服务。
package upgradeplan

import (
	"errors"
	"time"

	"homeagent/internal/command"
)

// PlanStage 标识 UpgradePlan 阶段全集枚举。
type PlanStage string

const (
	StageBridgePending  PlanStage = "bridge_pending"
	StageBridgeRunning  PlanStage = "bridge_running"
	StageCapabilityWait PlanStage = "capability_wait"
	StageTargetPending  PlanStage = "target_pending"
	StageTargetRunning  PlanStage = "target_running"
	StageSucceeded      PlanStage = "succeeded"
	StageFailed         PlanStage = "failed"
	StageCanceled       PlanStage = "canceled"
)

func (s PlanStage) Terminal() bool {
	switch s {
	case StageSucceeded, StageFailed, StageCanceled:
		return true
	default:
		return false
	}
}

var (
	ErrPlanNotFound            = errors.New("upgrade plan not found")
	ErrPlanConflict            = errors.New("upgrade plan conflict")
	ErrPlanInProgress          = errors.New("upgrade plan already in progress for device")
	ErrIdempotencyConflict     = errors.New("idempotency conflict on upgrade plan")
	ErrInvalidPlanTransition   = errors.New("invalid upgrade plan transition")
	ErrV2UpgradeUnavailable    = errors.New("v2_upgrade_temporarily_unavailable")
	ErrV2DisabledBeforeTarget  = errors.New("v2_disabled_before_target_dispatch")
)

// PlanSnapshot 记录创建 Plan 时冻结的目标与环境快照。
type PlanSnapshot struct {
	TargetVersion        string `json:"target_version"`
	TargetURL            string `json:"target_url,omitempty"`
	TargetSHA256         string `json:"target_sha256,omitempty"`
	TargetManifestDigest string `json:"target_manifest_digest,omitempty"`
	BridgeVersion        string `json:"bridge_version,omitempty"`
	BridgeURL            string `json:"bridge_url,omitempty"`
	BridgeSHA256         string `json:"bridge_sha256,omitempty"`
	InitialSecurityMode  string `json:"initial_security_mode,omitempty"`
	InitialProtocols     []int  `json:"initial_protocols,omitempty"`
	RequestDigest        string `json:"request_digest,omitempty"`
}

// UpgradePlan 表示两跳升级编排的持久化聚合实体。
type UpgradePlan struct {
	PlanID               string        `json:"plan_id"`
	DeviceID             string        `json:"device_id"`
	RequestedBy          command.Actor `json:"requested_by"`
	IdempotencyKey       string        `json:"idempotency_key,omitempty"`
	TargetVersion        string        `json:"target_version"`
	TargetManifestDigest *string       `json:"target_manifest_digest,omitempty"`
	BridgeVersion        *string       `json:"bridge_version,omitempty"`
	BridgeCommandID      *string       `json:"bridge_command_id,omitempty"`
	TargetCommandID      *string       `json:"target_command_id,omitempty"`
	Stage                PlanStage     `json:"stage"`
	FailureReason        string        `json:"failure_reason,omitempty"`
	CreatedAt            time.Time     `json:"created_at"`
	UpdatedAt            time.Time     `json:"updated_at"`
	Revision             uint64        `json:"revision"`
	Snapshot             PlanSnapshot  `json:"snapshot"`
}

// Filter 描述 UpgradePlan 查询过滤条件。
type Filter struct {
	DeviceID string
	Stage    PlanStage
	Limit    int
}
