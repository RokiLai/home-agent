// Package health 提供设备健康状态编排、事件发布与定时巡检服务。
package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// EventListener 定义健康变更事件监听器接口。
type EventListener func(ctx context.Context, events []HealthEvent)

// Service 编排健康评估流程、存储快照与历史并向外部提供统一查询接口。
type Service struct {
	mu            sync.Mutex
	cfg           EvaluatorConfig
	repo          Repository
	factsPort     DeviceFactsPort
	sshPort       SSHDesiredPort
	ddnsPort      DDNSPort
	cmdPort       CommandPort
	versionPort   VersionPolicyPort
	clock         Clock
	listeners     []EventListener
	evaluatingMu  sync.Map
}

// ServiceConfig 封装创建健康服务所需的各项参数。
type ServiceConfig struct {
	Config      EvaluatorConfig
	Repo        Repository
	FactsPort   DeviceFactsPort
	SSHPort     SSHDesiredPort
	DDNSPort    DDNSPort
	CmdPort     CommandPort
	VersionPort VersionPolicyPort
	Clock       Clock
}

// NewService 创建健康评估与编排服务实例。
func NewService(cfg ServiceConfig) *Service {
	clk := cfg.Clock
	if clk == nil {
		clk = NewRealClock()
	}
	return &Service{
		cfg:         cfg.Config,
		repo:        cfg.Repo,
		factsPort:   cfg.FactsPort,
		sshPort:     cfg.SSHPort,
		ddnsPort:    cfg.DDNSPort,
		cmdPort:     cfg.CmdPort,
		versionPort: cfg.VersionPort,
		clock:       clk,
	}
}

// RegisterListener 注册健康状态变更事件的监听器。
func (s *Service) RegisterListener(l EventListener) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, l)
}

// EvaluateDevice 收集指定设备当前的所有事实并执行一次原子健康求值。
func (s *Service) EvaluateDevice(ctx context.Context, deviceID string) (HealthSnapshot, error) {
	if deviceID == "" {
		return HealthSnapshot{}, errors.New("empty device id")
	}

	// 单设备防并发重复求值
	val, _ := s.evaluatingMu.LoadOrStore(deviceID, &sync.Mutex{})
	devMu := val.(*sync.Mutex)
	devMu.Lock()
	defer devMu.Unlock()

	now := s.clock.Now()

	// 1. 读取基础 Facts
	var facts *DeviceFactSummary
	if s.factsPort != nil {
		f, err := s.factsPort.GetDeviceFacts(ctx, deviceID)
		if err == nil {
			facts = f
		}
	}

	// 2. 读取 SSH 期望
	var sshDesired *SSHDesiredInfo
	if s.sshPort != nil {
		ver, hash, enabled, err := s.sshPort.GetDesiredKeySet(ctx)
		if err == nil {
			sshDesired = &SSHDesiredInfo{
				Version: ver,
				Hash:    hash,
				Enabled: enabled,
			}
		}
	}

	// 3. 读取 DDNS 状态
	var ddnsState *DDNSDeviceState
	if s.ddnsPort != nil {
		d, err := s.ddnsPort.GetDeviceDDNSState(ctx, deviceID)
		if err == nil {
			ddnsState = d
		}
	}

	// 4. 读取升级与 SSH 命令状态
	var upgradeCmd *CommandSummary
	var sshCmd *CommandSummary
	if s.cmdPort != nil {
		if c, err := s.cmdPort.GetLatestCommand(ctx, deviceID, "upgrade"); err == nil {
			upgradeCmd = c
		}
		if c, err := s.cmdPort.GetLatestCommand(ctx, deviceID, "ssh_keys"); err == nil {
			sshCmd = c
		}
	}

	// 5. 读取版本策略
	var versionPolicy *VersionPolicyInfo
	if s.versionPort != nil {
		rec, min, err := s.versionPort.GetVersionPolicy(ctx)
		if err == nil {
			versionPolicy = &VersionPolicyInfo{
				RecommendedVersion:      rec,
				MinimumSupportedVersion: min,
			}
		}
	}

	// 6. 读取前序快照
	prev, _ := s.repo.GetSnapshot(ctx, deviceID)

	input := EvaluationInput{
		Device:        facts,
		SSHDesired:    sshDesired,
		DDNSState:     ddnsState,
		UpgradeCmd:    upgradeCmd,
		SSHCmd:        sshCmd,
		VersionPolicy: versionPolicy,
		Previous:      prev,
		Now:           now,
	}

	snapshot, events := Evaluate(s.cfg, input)

	if err := s.repo.SaveSnapshot(ctx, snapshot); err != nil {
		slog.Error("failed to save health snapshot", "device_id", deviceID, "error", err)
		return snapshot, err
	}

	if len(events) > 0 {
		if err := s.repo.AppendEvents(ctx, events); err != nil {
			slog.Error("failed to append health events", "device_id", deviceID, "error", err)
			return snapshot, fmt.Errorf("append health events: %w", err)
		}
		// 严格在事件成功落盘后，才通知已注册的监听器（如告警模块）
		s.notifyListeners(ctx, events)
	}

	return snapshot, nil
}

// EvaluateAll 对全部受管设备执行一次全量健康评估。
func (s *Service) EvaluateAll(ctx context.Context) error {
	if s.factsPort == nil {
		return nil
	}
	ids, err := s.factsPort.ListAllDeviceIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.EvaluateDevice(ctx, id); err != nil {
			slog.Warn("health evaluation failed for device", "device_id", id, "error", err)
		}
	}
	return nil
}

// StartSweep 启动后台定时全量巡检协程。
func (s *Service) StartSweep(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 1 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.EvaluateAll(ctx)
			}
		}
	}()
}

// GetSnapshot 获取指定设备的最新健康快照。
func (s *Service) GetSnapshot(ctx context.Context, deviceID string) (*HealthSnapshot, error) {
	return s.repo.GetSnapshot(ctx, deviceID)
}

// ListSnapshots 获取全部设备的健康快照列表。
func (s *Service) ListSnapshots(ctx context.Context) ([]HealthSnapshot, error) {
	return s.repo.ListSnapshots(ctx)
}

// ListEvents 分页获取健康历史事件。
func (s *Service) ListEvents(ctx context.Context, deviceID string, cursor string, limit int) ([]HealthEvent, string, error) {
	return s.repo.ListEvents(ctx, deviceID, cursor, limit)
}

// GetSummary 获取全局设备健康统计概览。
func (s *Service) GetSummary(ctx context.Context) (Summary, error) {
	return s.repo.GetSummary(ctx)
}

func (s *Service) notifyListeners(ctx context.Context, events []HealthEvent) {
	s.mu.Lock()
	listeners := make([]EventListener, len(s.listeners))
	copy(listeners, s.listeners)
	s.mu.Unlock()

	for _, l := range listeners {
		l(ctx, events)
	}
}

