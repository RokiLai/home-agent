// Package upgrade 实现了 macOS 及跨平台客户端升级引擎协议 v2 核心逻辑，
// 包括 Ed25519 门限验签、规范归档打包与解包、持久化事务日志（HAUPJNL1）、
// Fenced 两阶段收敛状态机与独立恢复器通信接口。
package upgrade

import (
	"fmt"
)

// SwitchMode 标识 macOS 下 App 升级切换模式。
type SwitchMode uint64

const (
	SwitchModeLegacyMigration SwitchMode = 1 // 首次从旧路径迁移（更新 LaunchAgent plist）
	SwitchModeReleaseSwitch   SwitchMode = 2 // 规范 release 软链接切换
)

// SecurityMode 标识 Agent 当前防降级安全模式。
type SecurityMode uint64

const (
	SecurityModeUnlocked SecurityMode = 1 // 初始未锁定模式
	SecurityModeV2Locked SecurityMode = 2 // 已锁定 v2 协议，拒绝任何 v1 降级
)

func (s SecurityMode) String() string {
	switch s {
	case SecurityModeV2Locked:
		return "v2_locked"
	default:
		return "unlocked"
	}
}

// SideEffect 标识升级过程中的单步副作用意图。
type SideEffect uint64

const (
	SideEffectStageRelease       SideEffect = 1
	SideEffectInstallRecovery    SideEffect = 2
	SideEffectBootstrapRecovery  SideEffect = 3
	SideEffectBootoutOldJob      SideEffect = 4
	SideEffectWritePlist         SideEffect = 5
	SideEffectSwitchCurrent      SideEffect = 6
	SideEffectBootstrapNewJob    SideEffect = 7
	SideEffectRollbackLink       SideEffect = 8
	SideEffectRollbackPlist      SideEffect = 9
	SideEffectBootstrapOldJob    SideEffect = 10
)

// UpgradePhase 标识升级过程对外公开的阶段事件枚举。
type UpgradePhase string

const (
	PhaseAccepted    UpgradePhase = "accepted"
	PhaseDownloading UpgradePhase = "downloading"
	PhaseVerifying   UpgradePhase = "verifying"
	PhaseInstalling  UpgradePhase = "installing"
	PhaseInstalled   UpgradePhase = "installed"
	PhaseRestarting  UpgradePhase = "restarting"
	PhaseCommitReady UpgradePhase = "commit_ready"
	PhaseConverged   UpgradePhase = "converged"
	PhaseSkipped     UpgradePhase = "skipped"
	PhaseRolledBack  UpgradePhase = "rolled_back"
	PhaseFailed      UpgradePhase = "failed"
)

// PhaseID 将公开阶段转换为内部固定序号。
func (p UpgradePhase) PhaseID() uint64 {
	switch p {
	case PhaseAccepted:
		return 1
	case PhaseDownloading:
		return 2
	case PhaseVerifying:
		return 3
	case PhaseInstalling:
		return 4
	case PhaseInstalled:
		return 5
	case PhaseRestarting:
		return 6
	case PhaseCommitReady:
		return 7
	case PhaseConverged:
		return 8
	case PhaseSkipped:
		return 9
	case PhaseRolledBack:
		return 10
	case PhaseFailed:
		return 11
	default:
		return 0
	}
}

// State 标识事务日志（HAUPJNL1）中的完整内部状态枚举（1..25）。
type State uint64

const (
	StateAccepted               State = 1
	StateDownloading            State = 2
	StateVerifying              State = 3
	StateInstalling             State = 4
	StateInstalledRecovery      State = 5
	StateRecoveryReady          State = 6
	StateHandoffRequested       State = 7
	StateWroteNewPlist          State = 8
	StateSwitchedCurrent        State = 9
	StateBootedNewJob           State = 10
	StateRestartedJob           State = 11
	StateInstalled              State = 12
	StateLocalReady             State = 13
	StateAwaitingControl        State = 14
	StateConvergencePrepared    State = 15
	StateCommitReady            State = 16
	StateCommitted              State = 17
	StateCommittedLate          State = 18
	StateControlRejected        State = 19
	StateControlProcessLost     State = 20
	StateRolledBack             State = 21
	StateRollbackFailed         State = 22
	StateClosed                 State = 23
	StateClosedLate             State = 24
	StateManualRecoveryRequired State = 25
)

// RecordTag 标识事务日志中记录类型的 Tag 枚举（1..13）。
type RecordTag uint16

const (
	TagTransactionCreated   RecordTag = 1
	TagPhase                RecordTag = 2
	TagSideEffectIntent     RecordTag = 3
	TagSideEffectDone       RecordTag = 4
	TagPendingEvent         RecordTag = 5
	TagEventDelivered       RecordTag = 6
	TagFenceCreated         RecordTag = 7
	TagConvergencePrepared  RecordTag = 8
	TagCommitReady          RecordTag = 9
	TagCommitted            RecordTag = 10
	TagRollback             RecordTag = 11
	TagSecurityUpdate       RecordTag = 12
	TagTerminal             RecordTag = 13
)

// 结构化错误代码常量。
const (
	ErrCodeArtifactDownloadFailed = "artifact_download_failed"
	ErrCodeArtifactSizeMismatch   = "artifact_size_mismatch"
	ErrCodeArtifactHashMismatch   = "artifact_hash_mismatch"
	ErrCodeArchiveUnsafePath      = "archive_unsafe_path"
	ErrCodeArchiveInvalidLayout   = "archive_invalid_layout"
	ErrCodeSignatureInvalid       = "signature_invalid"
	ErrCodeNotarizationInvalid    = "notarization_invalid"
	ErrCodeIdentityMismatch       = "identity_mismatch"
	ErrCodeCandidateSmokeFailed   = "candidate_smoke_failed"
	ErrCodeInstallBackupFailed    = "install_backup_failed"
	ErrCodeInstallReplaceFailed   = "install_replace_failed"
	ErrCodeRestartNotConverged    = "restart_not_converged"
	ErrCodeRollbackFailed         = "rollback_failed"
	ErrCodeProtocolDowngrade      = "upgrade_protocol_downgrade_rejected"
)

// StructuredError 表示带阶段和错误码的结构化错误。
type StructuredError struct {
	CommandID string       `json:"command_id"`
	Phase     UpgradePhase `json:"phase"`
	Code      string       `json:"code"`
	Message   string       `json:"message"`
}

func (e *StructuredError) Error() string {
	return fmt.Sprintf("[%s/%s] %s: %s", e.CommandID, e.Phase, e.Code, e.Message)
}
