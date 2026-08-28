// Package devicestate 管理设备上报的 IPv6 网络状态、版本修订号及 DDNS 同步状态。
package devicestate

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"

	"homeagent/internal/networkaddr"
)

var (
	// ErrRevisionConflict 当接收到的上报版本号小于当前已保存版本时返回此错误。
	ErrRevisionConflict = errors.New("revision conflict: received older revision")
	// ErrRevisionContentMismatch 当版本号相同但上报的地址内容不一致时返回此错误。
	ErrRevisionContentMismatch = errors.New("revision conflict: identical revision with different payload")
	// ErrDeviceNotFound 当查询的设备网络状态不存在时返回此错误。
	ErrDeviceNotFound = errors.New("device network state not found")
)

// RevisionConflictKind 表示版本冲突的错误类型。
type RevisionConflictKind string

const (
	// RevisionConflictOlder 表示上报的版本号小于当前已保存版本。
	RevisionConflictOlder RevisionConflictKind = "revision_conflict"
	// RevisionConflictMismatch 表示版本号相同但上报内容不一致。
	RevisionConflictMismatch RevisionConflictKind = "revision_content_mismatch"
)

// RevisionConflictError 记录带原子状态快照的版本冲突错误。
type RevisionConflictError struct {
	Kind     RevisionConflictKind
	Current  uint64
	Received uint64
}

func (e *RevisionConflictError) Error() string {
	if e.Kind == RevisionConflictMismatch {
		return fmt.Sprintf("revision conflict: identical revision %d with different payload (current %d)", e.Received, e.Current)
	}
	return fmt.Sprintf("revision conflict: received %d, current is %d", e.Received, e.Current)
}

func (e *RevisionConflictError) Is(target error) bool {
	if target == ErrRevisionConflict && e.Kind == RevisionConflictOlder {
		return true
	}
	if target == ErrRevisionContentMismatch && e.Kind == RevisionConflictMismatch {
		return true
	}
	return false
}

const (
	// SyncStatusPending 表示地址已上报，等待同步到 DNS。
	SyncStatusPending = "pending"
	// SyncStatusSyncing 表示正在执行 DNS 记录同步。
	SyncStatusSyncing = "syncing"
	// SyncStatusSynced 表示 DNS 记录已成功同步。
	SyncStatusSynced = "synced"
	// SyncStatusFailed 表示最近一次同步操作发生错误。
	SyncStatusFailed = "failed"
	// SyncStatusGracePeriod 表示无可用候选地址，处于删除 DNS 记录前的宽限期。
	SyncStatusGracePeriod = "grace_period"
)

// DeviceIPv6State 记录单台设备的 IPv6 快照、期望地址、已生效地址及同步生命周期状态。
type DeviceIPv6State struct {
	DeviceID          string                            `json:"device_id"`
	NetworkID         string                            `json:"network_id"`
	Revision          uint64                            `json:"revision"`
	ObservedAt        time.Time                         `json:"observed_at"`
	ReportedAddresses []networkaddr.ReportedIPv6Address `json:"reported_addresses"`
	DesiredAddress    string                            `json:"desired_address,omitempty"`
	AppliedAddress    string                            `json:"applied_address,omitempty"`
	AppliedRevision   uint64                            `json:"applied_revision,omitempty"`
	SyncStatus        string                            `json:"sync_status"`
	SyncError         string                            `json:"sync_error,omitempty"`
	SyncUpdatedAt     time.Time                         `json:"sync_updated_at,omitempty"`
	GracePeriodEnd    *time.Time                        `json:"grace_period_end,omitempty"`
}

// Store 定义 DeviceIPv6State 的持久化存储接口。
type Store interface {
	Get(deviceID string) (*DeviceIPv6State, error)
	Save(state DeviceIPv6State) error
	List() ([]DeviceIPv6State, error)
}

// MemoryStore 是 Store 接口的线程安全内存实现。
type MemoryStore struct {
	mu     sync.RWMutex
	states map[string]DeviceIPv6State
}

// NewMemoryStore 创建内存状态存储实例。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		states: make(map[string]DeviceIPv6State),
	}
}

// Get 根据设备 ID 获取其 IPv6 网络状态。
func (s *MemoryStore) Get(deviceID string) (*DeviceIPv6State, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.states[deviceID]
	if !ok {
		return nil, ErrDeviceNotFound
	}
	cpy := st
	return &cpy, nil
}

// Save 保存或更新设备 IPv6 状态。
func (s *MemoryStore) Save(state DeviceIPv6State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[state.DeviceID] = state
	return nil
}

// List 返回所有已存储设备状态的列表快照。
func (s *MemoryStore) List() ([]DeviceIPv6State, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]DeviceIPv6State, 0, len(s.states))
	for _, st := range s.states {
		list = append(list, st)
	}
	return list, nil
}

// Service 协调设备网络状态的上报校验、版本控制与持久化管理。
type Service struct {
	store Store
	mu    sync.Mutex
}

// NewService 创建基于指定存储实现的 Service 实例。
func NewService(store Store) *Service {
	if store == nil {
		store = NewMemoryStore()
	}
	return &Service{store: store}
}

// UpdateReportedAddresses 校验并应用设备 Agent 上报的新地址快照，通过版本号控制并发冲突。
func (s *Service) UpdateReportedAddresses(
	deviceID, networkID string,
	revision uint64,
	observedAt time.Time,
	rawAddresses []networkaddr.ReportedIPv6Address,
) (state *DeviceIPv6State, changed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	normalized := networkaddr.NormalizeAndFilterCandidates(rawAddresses, observedAt)

	existing, err := s.store.Get(deviceID)
	if err != nil && !errors.Is(err, ErrDeviceNotFound) {
		return nil, false, err
	}

	if existing == nil {
		newState := DeviceIPv6State{
			DeviceID:          deviceID,
			NetworkID:         networkID,
			Revision:          revision,
			ObservedAt:        observedAt,
			ReportedAddresses: normalized,
			SyncStatus:        SyncStatusPending,
			SyncUpdatedAt:     time.Now().UTC(),
		}
		if err := s.store.Save(newState); err != nil {
			return nil, false, err
		}
		return &newState, true, nil
	}

	// 版本号防回退校验
	if revision < existing.Revision {
		return nil, false, &RevisionConflictError{
			Kind:     RevisionConflictOlder,
			Current:  existing.Revision,
			Received: revision,
		}
	}

	if revision == existing.Revision {
		if networkaddr.AddressesEqual(existing.ReportedAddresses, normalized) {
			return existing, false, nil
		}
		return nil, false, &RevisionConflictError{
			Kind:     RevisionConflictMismatch,
			Current:  existing.Revision,
			Received: revision,
		}
	}

	// revision > existing.Revision
	changed = !networkaddr.AddressesEqual(existing.ReportedAddresses, normalized)
	existing.Revision = revision
	existing.ObservedAt = observedAt
	if networkID != "" {
		existing.NetworkID = networkID
	}
	existing.ReportedAddresses = normalized
	if changed {
		existing.SyncStatus = SyncStatusPending
		existing.SyncUpdatedAt = time.Now().UTC()
	}

	if err := s.store.Save(*existing); err != nil {
		return nil, false, err
	}
	return existing, changed, nil
}

// Get 查询指定设备的 IPv6 状态。
func (s *Service) Get(deviceID string) (*DeviceIPv6State, error) {
	return s.store.Get(deviceID)
}

// List 列出所有已记录的设备 IPv6 状态。
func (s *Service) List() ([]DeviceIPv6State, error) {
	return s.store.List()
}

// Save 持久化保存设备 IPv6 状态。
func (s *Service) Save(state DeviceIPv6State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.Save(state)
}

// ParseCandidateAddrs 将上报的地址切片转换为合法已解析的 netip.Addr 切片。
func ParseCandidateAddrs(addrs []networkaddr.ReportedIPv6Address) []netip.Addr {
	var ips []netip.Addr
	for _, a := range addrs {
		ip, err := netip.ParseAddr(strings.TrimSpace(a.Address))
		if err == nil && ip.IsValid() {
			ips = append(ips, ip)
		}
	}
	return ips
}
