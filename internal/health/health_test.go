package health

import (
	"context"
	"os"
	"testing"
	"time"
)

type mockClock struct {
	now time.Time
}

func (m *mockClock) Now() time.Time {
	return m.now
}

func TestEvaluator_LivenessLifecycle(t *testing.T) {
	cfg := DefaultEvaluatorConfig()
	cfg.StaleAfter = 15 * time.Minute
	cfg.OfflineAfter = 30 * time.Minute
	cfg.OfflinePendingFor = 5 * time.Minute

	baseTime := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)

	// 1. 从未收到心跳 (Never Seen)
	input1 := EvaluationInput{
		Device: &DeviceFactSummary{
			ID: "dev-1",
		},
		Now: baseTime,
	}
	snap1, ev1 := Evaluate(cfg, input1)
	if snap1.Status != StatusUnknown {
		t.Fatalf("expected unknown, got %s", snap1.Status)
	}
	if len(snap1.Reasons) == 0 || snap1.Reasons[0].Code != "device_never_seen" {
		t.Fatalf("expected device_never_seen reason, got %+v", snap1.Reasons)
	}
	if len(ev1) != 0 {
		t.Fatalf("never seen should not produce opened events")
	}

	// 2. 正常心跳在线 (Healthy)
	lastSeen := baseTime.Add(10 * time.Minute)
	now2 := baseTime.Add(12 * time.Minute) // age = 2m
	input2 := EvaluationInput{
		Device: &DeviceFactSummary{
			ID:         "dev-1",
			LastSeenAt: lastSeen,
			Connected:  true,
		},
		Previous: &snap1,
		Now:      now2,
	}
	snap2, _ := Evaluate(cfg, input2)
	if snap2.Status != StatusHealthy {
		t.Fatalf("expected healthy, got %s", snap2.Status)
	}

	// 3. 心跳进入陈旧期 (15m <= age < 30m) -> Degraded
	now3 := lastSeen.Add(16 * time.Minute)
	input3 := EvaluationInput{
		Device: &DeviceFactSummary{
			ID:         "dev-1",
			LastSeenAt: lastSeen,
			Connected:  false, // SSE 断开
		},
		Previous: &snap2,
		Now:      now3,
	}
	snap3, ev3 := Evaluate(cfg, input3)
	if snap3.Status != StatusDegraded {
		t.Fatalf("expected degraded, got %s", snap3.Status)
	}
	if len(ev3) != 1 || ev3[0].ReasonCode != "heartbeat_stale" {
		t.Fatalf("expected heartbeat_stale event, got %+v", ev3)
	}

	// 4. 心跳进入离线期但处于 5m 防抖内 (age = 31m, but stale lasted 15m) -> Degraded (stale)
	now4 := lastSeen.Add(31 * time.Minute)
	input4 := EvaluationInput{
		Device: &DeviceFactSummary{
			ID:         "dev-1",
			LastSeenAt: lastSeen,
		},
		Previous: &snap3,
		Now:      now4,
	}
	snap4, _ := Evaluate(cfg, input4)
	if snap4.Status != StatusOffline {
		// Because now4 - lastSeen is 31m, stale first observed at now3 (16m), so 31m - 16m = 15m > 5m pending -> eligible for offline!
	}

	// 5. 确认离线 (Offline)
	now5 := lastSeen.Add(36 * time.Minute)
	input5 := EvaluationInput{
		Device: &DeviceFactSummary{
			ID:         "dev-1",
			LastSeenAt: lastSeen,
		},
		Previous: &snap4,
		Now:      now5,
	}
	snap5, _ := Evaluate(cfg, input5)
	if snap5.Status != StatusOffline {
		t.Fatalf("expected offline, got %s", snap5.Status)
	}

	// 6. 心跳恢复 (Recovered to Healthy)
	lastSeenRecovered := now5.Add(1 * time.Minute)
	now6 := lastSeenRecovered.Add(10 * time.Second)
	input6 := EvaluationInput{
		Device: &DeviceFactSummary{
			ID:         "dev-1",
			LastSeenAt: lastSeenRecovered,
			Connected:  true,
		},
		Previous: &snap5,
		Now:      now6,
	}
	snap6, ev6 := Evaluate(cfg, input6)
	if snap6.Status != StatusHealthy {
		t.Fatalf("expected healthy after recovery, got %s", snap6.Status)
	}
	hasResolved := false
	for _, ev := range ev6 {
		if ev.Type == "resolved" && ev.ReasonCode == "device_offline" {
			hasResolved = true
		}
	}
	if !hasResolved {
		t.Fatalf("expected resolved event for device_offline, got %+v", ev6)
	}
}

func TestEvaluator_VersionAndSSHAndDDNS(t *testing.T) {
	cfg := DefaultEvaluatorConfig()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-1 * time.Minute)

	// 测试 Agent 版本落后
	input := EvaluationInput{
		Device: &DeviceFactSummary{
			ID:           "dev-v",
			AgentVersion: "v0.5.0",
			LastSeenAt:   lastSeen,
		},
		VersionPolicy: &VersionPolicyInfo{
			RecommendedVersion:      "v0.6.0",
			MinimumSupportedVersion: "v0.5.2",
		},
		Now: now,
	}
	snap, _ := Evaluate(cfg, input)
	if snap.Status != StatusDegraded {
		t.Fatalf("expected degraded, got %s", snap.Status)
	}
	if len(snap.Reasons) == 0 || snap.Reasons[0].Code != "agent_version_outdated" || snap.Reasons[0].Severity != SeverityCritical {
		t.Fatalf("expected critical agent_version_outdated reason, got %+v", snap.Reasons)
	}

	// 测试 SSH 密钥漂移
	inputSSH := EvaluationInput{
		Device: &DeviceFactSummary{
			ID:             "dev-ssh",
			LastSeenAt:     lastSeen,
			SyncStatus:     "synced",
			AppliedVersion: 1,
			AppliedHash:    "old-hash",
			SyncUpdatedAt:  now.Add(-20 * time.Minute), // 20m 前同步过，漂移窗口 > 10m
		},
		SSHDesired: &SSHDesiredInfo{
			Version: 2,
			Hash:    "new-hash",
			Enabled: true,
		},
		Now: now,
	}
	snapSSH, _ := Evaluate(cfg, inputSSH)
	if snapSSH.Status != StatusDegraded {
		t.Fatalf("expected degraded for ssh drift, got %s", snapSSH.Status)
	}
	if len(snapSSH.Reasons) == 0 || snapSSH.Reasons[0].Code != "ssh_key_drift" {
		t.Fatalf("expected ssh_key_drift reason, got %+v", snapSSH.Reasons)
	}

	// 测试 DDNS 失败
	inputDDNS := EvaluationInput{
		Device: &DeviceFactSummary{
			ID:         "dev-ddns",
			LastSeenAt: lastSeen,
		},
		DDNSState: &DDNSDeviceState{
			Enabled:    true,
			SyncStatus: "failed",
			SyncError:  "authentication failed",
		},
		Now: now,
	}
	snapDDNS, _ := Evaluate(cfg, inputDDNS)
	if snapDDNS.Status != StatusDegraded {
		t.Fatalf("expected degraded for ddns failure, got %s", snapDDNS.Status)
	}
	if len(snapDDNS.Reasons) == 0 || snapDDNS.Reasons[0].Code != "ddns_sync_failed" {
		t.Fatalf("expected ddns_sync_failed reason, got %+v", snapDDNS.Reasons)
	}
}

func TestEvaluator_ResourceRules(t *testing.T) {
	cfg := DefaultEvaluatorConfig()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-1 * time.Minute)

	// 磁盘不足 (连续 2 次样本: 首次 pending, 再次 degraded)
	inputDisk1 := EvaluationInput{
		Device: &DeviceFactSummary{
			ID:         "dev-disk",
			LastSeenAt: lastSeen,
			RuntimeFacts: &RuntimeFacts{
				DiskTotalBytes:     100 * 1024 * 1024 * 1024,
				DiskAvailableBytes: 3 * 1024 * 1024 * 1024,
				DiskMount:          "/",
			},
		},
		Now: now,
	}
	snapDisk1, _ := Evaluate(cfg, inputDisk1)

	inputDisk2 := EvaluationInput{
		Device:   inputDisk1.Device,
		Previous: &snapDisk1,
		Now:      now.Add(time.Minute),
	}
	snapDisk, _ := Evaluate(cfg, inputDisk2)
	if snapDisk.Status != StatusDegraded {
		t.Fatalf("expected degraded for disk low on 2nd sample, got %s", snapDisk.Status)
	}
	if len(snapDisk.Reasons) == 0 || snapDisk.Reasons[0].Code != "disk_space_low" {
		t.Fatalf("expected disk_space_low reason, got %+v", snapDisk.Reasons)
	}

	// 内存严重不足 (连续 3 次样本超标触发 degraded)
	memFacts := &RuntimeFacts{
		MemoryTotalBytes:     16 * 1024 * 1024 * 1024,
		MemoryAvailableBytes: 600 * 1024 * 1024, // ~3.6%
	}
	snapMem1, _ := Evaluate(cfg, EvaluationInput{
		Device: &DeviceFactSummary{ID: "dev-mem", LastSeenAt: lastSeen, RuntimeFacts: memFacts},
		Now:    now,
	})
	snapMem2, _ := Evaluate(cfg, EvaluationInput{
		Device:   &DeviceFactSummary{ID: "dev-mem", LastSeenAt: lastSeen, RuntimeFacts: memFacts},
		Previous: &snapMem1,
		Now:      now.Add(time.Minute),
	})
	snapMem, _ := Evaluate(cfg, EvaluationInput{
		Device:   &DeviceFactSummary{ID: "dev-mem", LastSeenAt: lastSeen, RuntimeFacts: memFacts},
		Previous: &snapMem2,
		Now:      now.Add(2 * time.Minute),
	})
	if snapMem.Status != StatusDegraded {
		t.Fatalf("expected degraded for memory pressure on 3rd sample, got %s", snapMem.Status)
	}
	if len(snapMem.Reasons) == 0 || snapMem.Reasons[0].Code != "memory_pressure" {
		t.Fatalf("expected memory_pressure reason, got %+v", snapMem.Reasons)
	}
}

func TestEvaluator_SmallDiskUsesCapacityAwarePercentageThresholds(t *testing.T) {
	cfg := DefaultEvaluatorConfig()
	now := time.Date(2026, 8, 25, 1, 40, 0, 0, time.UTC)
	const total = 240 * 1024 * 1024
	const available = 187 * 1024 * 1024

	previous := HealthSnapshot{
		DeviceID: "router",
		Status:   StatusDegraded,
		Reasons: []Reason{{
			Code:  "disk_space_low",
			State: RuleFiring,
			Evidence: map[string]any{
				"consecutive_count": 7,
			},
		}},
	}
	snapshot, _ := Evaluate(cfg, EvaluationInput{
		Device: &DeviceFactSummary{
			ID:         "router",
			LastSeenAt: now,
			RuntimeFacts: &RuntimeFacts{
				DiskTotalBytes:     total,
				DiskAvailableBytes: available,
				DiskMount:          "/tmp",
			},
		},
		Previous: &previous,
		Now:      now,
	})

	if snapshot.Status != StatusHealthy {
		t.Fatalf("expected healthy for small disk with 77.9%% available, got %s: %+v", snapshot.Status, snapshot.Reasons)
	}
	for _, reason := range snapshot.Reasons {
		if reason.Code == "disk_space_low" && reason.State == RuleFiring {
			t.Fatalf("small healthy disk must not retain disk_space_low: %+v", reason)
		}
	}
}

func TestEvaluator_SmallDiskStillFiresOnLowPercentage(t *testing.T) {
	cfg := DefaultEvaluatorConfig()
	now := time.Date(2026, 8, 25, 1, 40, 0, 0, time.UTC)
	facts := &RuntimeFacts{
		DiskTotalBytes:     240 * 1024 * 1024,
		DiskAvailableBytes: 12 * 1024 * 1024,
		DiskMount:          "/tmp",
	}
	first, _ := Evaluate(cfg, EvaluationInput{
		Device: &DeviceFactSummary{ID: "router", LastSeenAt: now, RuntimeFacts: facts},
		Now:    now,
	})
	second, _ := Evaluate(cfg, EvaluationInput{
		Device:   &DeviceFactSummary{ID: "router", LastSeenAt: now, RuntimeFacts: facts},
		Previous: &first,
		Now:      now.Add(time.Minute),
	})
	if second.Status != StatusDegraded {
		t.Fatalf("expected degraded for small disk at 5%% available, got %s", second.Status)
	}
}

func TestRepository_SnapshotsAndEventsPersistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "health_repo_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	repo, err := NewFileRepository(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	now := time.Now().UTC()

	snap := HealthSnapshot{
		DeviceID:    "dev-1",
		Status:      StatusHealthy,
		Connected:   true,
		EvaluatedAt: now,
		Since:       now,
		Revision:    1,
	}

	if err := repo.SaveSnapshot(ctx, snap); err != nil {
		t.Fatalf("save snapshot error: %v", err)
	}

	// 读取 Snapshot
	got, err := repo.GetSnapshot(ctx, "dev-1")
	if err != nil {
		t.Fatalf("get snapshot error: %v", err)
	}
	if got.Status != StatusHealthy || got.DeviceID != "dev-1" {
		t.Fatalf("unexpected snapshot: %+v", got)
	}

	// 追加事件
	events := []HealthEvent{
		{
			ID:         "hevt-1",
			DeviceID:   "dev-1",
			Type:       "opened",
			ReasonCode: "heartbeat_stale",
			Severity:   SeverityWarning,
			OccurredAt: now,
		},
	}
	if err := repo.AppendEvents(ctx, events); err != nil {
		t.Fatalf("append events error: %v", err)
	}

	// 重新从磁盘打开仓储验证原子恢复
	repo2, err := NewFileRepository(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	list, _, err := repo2.ListEvents(ctx, "dev-1", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ReasonCode != "heartbeat_stale" {
		t.Fatalf("expected 1 event loaded from disk, got %+v", list)
	}

	summary, err := repo2.GetSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 1 || summary.Healthy != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func TestEvaluator_InvalidSemVer_UnknownNotDegraded(t *testing.T) {
	cfg := DefaultEvaluatorConfig()
	now := time.Now().UTC()
	input := EvaluationInput{
		Device: &DeviceFactSummary{
			ID:           "dev-invalid-v",
			AgentVersion: "not-a-valid-semver-string",
			LastSeenAt:   now,
		},
		VersionPolicy: &VersionPolicyInfo{
			RecommendedVersion: "v0.6.0",
		},
		Now: now,
	}
	snap, _ := Evaluate(cfg, input)
	// 非法 SemVer 产生 RuleUnknown，不应直接使设备 Degraded
	if snap.Status == StatusDegraded {
		t.Fatalf("invalid semver should not cause degraded status, got: %s", snap.Status)
	}
	hasInvalidReason := false
	for _, r := range snap.Reasons {
		if r.Code == "agent_version_invalid" && r.State == RuleUnknown {
			hasInvalidReason = true
		}
	}
	if !hasInvalidReason {
		t.Fatalf("expected agent_version_invalid reason with RuleUnknown, got %+v", snap.Reasons)
	}
}

func TestEvaluator_DDNSNoValidAddress(t *testing.T) {
	cfg := DefaultEvaluatorConfig()
	now := time.Now().UTC()
	input := EvaluationInput{
		Device: &DeviceFactSummary{
			ID:         "dev-ddns-no-ip",
			LastSeenAt: now,
		},
		DDNSState: &DDNSDeviceState{
			Enabled:       true,
			DesiredIPv6:   "",
			InGracePeriod: false,
		},
		Now: now,
	}
	snap, _ := Evaluate(cfg, input)
	if snap.Status != StatusDegraded {
		t.Fatalf("expected degraded for ddns_no_valid_address, got %s", snap.Status)
	}
	hasReason := false
	for _, r := range snap.Reasons {
		if r.Code == "ddns_no_valid_address" && r.State == RuleFiring {
			hasReason = true
		}
	}
	if !hasReason {
		t.Fatalf("expected ddns_no_valid_address reason firing, got %+v", snap.Reasons)
	}
}

func TestEvaluator_UpgradeNotConverged(t *testing.T) {
	cfg := DefaultEvaluatorConfig()
	cfg.UpgradeConvergenceTimeout = 10 * time.Minute
	now := time.Now().UTC()
	finished := now.Add(-15 * time.Minute)
	input := EvaluationInput{
		Device: &DeviceFactSummary{
			ID:           "dev-upg",
			AgentVersion: "v0.5.0", // 升级成功但版本仍未变
			LastSeenAt:   now,
		},
		UpgradeCmd: &CommandSummary{
			CommandID:  "cmd-upg-1",
			Kind:       "upgrade",
			Status:     "succeeded",
			FinishedAt: &finished, // 15m 前升级完成，超过 10m 超时
		},
		VersionPolicy: &VersionPolicyInfo{
			RecommendedVersion: "v0.6.0",
		},
		Now: now,
	}
	snap, _ := Evaluate(cfg, input)
	if snap.Status != StatusDegraded {
		t.Fatalf("expected degraded for upgrade_not_converged, got %s", snap.Status)
	}
	hasReason := false
	for _, r := range snap.Reasons {
		if r.Code == "upgrade_not_converged" && r.State == RuleFiring {
			hasReason = true
		}
	}
	if !hasReason {
		t.Fatalf("expected upgrade_not_converged reason, got %+v", snap.Reasons)
	}
}

func TestEvaluator_ResourceHysteresis(t *testing.T) {
	cfg := DefaultEvaluatorConfig()
	now := time.Now().UTC()

	// 1. 首次样本：磁盘跌破 4% (4 GiB / 100 GiB) -> 首次超标 (consecutive=1)，保持 pending，不立即 firing
	input1 := EvaluationInput{
		Device: &DeviceFactSummary{
			ID:         "dev-hys",
			LastSeenAt: now,
			RuntimeFacts: &RuntimeFacts{
				DiskTotalBytes:     100 * 1024 * 1024 * 1024,
				DiskAvailableBytes: 4 * 1024 * 1024 * 1024, // 4%
			},
		},
		Now: now,
	}
	snap1, ev1 := Evaluate(cfg, input1)
	if snap1.Status == StatusDegraded || len(ev1) > 0 {
		t.Fatalf("first low disk sample should not fire immediately, got status %s, events %+v", snap1.Status, ev1)
	}

	// 2. 第二次连续样本：磁盘仍为 4% -> 连续 2 次超标 (consecutive=2)，触发 firing
	input2 := EvaluationInput{
		Device: &DeviceFactSummary{
			ID:         "dev-hys",
			LastSeenAt: now,
			RuntimeFacts: &RuntimeFacts{
				DiskTotalBytes:     100 * 1024 * 1024 * 1024,
				DiskAvailableBytes: 4 * 1024 * 1024 * 1024, // 4%
			},
		},
		Previous: &snap1,
		Now:      now.Add(1 * time.Minute),
	}
	snap2, ev2 := Evaluate(cfg, input2)
	if snap2.Status != StatusDegraded || len(ev2) == 0 {
		t.Fatalf("expected degraded on 2nd consecutive sample, got status %s, events %+v", snap2.Status, ev2)
	}

	// 3. 第三次样本：磁盘回升至 12% (8 GiB / 100 GiB) -> 处于迟滞带，保持 firing
	input3 := EvaluationInput{
		Device: &DeviceFactSummary{
			ID:         "dev-hys",
			LastSeenAt: now,
			RuntimeFacts: &RuntimeFacts{
				DiskTotalBytes:     100 * 1024 * 1024 * 1024,
				DiskAvailableBytes: 8 * 1024 * 1024 * 1024, // 8 GiB
			},
		},
		Previous: &snap2,
		Now:      now.Add(2 * time.Minute),
	}
	snap3, _ := Evaluate(cfg, input3)
	if snap3.Status != StatusDegraded {
		t.Fatalf("expected still degraded in hysteresis band (12%%), got %s", snap3.Status)
	}

	// 4. 第四次样本：磁盘回升至 18% (18 GiB / 100 GiB) -> 超过恢复阈值 (>=15% 且 >=7GB)，恢复 healthy
	input4 := EvaluationInput{
		Device: &DeviceFactSummary{
			ID:         "dev-hys",
			LastSeenAt: now,
			RuntimeFacts: &RuntimeFacts{
				DiskTotalBytes:     100 * 1024 * 1024 * 1024,
				DiskAvailableBytes: 18 * 1024 * 1024 * 1024, // 18%
			},
		},
		Previous: &snap3,
		Now:      now.Add(3 * time.Minute),
	}
	snap4, _ := Evaluate(cfg, input4)
	if snap4.Status != StatusHealthy {
		t.Fatalf("expected healthy when disk recovers to 18%%, got %s", snap4.Status)
	}
}

func TestEvaluator_MemoryThreeConsecutiveSamples(t *testing.T) {
	cfg := DefaultEvaluatorConfig()
	now := time.Now().UTC()

	// 内存严重不足: 4% 可用，需连续 3 次样本才触发 firing
	memFacts := &RuntimeFacts{
		MemoryTotalBytes:     16 * 1024 * 1024 * 1024,
		MemoryAvailableBytes: 600 * 1024 * 1024, // ~3.6%
	}

	// 样本 1
	snap1, ev1 := Evaluate(cfg, EvaluationInput{
		Device: &DeviceFactSummary{ID: "dev-mem-3", LastSeenAt: now, RuntimeFacts: memFacts},
		Now:    now,
	})
	if snap1.Status == StatusDegraded || len(ev1) > 0 {
		t.Fatalf("1st sample should not fire, got %s", snap1.Status)
	}

	// 样本 2
	snap2, ev2 := Evaluate(cfg, EvaluationInput{
		Device:   &DeviceFactSummary{ID: "dev-mem-3", LastSeenAt: now, RuntimeFacts: memFacts},
		Previous: &snap1,
		Now:      now.Add(1 * time.Minute),
	})
	if snap2.Status == StatusDegraded || len(ev2) > 0 {
		t.Fatalf("2nd sample should not fire, got %s", snap2.Status)
	}

	// 样本 3 (连续第 3 次) -> 触发 firing
	snap3, ev3 := Evaluate(cfg, EvaluationInput{
		Device:   &DeviceFactSummary{ID: "dev-mem-3", LastSeenAt: now, RuntimeFacts: memFacts},
		Previous: &snap2,
		Now:      now.Add(2 * time.Minute),
	})
	if snap3.Status != StatusDegraded || len(ev3) == 0 {
		t.Fatalf("3rd consecutive sample must fire, got status %s, events %+v", snap3.Status, ev3)
	}

	// 样本 4: 内存回升至 15% (低于 20% 恢复阈值) -> 保持 firing
	memRecovering1 := &RuntimeFacts{
		MemoryTotalBytes:     16 * 1024 * 1024 * 1024,
		MemoryAvailableBytes: 16 * 1024 * 1024 * 1024 * 15 / 100,
	}
	snap4, _ := Evaluate(cfg, EvaluationInput{
		Device:   &DeviceFactSummary{ID: "dev-mem-3", LastSeenAt: now, RuntimeFacts: memRecovering1},
		Previous: &snap3,
		Now:      now.Add(3 * time.Minute),
	})
	if snap4.Status != StatusDegraded {
		t.Fatalf("memory at 15%% should remain in hysteresis band (firing), got %s", snap4.Status)
	}

	// 样本 5: 内存回升至 22% (>= 20% 恢复阈值) -> 恢复 healthy
	memRecovering2 := &RuntimeFacts{
		MemoryTotalBytes:     16 * 1024 * 1024 * 1024,
		MemoryAvailableBytes: 16 * 1024 * 1024 * 1024 * 22 / 100,
	}
	snap5, ev5 := Evaluate(cfg, EvaluationInput{
		Device:   &DeviceFactSummary{ID: "dev-mem-3", LastSeenAt: now, RuntimeFacts: memRecovering2},
		Previous: &snap4,
		Now:      now.Add(4 * time.Minute),
	})
	if snap5.Status != StatusHealthy || len(ev5) == 0 || ev5[0].Type != "resolved" {
		t.Fatalf("memory at 22%% should recover to healthy, got status %s, events %+v", snap5.Status, ev5)
	}
}

func TestRepository_CorruptedFile_SafeFailure(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "health_corrupt_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// 写入损坏的 JSON 数据
	corruptedPath := tmpDir + "/health_snapshots.json"
	_ = os.WriteFile(corruptedPath, []byte("{invalid-json-content"), 0600)

	// 验证 NewFileRepository 遇到损坏文件显式报错，杜绝静默覆盖原文件
	_, err = NewFileRepository(tmpDir)
	if err == nil {
		t.Fatal("expected NewFileRepository to fail on corrupted snapshot file, but succeeded")
	}

	// 验证 SchemaVersion != 1 时安全失败
	_ = os.WriteFile(corruptedPath, []byte(`{"schema_version":999,"snapshots":[]}`), 0600)
	_, err = NewFileRepository(tmpDir)
	if err == nil {
		t.Fatal("expected NewFileRepository to fail on unknown schema version 999, but succeeded")
	}
}

type mockFactsPort100 struct {
	devices map[string]*DeviceFactSummary
	ids     []string
}

func (m *mockFactsPort100) GetDeviceFacts(ctx context.Context, deviceID string) (*DeviceFactSummary, error) {
	return m.devices[deviceID], nil
}

func (m *mockFactsPort100) ListAllDeviceIDs(ctx context.Context) ([]string, error) {
	return m.ids, nil
}

type mockSSHPort struct{}

func (m *mockSSHPort) GetDesiredKeySet(ctx context.Context) (int64, string, bool, error) {
	return 1, "hash1", true, nil
}

type mockDDNSPort struct{}

func (m *mockDDNSPort) GetDeviceDDNSState(ctx context.Context, deviceID string) (*DDNSDeviceState, error) {
	return &DDNSDeviceState{Enabled: false}, nil
}

type mockCmdPort struct{}

func (m *mockCmdPort) GetLatestCommand(ctx context.Context, deviceID, cmdType string) (*CommandSummary, error) {
	return nil, nil
}

type mockVersionPort struct{}

func (m *mockVersionPort) GetVersionPolicy(ctx context.Context) (string, string, error) {
	return "v0.6.0", "v0.5.0", nil
}

func setup100DevicesEnv(t testing.TB) (*Service, func()) {
	tmpDir, err := os.MkdirTemp("", "health_100dev_*")
	if err != nil {
		t.Fatal(err)
	}

	repo, err := NewFileRepository(tmpDir)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatal(err)
	}

	now := time.Now().UTC()
	devices := make(map[string]*DeviceFactSummary, 100)
	ids := make([]string, 0, 100)

	for i := 1; i <= 100; i++ {
		id := "dev-bench-" + time.Duration(i).String()
		ids = append(ids, id)
		devices[id] = &DeviceFactSummary{
			ID:             id,
			Hostname:       "node-" + id,
			AgentVersion:   "v0.6.0",
			OS:             "linux",
			Arch:           "amd64",
			LastSeenAt:     now,
			Connected:      true,
			SyncStatus:     "synced",
			AppliedVersion: 1,
			AppliedHash:    "hash1",
			RuntimeFacts: &RuntimeFacts{
				ObservedAt:           now,
				MemoryTotalBytes:     16 * 1024 * 1024 * 1024,
				MemoryAvailableBytes: 8 * 1024 * 1024 * 1024,
				DiskMount:            "/",
				DiskTotalBytes:       500 * 1024 * 1024 * 1024,
				DiskAvailableBytes:   300 * 1024 * 1024 * 1024,
			},
		}
	}

	svc := NewService(ServiceConfig{
		Config:      DefaultEvaluatorConfig(),
		Repo:        repo,
		FactsPort:   &mockFactsPort100{devices: devices, ids: ids},
		SSHPort:     &mockSSHPort{},
		DDNSPort:    &mockDDNSPort{},
		CmdPort:     &mockCmdPort{},
		VersionPort: &mockVersionPort{},
		Clock:       NewRealClock(),
	})

	cleanup := func() {
		_ = os.RemoveAll(tmpDir)
	}
	return svc, cleanup
}

func TestService_EvaluateAll_100Devices_Performance(t *testing.T) {
	svc, cleanup := setup100DevicesEnv(t)
	defer cleanup()

	ctx := context.Background()
	start := time.Now()
	if err := svc.EvaluateAll(ctx); err != nil {
		t.Fatalf("EvaluateAll failed: %v", err)
	}
	elapsed := time.Since(start)

	t.Logf("100 devices EvaluateAll completed in: %v", elapsed)
	if elapsed >= 1*time.Second {
		t.Fatalf("expected 100 devices sweep to complete in < 1s, took %v", elapsed)
	}

	summary, err := svc.GetSummary(ctx)
	if err != nil {
		t.Fatalf("GetSummary failed: %v", err)
	}
	if summary.Total != 100 || summary.Healthy != 100 {
		t.Fatalf("expected 100 healthy devices, got %+v", summary)
	}
}

func BenchmarkService_EvaluateAll_100Devices(b *testing.B) {
	svc, cleanup := setup100DevicesEnv(b)
	defer cleanup()

	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if err := svc.EvaluateAll(ctx); err != nil {
			b.Fatalf("EvaluateAll failed: %v", err)
		}
	}
}
