package ddns

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"homeagent/internal/devicestate"
	"homeagent/internal/prefixstate"
)

// DeviceConfig 定义目标主机的 DDNS 域名记录与所属网络映射。
type DeviceConfig struct {
	NetworkID string `json:"network_id" yaml:"network_id"`
	Record    string `json:"record" yaml:"record"`
	Interface string `json:"interface,omitempty" yaml:"interface,omitempty"`
}

// NetworkConfig 定义局域网绑定的网关路由器设备 ID 及前缀过期容忍时长。
type NetworkConfig struct {
	RouterDeviceID string        `json:"router_device_id" yaml:"router_device_id"`
	PrefixStateTTL time.Duration `json:"prefix_state_ttl" yaml:"prefix_state_ttl"`
}

// Config 包含 DDNS 服务的全量配置：宽限期时长、同步超时、DNS TTL 以及设备和网络映射表。
type Config struct {
	Enabled                 bool                     `json:"enabled" yaml:"enabled"`
	EmptyAddressGracePeriod time.Duration            `json:"empty_address_grace_period" yaml:"empty_address_grace_period"`
	SyncTimeout             time.Duration            `json:"sync_timeout" yaml:"sync_timeout"`
	TTL                     int                      `json:"ttl" yaml:"ttl"`
	Networks                map[string]NetworkConfig `json:"networks" yaml:"networks"`
	Devices                 map[string]DeviceConfig  `json:"devices" yaml:"devices"`
}

// Service 协调设备上报地址与路由器局域网 IPv6 前缀的调和对齐，并在必要时更新或删除 DNS AAAA 记录。
type Service struct {
	cfg       Config
	devSvc    *devicestate.Service
	prefSvc   *prefixstate.Service
	publisher DNSPublisher
	logger    *slog.Logger
	deviceMu  sync.Map // 单设备并发互斥锁
}

// NewService 构建并初始化 DDNS 调度服务实例，注入默认回退值。
func NewService(

	cfg Config,
	devSvc *devicestate.Service,
	prefSvc *prefixstate.Service,
	pub DNSPublisher,
	logger *slog.Logger,
) *Service {
	if cfg.EmptyAddressGracePeriod <= 0 {
		cfg.EmptyAddressGracePeriod = 10 * time.Minute
	}
	if cfg.SyncTimeout <= 0 {
		cfg.SyncTimeout = 10 * time.Second
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 120
	}
	if cfg.Networks == nil {
		cfg.Networks = make(map[string]NetworkConfig)
	}
	if cfg.Devices == nil {
		cfg.Devices = make(map[string]DeviceConfig)
	}

	return &Service{
		cfg:       cfg,
		devSvc:    devSvc,
		prefSvc:   prefSvc,
		publisher: pub,
		logger:    logger,
	}
}

func (s *Service) getDeviceLock(deviceID string) *sync.Mutex {
	val, _ := s.deviceMu.LoadOrStore(deviceID, &sync.Mutex{})
	return val.(*sync.Mutex)
}

// ReconcileNetwork triggers reconciliation for all configured devices in the specified network.
func (s *Service) ReconcileNetwork(ctx context.Context, networkID string) error {
	var errs []error
	for devID, devCfg := range s.cfg.Devices {
		if devCfg.NetworkID == networkID {
			if err := s.ReconcileDevice(ctx, devID); err != nil {
				errs = append(errs, fmt.Errorf("device %s: %w", devID, err))
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// ReconcileAll reconciles all configured devices and cleans up expired grace periods.
func (s *Service) ReconcileAll(ctx context.Context) error {
	var errs []error
	for devID := range s.cfg.Devices {
		if err := s.ReconcileDevice(ctx, devID); err != nil {
			errs = append(errs, fmt.Errorf("device %s: %w", devID, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// ReconcileDevice converges a device's reported address snapshot with router prefixes and updates DDNS.
func (s *Service) ReconcileDevice(ctx context.Context, deviceID string) error {
	if !s.cfg.Enabled || s.publisher == nil {
		return nil
	}

	devCfg, ok := s.cfg.Devices[deviceID]
	if !ok || devCfg.Record == "" {
		return nil // Not configured for DDNS
	}

	mu := s.getDeviceLock(deviceID)
	mu.Lock()
	defer mu.Unlock()

	st, err := s.devSvc.Get(deviceID)
	if err != nil {
		if errors.Is(err, devicestate.ErrDeviceNotFound) {
			return nil
		}
		return err
	}

	networkID := devCfg.NetworkID
	if networkID == "" {
		networkID = st.NetworkID
	}
	netCfg := s.cfg.Networks[networkID]
	staleTTL := netCfg.PrefixStateTTL
	if staleTTL <= 0 {
		staleTTL = 15 * time.Minute
	}

	now := time.Now().UTC()
	activePrefixes, isStale, err := s.prefSvc.GetActivePrefixes(networkID, now, staleTTL)
	if err != nil && !errors.Is(err, prefixstate.ErrNetworkNotFound) {
		return err
	}

	if isStale {
		if s.logger != nil {
			s.logger.Warn("router prefix is stale, skipping new ddns updates to avoid misconfiguration",
				"device_id", deviceID, "network_id", networkID)
		}
		return nil
	}

	// Intersect reported addresses with active LAN prefixes
	validCandidates := prefixstate.Intersect(st.ReportedAddresses, activePrefixes)
	desiredIP, hasDesired := prefixstate.SelectAddress(validCandidates, st.AppliedAddress)

	syncCtx, cancel := context.WithTimeout(ctx, s.cfg.SyncTimeout)
	defer cancel()

	if hasDesired {
		// Valid address available
		st.GracePeriodEnd = nil
		desiredStr := desiredIP.String()
		st.DesiredAddress = desiredStr

		if st.AppliedAddress == desiredStr && st.SyncStatus == devicestate.SyncStatusSynced {
			// Already in desired synced state
			return nil
		}

		st.SyncStatus = devicestate.SyncStatusSyncing
		_ = s.devSvc.Save(*st)

		err := s.publisher.UpsertAAAA(syncCtx, devCfg.Record, desiredIP, s.cfg.TTL)
		now = time.Now().UTC()
		st.SyncUpdatedAt = now

		if err != nil {
			st.SyncStatus = devicestate.SyncStatusFailed
			st.SyncError = err.Error()
			_ = s.devSvc.Save(*st)
			if s.logger != nil {
				s.logger.Error("failed to upsert ddns AAAA record",
					"device_id", deviceID, "record", devCfg.Record, "address", desiredStr, "error", err)
			}
			return err
		}

		st.AppliedAddress = desiredStr
		st.AppliedRevision = st.Revision
		st.SyncStatus = devicestate.SyncStatusSynced
		st.SyncError = ""
		_ = s.devSvc.Save(*st)

		if s.logger != nil {
			s.logger.Info("ddns AAAA record synced",
				"device_id", deviceID, "record", devCfg.Record, "address", desiredStr, "revision", st.Revision)
		}
		return nil
	}

	// No valid address available
	if st.AppliedAddress == "" {
		st.DesiredAddress = ""
		st.SyncStatus = devicestate.SyncStatusSynced
		st.SyncUpdatedAt = now
		_ = s.devSvc.Save(*st)
		return nil
	}

	// Currently has an applied address, but no candidate now
	if st.GracePeriodEnd == nil {
		// Enter grace period
		graceEnd := now.Add(s.cfg.EmptyAddressGracePeriod)
		st.GracePeriodEnd = &graceEnd
		st.SyncStatus = devicestate.SyncStatusGracePeriod
		st.SyncUpdatedAt = now
		_ = s.devSvc.Save(*st)
		if s.logger != nil {
			s.logger.Info("entered ddns grace period before deleting AAAA record",
				"device_id", deviceID, "record", devCfg.Record, "grace_until", graceEnd)
		}
		return nil
	}

	if now.Before(*st.GracePeriodEnd) {
		// Grace period still active, keep existing AAAA
		return nil
	}

	// Grace period expired, delete AAAA record
	st.SyncStatus = devicestate.SyncStatusSyncing
	_ = s.devSvc.Save(*st)

	err = s.publisher.DeleteAAAA(syncCtx, devCfg.Record)
	now = time.Now().UTC()
	st.SyncUpdatedAt = now

	if err != nil {
		st.SyncStatus = devicestate.SyncStatusFailed
		st.SyncError = err.Error()
		_ = s.devSvc.Save(*st)
		if s.logger != nil {
			s.logger.Error("failed to delete expired ddns AAAA record",
				"device_id", deviceID, "record", devCfg.Record, "error", err)
		}
		return err
	}

	st.AppliedAddress = ""
	st.DesiredAddress = ""
	st.GracePeriodEnd = nil
	st.SyncStatus = devicestate.SyncStatusSynced
	st.SyncError = ""
	_ = s.devSvc.Save(*st)

	if s.logger != nil {
		s.logger.Info("ddns AAAA record deleted after grace period expired",
			"device_id", deviceID, "record", devCfg.Record)
	}
	return nil
}

// StartBackgroundSweep periodically runs reconciliation to handle grace period expirations and retries.
func (s *Service) StartBackgroundSweep(ctx context.Context, sweepInterval time.Duration) {
	if sweepInterval <= 0 {
		sweepInterval = 1 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.ReconcileAll(ctx)
			}
		}
	}()
}
