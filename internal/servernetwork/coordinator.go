package servernetwork

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"homeagent/internal/ddns"
	"homeagent/internal/networkaddr"
)

// Config 定义服务端 IPv6 自更新协调器的配置项。
type Config struct {
	Enabled         bool
	Interface       string
	Records         []string
	Interval        time.Duration
	Debounce        time.Duration
	MaxBackoff      time.Duration
	TTL             int
	ConflictChecker func(records []string) error
}

// StatusView 提供给外部或 API 展示的服务端网络自举当前状态快照。
type StatusView struct {
	Enabled         bool         `json:"enabled"`
	Interface       string       `json:"interface"`
	Records         []string     `json:"records"`
	Status          RecordStatus `json:"status"`
	CurrentAddress  string       `json:"current_address"`
	LastSuccessTime *time.Time   `json:"last_success_time,omitempty"`
	LastError       string       `json:"last_error,omitempty"`
}

// Coordinator 实现服务端 DNS 自更新、自举恢复与独占调和控制器。
type Coordinator struct {
	cfg       Config
	collector *Collector
	publisher ddns.DNSPublisher
	store     StateStore
	logger    *slog.Logger

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
	doneCh  chan struct{}
	watcher *networkaddr.Watcher
}

// NewCoordinator 创建 Coordinator 实例并校验配置。
func NewCoordinator(cfg Config, collector *Collector, publisher ddns.DNSPublisher, store StateStore, logger *slog.Logger) (*Coordinator, error) {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.Debounce <= 0 {
		cfg.Debounce = 3 * time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 60 * time.Second
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 300
	}
	if logger == nil {
		logger = slog.Default()
	}

	// 规范化并去重受管域名
	normalizedRecords := make([]string, 0, len(cfg.Records))
	seen := make(map[string]bool)
	for _, r := range cfg.Records {
		rec := strings.TrimSpace(strings.ToLower(r))
		if rec != "" && !seen[rec] {
			seen[rec] = true
			normalizedRecords = append(normalizedRecords, rec)
		}
	}
	slices.Sort(normalizedRecords)
	cfg.Records = normalizedRecords

	if cfg.Enabled {
		if cfg.Interface == "" {
			return nil, errors.New("server ipv6 interface cannot be empty when self update is enabled")
		}
		if len(cfg.Records) == 0 {
			return nil, errors.New("server ddns records cannot be empty when self update is enabled")
		}
		if cfg.ConflictChecker != nil {
			if err := cfg.ConflictChecker(cfg.Records); err != nil {
				return nil, fmt.Errorf("ddns record conflict check: %w", err)
			}
		}
	}

	return &Coordinator{
		cfg:       cfg,
		collector: collector,
		publisher: publisher,
		store:     store,
		logger:    logger,
	}, nil
}

// Start 启动后台协调器，包含初始同步、事件监听与定期轮询。
func (c *Coordinator) Start(ctx context.Context) {
	c.mu.Lock()
	if c.running || !c.cfg.Enabled {
		c.mu.Unlock()
		return
	}
	c.running = true
	c.stopCh = make(chan struct{})
	c.doneCh = make(chan struct{})
	c.mu.Unlock()

	c.logger.Info("starting_server_ipv6_coordinator", "interface", c.cfg.Interface, "records", c.cfg.Records)

	// 启动网络事件监听 Watcher
	if c.collector != nil && c.collector.provider != nil {
		c.watcher = networkaddr.NewWatcher(networkaddr.WatcherConfig{
			Interface:         c.cfg.Interface,
			DebounceDuration:  c.cfg.Debounce,
			HeartbeatInterval: c.cfg.Interval * 2,
			PollInterval:      c.cfg.Interval,
			Provider:          c.collector.provider,
			OnSnapshot: func(snapshot []networkaddr.ReportedIPv6Address, changed bool) {
				if changed {
					c.logger.Info("server_interface_address_changed_triggering_reconcile", "interface", c.cfg.Interface)
					_ = c.Reconcile(context.Background())
				}
			},
		})
		c.watcher.Start()
	}

	go c.loop(ctx)
}

// Stop 停止后台协调器。
func (c *Coordinator) Stop() {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return
	}
	c.running = false
	close(c.stopCh)
	if c.watcher != nil {
		c.watcher.Stop()
	}
	c.mu.Unlock()

	<-c.doneCh
	c.logger.Info("server_ipv6_coordinator_stopped")
}

func (c *Coordinator) loop(ctx context.Context) {
	defer close(c.doneCh)

	// 启动立即执行一次协调
	_ = c.Reconcile(ctx)

	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.Reconcile(ctx)
		}
	}
}

// Reconcile 执行一次完整的地址探测、状态比对、DNS 写入与查证。
func (c *Coordinator) Reconcile(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	state, err := c.store.Load()
	if err != nil {
		c.logger.Error("failed_to_load_server_network_state", "error", err)
		return fmt.Errorf("load state: %w", err)
	}

	now := time.Now().UTC()

	// 若未启用，更新状态为 disabled 并保存
	if !c.cfg.Enabled {
		for _, rec := range c.cfg.Records {
			rs := state.Records[rec]
			if rs == nil {
				rs = &RecordState{Record: rec}
				state.Records[rec] = rs
			}
			rs.Status = StatusDisabled
			rs.UpdatedAt = now
		}
		_ = c.store.Save(state)
		return nil
	}

	// 找出当前已确认的首个有效地址作为探测基准
	var currentConfirmed netip.Addr
	for _, rec := range c.cfg.Records {
		if rs := state.Records[rec]; rs != nil && rs.ConfirmedAddress != "" {
			if ip, err := netip.ParseAddr(rs.ConfirmedAddress); err == nil && ip.IsValid() {
				currentConfirmed = ip
				break
			}
		}
	}

	// 探测物理网卡地址
	desiredIP, collectErr := c.collector.Collect(ctx, currentConfirmed)
	if collectErr != nil {
		c.logger.Warn("server_ipv6_collect_failed_or_no_valid_address", "interface", c.cfg.Interface, "error", collectErr)
		for _, rec := range c.cfg.Records {
			rs := state.Records[rec]
			if rs == nil {
				rs = &RecordState{Record: rec}
				state.Records[rec] = rs
			}
			rs.Status = StatusWaitingAddress
			rs.LastError = collectErr.Error()
			rs.UpdatedAt = now
		}
		_ = c.store.Save(state)
		return nil
	}

	desiredAddrStr := desiredIP.String()

	// 逐条记录调和
	for _, rec := range c.cfg.Records {
		rs := state.Records[rec]
		if rs == nil {
			rs = &RecordState{
				Record:        rec,
				ConfigVersion: 1,
			}
			state.Records[rec] = rs
		}

		// 检查退避重试时间
		if rs.NextRetryTime != nil && now.Before(*rs.NextRetryTime) && rs.Status == StatusFailed && rs.DesiredAddress == desiredAddrStr {
			continue
		}

		needSync := rs.ConfirmedAddress != desiredAddrStr || rs.Status != StatusSynced

		// 如果已有记录状态为 synced 且地址匹配，执行定期查证防外部改写
		if !needSync {
			remoteAddrs, checkErr := c.publisher.GetAAAA(ctx, rec)
			if checkErr == nil {
				hasDesired := false
				for _, rIP := range remoteAddrs {
					if rIP == desiredIP {
						hasDesired = true
						break
					}
				}
				if !hasDesired {
					c.logger.Warn("detected_external_dns_rewrite_for_server_record", "record", rec, "expected", desiredAddrStr, "actual", remoteAddrs)
					needSync = true
				}
			}
		}

		if !needSync {
			continue
		}

		// 更新期望地址与版本
		if rs.DesiredAddress != desiredAddrStr {
			rs.DesiredAddress = desiredAddrStr
			rs.DesiredVersion++
		}
		rs.Status = StatusSyncing
		rs.UpdatedAt = now
		_ = c.store.Save(state)

		c.logger.Info("syncing_server_ddns_record", "record", rec, "desired_address", desiredAddrStr, "version", rs.DesiredVersion)

		// 调用 DNS 服务商写入
		upsertErr := c.publisher.UpsertAAAA(ctx, rec, desiredIP, c.cfg.TTL)
		if upsertErr != nil {
			c.logger.Error("failed_to_upsert_server_ddns_record", "record", rec, "error", upsertErr)
			rs.Status = StatusFailed
			rs.LastError = upsertErr.Error()
			rs.RetryCount++
			backoff := c.calculateBackoff(rs.RetryCount)
			nextRetry := now.Add(backoff)
			rs.NextRetryTime = &nextRetry
			rs.UpdatedAt = now
			_ = c.store.Save(state)
			continue
		}

		// 查证写入结果
		rs.Status = StatusVerifying
		rs.UpdatedAt = now
		_ = c.store.Save(state)

		verifiedAddrs, getErr := c.publisher.GetAAAA(ctx, rec)
		converged := false
		if getErr == nil {
			for _, vIP := range verifiedAddrs {
				if vIP == desiredIP {
					converged = true
					break
				}
			}
		}

		if !converged {
			errMsg := "dns record verification failed: desired address not returned by provider"
			if getErr != nil {
				errMsg = fmt.Sprintf("dns query error: %v", getErr)
			}
			c.logger.Warn("server_ddns_verification_pending_or_failed", "record", rec, "error", errMsg)
			rs.Status = StatusFailed
			rs.LastError = errMsg
			rs.RetryCount++
			backoff := c.calculateBackoff(rs.RetryCount)
			nextRetry := now.Add(backoff)
			rs.NextRetryTime = &nextRetry
			rs.UpdatedAt = now
			_ = c.store.Save(state)
			continue
		}

		// 查证收敛成功
		c.logger.Info("server_ddns_record_synced_successfully", "record", rec, "address", desiredAddrStr)
		rs.Status = StatusSynced
		rs.ConfirmedAddress = desiredAddrStr
		rs.ConfirmedVersion = rs.DesiredVersion
		rs.LastSuccessTime = &now
		rs.LastError = ""
		rs.RetryCount = 0
		rs.NextRetryTime = nil
		rs.UpdatedAt = now
		_ = c.store.Save(state)
	}

	return nil
}

func (c *Coordinator) calculateBackoff(retryCount int) time.Duration {
	if retryCount <= 0 {
		retryCount = 1
	}
	base := 3.0 * math.Pow(2, float64(retryCount-1))
	backoff := time.Duration(base) * time.Second
	if backoff > c.cfg.MaxBackoff {
		backoff = c.cfg.MaxBackoff
	}
	return backoff
}

// GetConfig 返回当前协调器生效的配置副本。
func (c *Coordinator) GetConfig() Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// GetStatus 获取服务端自举网络的综合状态视图。
func (c *Coordinator) GetStatus(ctx context.Context) StatusView {
	c.mu.Lock()
	defer c.mu.Unlock()

	view := StatusView{
		Enabled:   c.cfg.Enabled,
		Interface: c.cfg.Interface,
		Records:   c.cfg.Records,
		Status:    StatusDisabled,
	}

	if !c.cfg.Enabled {
		return view
	}

	state, err := c.store.Load()
	if err != nil || state == nil || len(state.Records) == 0 {
		view.Status = StatusPending
		return view
	}

	// 汇总首个受管域名的状态作为代表状态
	for _, rec := range c.cfg.Records {
		if rs := state.Records[rec]; rs != nil {
			view.Status = rs.Status
			view.CurrentAddress = rs.ConfirmedAddress
			if view.CurrentAddress == "" {
				view.CurrentAddress = rs.DesiredAddress
			}
			view.LastSuccessTime = rs.LastSuccessTime
			view.LastError = rs.LastError
			break
		}
	}

	return view
}

// UpdateConfig 动态更新自举网络配置并热生效。
func (c *Coordinator) UpdateConfig(ctx context.Context, newCfg Config) error {
	// 规范化并校验
	if newCfg.Interval <= 0 {
		newCfg.Interval = 30 * time.Second
	}
	if newCfg.Debounce <= 0 {
		newCfg.Debounce = 3 * time.Second
	}
	if newCfg.MaxBackoff <= 0 {
		newCfg.MaxBackoff = 60 * time.Second
	}
	if newCfg.TTL <= 0 {
		newCfg.TTL = 300
	}

	normalizedRecords := make([]string, 0, len(newCfg.Records))
	seen := make(map[string]bool)
	for _, r := range newCfg.Records {
		rec := strings.TrimSpace(strings.ToLower(r))
		if rec != "" && !seen[rec] {
			seen[rec] = true
			normalizedRecords = append(normalizedRecords, rec)
		}
	}
	slices.Sort(normalizedRecords)
	newCfg.Records = normalizedRecords

	if newCfg.Enabled {
		if newCfg.Interface == "" {
			return errors.New("server ipv6 interface cannot be empty when self update is enabled")
		}
		if len(newCfg.Records) == 0 {
			return errors.New("server ddns records cannot be empty when self update is enabled")
		}
		if c.cfg.ConflictChecker != nil {
			if err := c.cfg.ConflictChecker(newCfg.Records); err != nil {
				return fmt.Errorf("ddns record conflict check: %w", err)
			}
		}
	}

	c.mu.Lock()
	c.cfg.Enabled = newCfg.Enabled
	c.cfg.Interface = newCfg.Interface
	c.cfg.Records = newCfg.Records
	if c.collector != nil {
		c.collector.iface = newCfg.Interface
	}

	// 保存配置到持久化存储
	if cStore, ok := c.store.(ConfigStore); ok {
		_ = cStore.SaveConfig(&PersistedConfig{
			Enabled:   newCfg.Enabled,
			Interface: newCfg.Interface,
			Records:   newCfg.Records,
		})
	}

	// 动态管理 Watcher
	if c.running {
		if !newCfg.Enabled {
			if c.watcher != nil {
				c.watcher.Stop()
				c.watcher = nil
			}
		} else if c.watcher == nil && c.collector != nil && c.collector.provider != nil {
			c.watcher = networkaddr.NewWatcher(networkaddr.WatcherConfig{
				Interface:         c.cfg.Interface,
				DebounceDuration:  c.cfg.Debounce,
				HeartbeatInterval: c.cfg.Interval * 2,
				PollInterval:      c.cfg.Interval,
				Provider:          c.collector.provider,
				OnSnapshot: func(snapshot []networkaddr.ReportedIPv6Address, changed bool) {
					if changed {
						_ = c.Reconcile(context.Background())
					}
				},
			})
			c.watcher.Start()
		}
	}
	c.mu.Unlock()

	// 立即触发一次调和以刷新状态
	return c.Reconcile(ctx)
}
