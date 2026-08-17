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
	ErrRevisionConflict        = errors.New("revision conflict: received older revision")
	ErrRevisionContentMismatch = errors.New("revision conflict: identical revision with different payload")
	ErrDeviceNotFound          = errors.New("device network state not found")
)

const (
	SyncStatusPending     = "pending"
	SyncStatusSyncing     = "syncing"
	SyncStatusSynced      = "synced"
	SyncStatusFailed      = "failed"
	SyncStatusGracePeriod = "grace_period"
)

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

type Store interface {
	Get(deviceID string) (*DeviceIPv6State, error)
	Save(state DeviceIPv6State) error
	List() ([]DeviceIPv6State, error)
}

type MemoryStore struct {
	mu     sync.RWMutex
	states map[string]DeviceIPv6State
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		states: make(map[string]DeviceIPv6State),
	}
}

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

func (s *MemoryStore) Save(state DeviceIPv6State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[state.DeviceID] = state
	return nil
}

func (s *MemoryStore) List() ([]DeviceIPv6State, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]DeviceIPv6State, 0, len(s.states))
	for _, st := range s.states {
		list = append(list, st)
	}
	return list, nil
}

type Service struct {
	store Store
	mu    sync.Mutex
}

func NewService(store Store) *Service {
	if store == nil {
		store = NewMemoryStore()
	}
	return &Service{store: store}
}

// UpdateReportedAddresses validates and applies a new address snapshot reported by a device agent.
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

	// Revision verification
	if revision < existing.Revision {
		return nil, false, fmt.Errorf("%w: received %d, current is %d", ErrRevisionConflict, revision, existing.Revision)
	}

	if revision == existing.Revision {
		if networkaddr.AddressesEqual(existing.ReportedAddresses, normalized) {
			return existing, false, nil
		}
		return nil, false, ErrRevisionContentMismatch
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

func (s *Service) Get(deviceID string) (*DeviceIPv6State, error) {
	return s.store.Get(deviceID)
}

func (s *Service) List() ([]DeviceIPv6State, error) {
	return s.store.List()
}

func (s *Service) Save(state DeviceIPv6State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.Save(state)
}

// ParseCandidateAddrs converts reported addresses to []netip.Addr.
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
