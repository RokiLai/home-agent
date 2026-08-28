// Package health 实现纯函数式健康状态评估器与多维度规则合并。
package health

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// EvaluatorConfig 包含健康评估的各项时间阈值与策略参数。
type EvaluatorConfig struct {
	StaleAfter                time.Duration // 默认 15m
	OfflineAfter              time.Duration // 默认 30m
	OfflinePendingFor         time.Duration // 默认 5m
	SSHDriftAfter             time.Duration // 默认 10m
	UpgradeConvergenceTimeout time.Duration // 默认 15m
	RuleVersion               int
}

// DefaultEvaluatorConfig 返回默认的健康评估配置。
func DefaultEvaluatorConfig() EvaluatorConfig {
	return EvaluatorConfig{
		StaleAfter:                15 * time.Minute,
		OfflineAfter:              30 * time.Minute,
		OfflinePendingFor:         5 * time.Minute,
		SSHDriftAfter:             10 * time.Minute,
		UpgradeConvergenceTimeout: 15 * time.Minute,
		RuleVersion:               1,
	}
}

// EvaluationInput 封装单次评估所需的所有输入事实。
type EvaluationInput struct {
	Device        *DeviceFactSummary
	SSHDesired    *SSHDesiredInfo
	DDNSState     *DDNSDeviceState
	UpgradeCmd    *CommandSummary
	SSHCmd        *CommandSummary
	VersionPolicy *VersionPolicyInfo
	Previous      *HealthSnapshot
	Now           time.Time
}

// SSHDesiredInfo 封装 SSH KeySet 的期望状态。
type SSHDesiredInfo struct {
	Version int64
	Hash    string
	Enabled bool
}

// VersionPolicyInfo 封装版本策略基线。
type VersionPolicyInfo struct {
	RecommendedVersion      string
	MinimumSupportedVersion string
}

// Evaluate 根据输入的事实与前序快照，纯函数式计算出最新健康快照与历史事件。
func Evaluate(cfg EvaluatorConfig, input EvaluationInput) (HealthSnapshot, []HealthEvent) {
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	deviceID := ""
	connected := false
	var lastSeen *time.Time
	var facts *RuntimeFacts

	if input.Device != nil {
		deviceID = input.Device.ID
		connected = input.Device.Connected
		if !input.Device.LastSeenAt.IsZero() {
			t := input.Device.LastSeenAt.UTC()
			lastSeen = &t
		}
		facts = input.Device.RuntimeFacts
	}

	var reasons []Reason

	// 1. 活跃度规则 (Liveness)
	if lastSeen == nil {
		reasons = append(reasons, Reason{
			Code:            "device_never_seen",
			State:           RuleUnknown,
			Severity:        SeverityInfo,
			Summary:         "从未收到已认证的设备活跃证据",
			FirstObservedAt: now,
			LastObservedAt:  now,
			SuggestedAction: "等待设备 Agent 启动并完成首次认证登记",
		})
	} else {
		age := now.Sub(*lastSeen)
		if age >= cfg.OfflineAfter {
			// 检查是否满足 offline_pending_for 防抖或前序已经是 offline
			offlineEligible := false
			if input.Previous != nil && input.Previous.Status == StatusOffline {
				offlineEligible = true
			} else {
				// 检查之前是否有 heartbeat_stale 且持续时间达到 pending
				firstStale := now
				if input.Previous != nil {
					for _, r := range input.Previous.Reasons {
						if r.Code == "heartbeat_stale" || r.Code == "device_offline" {
							firstStale = r.FirstObservedAt
							break
						}
					}
				}
				if now.Sub(firstStale) >= cfg.OfflinePendingFor || age >= cfg.OfflineAfter+cfg.OfflinePendingFor {
					offlineEligible = true
				}
			}

			if offlineEligible {
				reasons = append(reasons, Reason{
					Code:            "device_offline",
					State:           RuleFiring,
					Severity:        SeverityCritical,
					Summary:         fmt.Sprintf("设备超过 %d 分钟未上报心跳", int(cfg.OfflineAfter.Minutes())),
					FirstObservedAt: now,
					LastObservedAt:  now,
					SuggestedAction: "检查设备供电、网络和 Agent 守护进程状态",
					Evidence: map[string]any{
						"age_seconds":   int64(age.Seconds()),
						"last_seen_at":  lastSeen.Format(time.RFC3339),
						"threshold_min": int(cfg.OfflineAfter.Minutes()),
					},
				})
			} else {
				reasons = append(reasons, Reason{
					Code:            "heartbeat_stale",
					State:           RuleFiring,
					Severity:        SeverityWarning,
					Summary:         fmt.Sprintf("心跳陈旧，已持续 %d 分钟未收到活跃上报（正在确认离线）", int(age.Minutes())),
					FirstObservedAt: now,
					LastObservedAt:  now,
					SuggestedAction: "检查设备网络连通性",
					Evidence: map[string]any{
						"age_seconds":  int64(age.Seconds()),
						"last_seen_at": lastSeen.Format(time.RFC3339),
					},
				})
			}
		} else if age >= cfg.StaleAfter {
			reasons = append(reasons, Reason{
				Code:            "heartbeat_stale",
				State:           RuleFiring,
				Severity:        SeverityWarning,
				Summary:         fmt.Sprintf("心跳陈旧，已超过 %d 分钟未收到活跃上报", int(cfg.StaleAfter.Minutes())),
				FirstObservedAt: now,
				LastObservedAt:  now,
				SuggestedAction: "检查设备网络连通性或 Agent 服务运行状态",
				Evidence: map[string]any{
					"age_seconds":  int64(age.Seconds()),
					"last_seen_at": lastSeen.Format(time.RFC3339),
				},
			})
		}
	}

	// 2. 版本规则 (Agent Version)
	if input.Device != nil && input.VersionPolicy != nil && input.Device.AgentVersion != "" {
		v := input.Device.AgentVersion
		if !isValidSemVer(v) {
			reasons = append(reasons, Reason{
				Code:            "agent_version_invalid",
				State:           RuleUnknown,
				Severity:        SeverityInfo,
				Summary:         fmt.Sprintf("Agent 版本格式非法 (%s)，无法进行版本规则比较", v),
				FirstObservedAt: now,
				LastObservedAt:  now,
				SuggestedAction: "检查并规范化 Agent 版本号格式",
				Evidence: map[string]any{
					"raw_version": v,
				},
			})
		} else if input.VersionPolicy.MinimumSupportedVersion != "" && semverCompare(v, input.VersionPolicy.MinimumSupportedVersion) < 0 {
			reasons = append(reasons, Reason{
				Code:            "agent_version_outdated",
				State:           RuleFiring,
				Severity:        SeverityCritical,
				Summary:         fmt.Sprintf("Agent 版本 (%s) 低于最低支持版本 (%s)", v, input.VersionPolicy.MinimumSupportedVersion),
				FirstObservedAt: now,
				LastObservedAt:  now,
				SuggestedAction: "执行自升级命令或手动更新 Agent 到最新版本",
				Evidence: map[string]any{
					"current_version": v,
					"min_supported":   input.VersionPolicy.MinimumSupportedVersion,
				},
			})
		} else if input.VersionPolicy.RecommendedVersion != "" && semverCompare(v, input.VersionPolicy.RecommendedVersion) < 0 {
			reasons = append(reasons, Reason{
				Code:            "agent_version_outdated",
				State:           RuleFiring,
				Severity:        SeverityWarning,
				Summary:         fmt.Sprintf("Agent 版本 (%s) 落后于推荐版本 (%s)", v, input.VersionPolicy.RecommendedVersion),
				FirstObservedAt: now,
				LastObservedAt:  now,
				SuggestedAction: "执行自升级以获取最新功能与安全修复",
				Evidence: map[string]any{
					"current_version":     v,
					"recommended_version": input.VersionPolicy.RecommendedVersion,
				},
			})
		}
	}

	// 3. SSH 密钥同步规则
	if input.SSHDesired != nil && input.SSHDesired.Enabled && input.Device != nil {
		if input.Device.SyncStatus == "failed" || (input.SSHCmd != nil && (input.SSHCmd.Status == "failed" || input.SSHCmd.Status == "timed_out")) {
			errMsg := input.Device.SyncError
			if errMsg == "" && input.SSHCmd != nil {
				errMsg = input.SSHCmd.ErrorCode
			}
			reasons = append(reasons, Reason{
				Code:            "ssh_sync_failed",
				State:           RuleFiring,
				Severity:        SeverityWarning,
				Summary:         fmt.Sprintf("SSH 密钥同步失败: %s", errMsg),
				FirstObservedAt: now,
				LastObservedAt:  now,
				SuggestedAction: "检查设备 SSH 托管目录权限与网络状态，重新触发同步",
				Evidence: map[string]any{
					"sync_error": errMsg,
				},
			})
		} else if input.SSHDesired.Hash != "" && (input.Device.AppliedHash != input.SSHDesired.Hash || input.Device.AppliedVersion != input.SSHDesired.Version) {
			driftDuration := now.Sub(input.Device.SyncUpdatedAt)
			if input.Device.SyncUpdatedAt.IsZero() || driftDuration >= cfg.SSHDriftAfter {
				reasons = append(reasons, Reason{
					Code:            "ssh_key_drift",
					State:           RuleFiring,
					Severity:        SeverityWarning,
					Summary:         "SSH 密钥配置与期望状态不一致，存在漂移",
					FirstObservedAt: now,
					LastObservedAt:  now,
					SuggestedAction: "重新下发 SSH 密钥同步任务以收敛配置",
					Evidence: map[string]any{
						"desired_version": input.SSHDesired.Version,
						"applied_version": input.Device.AppliedVersion,
						"desired_hash":    input.SSHDesired.Hash,
						"applied_hash":    input.Device.AppliedHash,
					},
				})
			}
		}
	}

	// 4. DDNS 规则
	if input.DDNSState != nil && input.DDNSState.Enabled {
		if input.DDNSState.SyncStatus == "failed" {
			reasons = append(reasons, Reason{
				Code:            "ddns_sync_failed",
				State:           RuleFiring,
				Severity:        SeverityWarning,
				Summary:         fmt.Sprintf("DDNS 解析同步失败: %s", input.DDNSState.SyncError),
				FirstObservedAt: now,
				LastObservedAt:  now,
				SuggestedAction: "检查 DDNS API Token 权限与云服务商连通性",
				Evidence: map[string]any{
					"error": input.DDNSState.SyncError,
				},
			})
		} else if input.DDNSState.DesiredIPv6 == "" && !input.DDNSState.InGracePeriod {
			reasons = append(reasons, Reason{
				Code:            "ddns_no_valid_address",
				State:           RuleFiring,
				Severity:        SeverityWarning,
				Summary:         "DDNS 已启用但未检测到有效的公网 IPv6 地址",
				FirstObservedAt: now,
				LastObservedAt:  now,
				SuggestedAction: "检查设备 IPv6 网络连通性或前缀分配",
			})
		} else if input.DDNSState.DesiredIPv6 != "" && input.DDNSState.AppliedIPv6 != "" && input.DDNSState.DesiredIPv6 != input.DDNSState.AppliedIPv6 && !input.DDNSState.InGracePeriod {
			reasons = append(reasons, Reason{
				Code:            "ddns_address_drift",
				State:           RuleFiring,
				Severity:        SeverityWarning,
				Summary:         "DDNS 解析记录与当前设备 IPv6 地址不一致",
				FirstObservedAt: now,
				LastObservedAt:  now,
				SuggestedAction: "触发 DDNS 自动更新或检查网络地址变动",
				Evidence: map[string]any{
					"desired_ip": input.DDNSState.DesiredIPv6,
					"applied_ip": input.DDNSState.AppliedIPv6,
				},
			})
		} else if input.DDNSState.PrefixStale {
			reasons = append(reasons, Reason{
				Code:            "ddns_prefix_stale",
				State:           RuleFiring,
				Severity:        SeverityWarning,
				Summary:         "网络前缀已过期陈旧",
				FirstObservedAt: now,
				LastObservedAt:  now,
				SuggestedAction: "检查路由器 IPv6 前缀分发与 SLAAC 租约状态",
			})
		}
	}

	// 5. 升级任务规则
	if input.UpgradeCmd != nil {
		if input.UpgradeCmd.Status == "failed" || input.UpgradeCmd.Status == "timed_out" || input.UpgradeCmd.Status == "interrupted" {
			reasons = append(reasons, Reason{
				Code:            "upgrade_failed",
				State:           RuleFiring,
				Severity:        SeverityWarning,
				Summary:         fmt.Sprintf("自升级任务执行失败 (%s)", input.UpgradeCmd.Status),
				FirstObservedAt: now,
				LastObservedAt:  now,
				SuggestedAction: "查看自升级回执详情并排查安装环境后重试",
				Evidence: map[string]any{
					"command_id": input.UpgradeCmd.CommandID,
					"status":     input.UpgradeCmd.Status,
					"error_code": input.UpgradeCmd.ErrorCode,
				},
			})
		} else if input.UpgradeCmd.Status == "succeeded" {
			if input.Device != nil && input.VersionPolicy != nil && input.VersionPolicy.RecommendedVersion != "" && input.Device.AgentVersion != input.VersionPolicy.RecommendedVersion {
				finishedAge := now.Sub(input.UpgradeCmd.CreatedAt)
				if input.UpgradeCmd.FinishedAt != nil && !input.UpgradeCmd.FinishedAt.IsZero() {
					finishedAge = now.Sub(*input.UpgradeCmd.FinishedAt)
				}
				if finishedAge >= cfg.UpgradeConvergenceTimeout {
					reasons = append(reasons, Reason{
						Code:            "upgrade_not_converged",
						State:           RuleFiring,
						Severity:        SeverityWarning,
						Summary:         fmt.Sprintf("自升级已成功执行，但上报版本 (%s) 超过 %d 分钟未收敛至最新版本 (%s)", input.Device.AgentVersion, int(cfg.UpgradeConvergenceTimeout.Minutes()), input.VersionPolicy.RecommendedVersion),
						FirstObservedAt: now,
						LastObservedAt:  now,
						SuggestedAction: "查看自升级日志并检查 Agent 重启与运行状态",
						Evidence: map[string]any{
							"current_version": input.Device.AgentVersion,
							"target_version":  input.VersionPolicy.RecommendedVersion,
							"command_id":      input.UpgradeCmd.CommandID,
						},
					})
				}
			}
		}
	}

	// 6. 运行资源规则 (Disk / Memory) - 连续样本确认与滞回防抖
	if facts != nil {
		var prevDiskReason *Reason
		var prevMemReason *Reason
		if input.Previous != nil {
			for i := range input.Previous.Reasons {
				r := &input.Previous.Reasons[i]
				if r.Code == "disk_space_low" {
					prevDiskReason = r
				}
				if r.Code == "memory_pressure" {
					prevMemReason = r
				}
			}
		}

		// 磁盘检测：小于 8 GiB 的嵌入式文件系统只使用比例阈值；其余文件系统同时使用比例与绝对阈值。
		if facts.DiskTotalBytes > 0 {
			availRatio := float64(facts.DiskAvailableBytes) / float64(facts.DiskTotalBytes)
			const fiveGiB = 5 * 1024 * 1024 * 1024
			const eightGiB = 8 * 1024 * 1024 * 1024
			useAbsoluteThreshold := facts.DiskTotalBytes >= eightGiB

			prevDiskFiring := false
			prevDiskConsecutive := 0
			if prevDiskReason != nil {
				prevDiskFiring = (prevDiskReason.State == RuleFiring)
				if cnt, ok := prevDiskReason.Evidence["consecutive_count"].(int); ok {
					prevDiskConsecutive = cnt
				} else if cntF, ok := prevDiskReason.Evidence["consecutive_count"].(float64); ok {
					prevDiskConsecutive = int(cntF)
				} else if prevDiskFiring {
					prevDiskConsecutive = 2
				}
			}

			sampleExceeded := availRatio < 0.10 || (useAbsoluteThreshold && facts.DiskAvailableBytes < fiveGiB)
			var diskState RuleState
			var diskConsecutive int

			if prevDiskFiring {
				recovered := availRatio >= 0.15 && (!useAbsoluteThreshold || facts.DiskAvailableBytes >= eightGiB)
				if recovered {
					diskState = RuleOK
					diskConsecutive = 0
				} else {
					diskState = RuleFiring
					diskConsecutive = prevDiskConsecutive + 1
				}
			} else {
				if sampleExceeded {
					diskConsecutive = prevDiskConsecutive + 1
					if diskConsecutive >= 2 {
						diskState = RuleFiring
					} else {
						diskState = RuleOK
					}
				} else {
					diskConsecutive = 0
					diskState = RuleOK
				}
			}

			if diskState == RuleFiring || diskConsecutive > 0 {
				reasons = append(reasons, Reason{
					Code:            "disk_space_low",
					State:           diskState,
					Severity:        SeverityWarning,
					Summary:         fmt.Sprintf("磁盘空间不足 (可用: %.1f%% / %d MiB)", availRatio*100, facts.DiskAvailableBytes/(1024*1024)),
					FirstObservedAt: now,
					LastObservedAt:  now,
					SuggestedAction: "清理系统日志与临时文件释放磁盘空间",
					Evidence: map[string]any{
						"mount":             facts.DiskMount,
						"available_bytes":   facts.DiskAvailableBytes,
						"total_bytes":       facts.DiskTotalBytes,
						"avail_ratio":       availRatio,
						"consecutive_count": diskConsecutive,
					},
				})
			}
		}

		// 内存检测 (连续 3 次样本超标触发; 迟滞: 触发 <10%; 恢复 >=20%)
		if facts.MemoryTotalBytes > 0 {
			availRatio := float64(facts.MemoryAvailableBytes) / float64(facts.MemoryTotalBytes)

			prevMemFiring := false
			prevMemConsecutive := 0
			if prevMemReason != nil {
				prevMemFiring = (prevMemReason.State == RuleFiring)
				if cnt, ok := prevMemReason.Evidence["consecutive_count"].(int); ok {
					prevMemConsecutive = cnt
				} else if cntF, ok := prevMemReason.Evidence["consecutive_count"].(float64); ok {
					prevMemConsecutive = int(cntF)
				} else if prevMemFiring {
					prevMemConsecutive = 3
				}
			}

			sampleExceeded := (availRatio < 0.10)
			var memState RuleState
			var memConsecutive int

			if prevMemFiring {
				recovered := (availRatio >= 0.20)
				if recovered {
					memState = RuleOK
					memConsecutive = 0
				} else {
					memState = RuleFiring
					memConsecutive = prevMemConsecutive + 1
				}
			} else {
				if sampleExceeded {
					memConsecutive = prevMemConsecutive + 1
					if memConsecutive >= 3 {
						memState = RuleFiring
					} else {
						memState = RuleOK
					}
				} else {
					memConsecutive = 0
					memState = RuleOK
				}
			}

			if memState == RuleFiring || memConsecutive > 0 {
				reasons = append(reasons, Reason{
					Code:            "memory_pressure",
					State:           memState,
					Severity:        SeverityWarning,
					Summary:         fmt.Sprintf("可用内存严重不足 (可用: %.1f%% / %d MiB)", availRatio*100, facts.MemoryAvailableBytes/(1024*1024)),
					FirstObservedAt: now,
					LastObservedAt:  now,
					SuggestedAction: "检查高内存占用进程并及时释放",
					Evidence: map[string]any{
						"available_bytes":   facts.MemoryAvailableBytes,
						"total_bytes":       facts.MemoryTotalBytes,
						"avail_ratio":       availRatio,
						"consecutive_count": memConsecutive,
					},
				})
			}
		}
	}

	// 保持历史 FirstObservedAt 并排序
	if input.Previous != nil {
		prevReasonMap := make(map[string]Reason, len(input.Previous.Reasons))
		for _, pr := range input.Previous.Reasons {
			prevReasonMap[pr.Code] = pr
		}
		for i := range reasons {
			if pr, ok := prevReasonMap[reasons[i].Code]; ok && pr.State == RuleFiring && reasons[i].State == RuleFiring {
				reasons[i].FirstObservedAt = pr.FirstObservedAt
			}
		}
	}

	sort.SliceStable(reasons, func(i, j int) bool {
		si := severityWeight(reasons[i].Severity)
		sj := severityWeight(reasons[j].Severity)
		if si != sj {
			return si > sj
		}
		return reasons[i].Code < reasons[j].Code
	})

	// 计算综合状态
	var overall Status
	hasOffline := false
	hasNeverSeen := false
	hasWarningOrCritical := false

	for _, r := range reasons {
		if r.Code == "device_offline" && r.State == RuleFiring {
			hasOffline = true
		}
		if r.Code == "device_never_seen" {
			hasNeverSeen = true
		}
		if r.State == RuleFiring && (r.Severity == SeverityCritical || r.Severity == SeverityWarning) {
			hasWarningOrCritical = true
		}
	}

	if hasOffline {
		overall = StatusOffline
	} else if hasNeverSeen {
		overall = StatusUnknown
	} else if hasWarningOrCritical {
		overall = StatusDegraded
	} else {
		overall = StatusHealthy
	}

	since := now
	revision := uint64(1)
	if input.Previous != nil {
		revision = input.Previous.Revision + 1
		if input.Previous.Status == overall {
			since = input.Previous.Since
		}
	}

	snapshot := HealthSnapshot{
		DeviceID:    deviceID,
		Status:      overall,
		Connected:   connected,
		EvaluatedAt: now,
		LastSeenAt:  lastSeen,
		Since:       since,
		RuleVersion: cfg.RuleVersion,
		Reasons:     reasons,
		Facts:       facts,
		Revision:    revision,
	}

	// 生成事件
	events := generateEvents(input.Previous, snapshot, now)

	return snapshot, events
}

func severityWeight(s Severity) int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityWarning:
		return 2
	case SeverityInfo:
		return 1
	default:
		return 0
	}
}

func generateEvents(prev *HealthSnapshot, current HealthSnapshot, now time.Time) []HealthEvent {
	var events []HealthEvent

	currentMap := make(map[string]Reason, len(current.Reasons))
	for _, r := range current.Reasons {
		currentMap[r.Code] = r
	}

	prevMap := make(map[string]Reason)
	if prev != nil {
		for _, r := range prev.Reasons {
			prevMap[r.Code] = r
		}
	}

	// 1. 新打开或变更的规则
	for code, cur := range currentMap {
		if cur.State != RuleFiring {
			continue
		}
		old, existed := prevMap[code]
		if !existed || old.State != RuleFiring {
			events = append(events, HealthEvent{
				ID:               fmt.Sprintf("hevt_%s_%s_%d", current.DeviceID, code, now.UnixNano()),
				DeviceID:         current.DeviceID,
				Type:             "opened",
				ReasonCode:       code,
				FromState:        RuleOK,
				ToState:          cur.State,
				Severity:         cur.Severity,
				OccurredAt:       now,
				RuleVersion:      current.RuleVersion,
				Evidence:         cur.Evidence,
				SnapshotRevision: current.Revision,
			})
		} else if old.Severity != cur.Severity {
			events = append(events, HealthEvent{
				ID:               fmt.Sprintf("hevt_%s_%s_%d", current.DeviceID, code, now.UnixNano()),
				DeviceID:         current.DeviceID,
				Type:             "changed",
				ReasonCode:       code,
				FromState:        old.State,
				ToState:          cur.State,
				Severity:         cur.Severity,
				OccurredAt:       now,
				RuleVersion:      current.RuleVersion,
				Evidence:         cur.Evidence,
				SnapshotRevision: current.Revision,
			})
		}
	}

	// 2. 已恢复的规则
	for code, old := range prevMap {
		if old.State != RuleFiring {
			continue
		}
		cur, exists := currentMap[code]
		if !exists || cur.State != RuleFiring {
			events = append(events, HealthEvent{
				ID:               fmt.Sprintf("hevt_%s_%s_%d", current.DeviceID, code, now.UnixNano()),
				DeviceID:         current.DeviceID,
				Type:             "resolved",
				ReasonCode:       code,
				FromState:        old.State,
				ToState:          RuleOK,
				Severity:         old.Severity,
				OccurredAt:       now,
				RuleVersion:      current.RuleVersion,
				Evidence:         old.Evidence,
				SnapshotRevision: current.Revision,
			})
		}
	}

	return events
}

// semverCompare 简易 SemVer 比较工具函数 (返回 -1, 0, 1)
func semverCompare(a, b string) int {
	clean := func(s string) string {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "v")
		if idx := strings.Index(s, "-"); idx != -1 {
			s = s[:idx]
		}
		return s
	}
	ca := clean(a)
	cb := clean(b)
	if ca == cb {
		return 0
	}
	pa := strings.Split(ca, ".")
	pb := strings.Split(cb, ".")
	maxLen := len(pa)
	if len(pb) > maxLen {
		maxLen = len(pb)
	}
	for i := 0; i < maxLen; i++ {
		var na, nb int
		if i < len(pa) {
			na, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			nb, _ = strconv.Atoi(pb[i])
		}
		if na < nb {
			return -1
		}
		if na > nb {
			return 1
		}
	}
	return 0
}

func isValidSemVer(s string) bool {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return false
	}
	if idx := strings.Index(s, "-"); idx != -1 {
		s = s[:idx]
	}
	parts := strings.Split(s, ".")
	if len(parts) == 0 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}
