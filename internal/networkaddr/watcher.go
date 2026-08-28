package networkaddr

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// WatcherConfig 定义监控网络接口地址变更的参数配置。
type WatcherConfig struct {
	// Interface 指定要监听的网络接口名称（留空或 "auto" 则监听所有活动非回环接口）。
	Interface string
	// DebounceDuration 指定接收到触发信号后进行采样的防抖等待窗口。
	DebounceDuration time.Duration
	// HeartbeatInterval 指定无地址变更时无条件发送心跳快照的周期。
	HeartbeatInterval time.Duration
	// PollInterval 指定后台周期性主动轮询接口的频率。
	PollInterval time.Duration
	// Provider 底层平台地址采集提供器。
	Provider AddressProvider
	// Logger 结构化日志记录器实例。
	Logger *slog.Logger
	// OnSnapshot 当检测到地址变更或心跳触发时的回调处理函数。
	OnSnapshot func(snapshot []ReportedIPv6Address, changed bool)
}

// Watcher 管理接口监控例程、事件防抖和周期性快照触发。
type Watcher struct {
	cfg          WatcherConfig
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	mu           sync.Mutex
	lastSnapshot []ReportedIPv6Address
	triggerCh    chan struct{}
}

// NewWatcher 创建配置了默认防抖参数的网络地址变化监听器。
func NewWatcher(cfg WatcherConfig) *Watcher {

	if cfg.DebounceDuration <= 0 {
		cfg.DebounceDuration = 2 * time.Second
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 10 * time.Minute
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.Provider == nil {
		cfg.Provider = NewDefaultProvider()
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Watcher{
		cfg:       cfg,
		ctx:       ctx,
		cancel:    cancel,
		triggerCh: make(chan struct{}, 1),
	}
}

// Start begins watching network interfaces and periodic heartbeats.
func (w *Watcher) Start() {
	w.wg.Add(1)
	go w.run()
}

// Trigger forces an immediate address check.
func (w *Watcher) Trigger() {
	select {
	case w.triggerCh <- struct{}{}:
	default:
	}
}

// Stop terminates the watcher and waits for all goroutines to finish.
func (w *Watcher) Stop() {
	w.cancel()
	w.wg.Wait()
}

func (w *Watcher) run() {
	defer w.wg.Done()

	// Initial immediate check
	w.checkAndEmit(true)

	heartbeatTicker := time.NewTicker(w.cfg.HeartbeatInterval)
	defer heartbeatTicker.Stop()

	pollTicker := time.NewTicker(w.cfg.PollInterval)
	defer pollTicker.Stop()

	var debounceTimer *time.Timer
	var debounceCh <-chan time.Time

	for {
		select {
		case <-w.ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return

		case <-w.triggerCh:
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.NewTimer(w.cfg.DebounceDuration)
			debounceCh = debounceTimer.C

		case <-debounceCh:
			debounceTimer = nil
			debounceCh = nil
			w.checkAndEmit(false)

		case <-pollTicker.C:
			w.checkAndEmit(false)

		case <-heartbeatTicker.C:
			w.checkAndEmit(true)
		}
	}
}

func (w *Watcher) checkAndEmit(forceEmit bool) {
	ctx, cancel := context.WithTimeout(w.ctx, 5*time.Second)
	defer cancel()

	addrs, err := w.cfg.Provider.GetAddresses(ctx, w.cfg.Interface)
	if err != nil {
		if w.cfg.Logger != nil {
			w.cfg.Logger.Error("failed to get ipv6 addresses", "error", err)
		}
		return
	}

	w.mu.Lock()
	changed := !AddressesEqual(w.lastSnapshot, addrs)
	if changed {
		w.lastSnapshot = make([]ReportedIPv6Address, len(addrs))
		copy(w.lastSnapshot, addrs)
	}
	w.mu.Unlock()

	if (changed || forceEmit) && w.cfg.OnSnapshot != nil {
		w.cfg.OnSnapshot(addrs, changed)
	}
}

// GetCurrentSnapshot returns the last known address snapshot.
func (w *Watcher) GetCurrentSnapshot() []ReportedIPv6Address {
	w.mu.Lock()
	defer w.mu.Unlock()
	res := make([]ReportedIPv6Address, len(w.lastSnapshot))
	copy(res, w.lastSnapshot)
	return res
}
