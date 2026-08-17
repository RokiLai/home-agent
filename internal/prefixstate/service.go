package prefixstate

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"
)

var (
	ErrRevisionConflict        = errors.New("prefix revision conflict: received older revision")
	ErrRevisionContentMismatch = errors.New("prefix revision conflict: identical revision with different payload")
	ErrNetworkNotFound         = errors.New("network prefix state not found")
)

type Store interface {
	GetByNetwork(networkID string) (*RouterPrefixState, error)
	Save(state RouterPrefixState) error
	List() ([]RouterPrefixState, error)
}

type MemoryStore struct {
	mu     sync.RWMutex
	states map[string]RouterPrefixState // keyed by networkID
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		states: make(map[string]RouterPrefixState),
	}
}

func (s *MemoryStore) GetByNetwork(networkID string) (*RouterPrefixState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.states[networkID]
	if !ok {
		return nil, ErrNetworkNotFound
	}
	cpy := st
	return &cpy, nil
}

func (s *MemoryStore) Save(state RouterPrefixState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[state.NetworkID] = state
	return nil
}

func (s *MemoryStore) List() ([]RouterPrefixState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]RouterPrefixState, 0, len(s.states))
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

// UpdateRouterPrefixes updates prefix snapshot reported by a router agent.
func (s *Service) UpdateRouterPrefixes(
	routerDeviceID, networkID string,
	revision uint64,
	observedAt time.Time,
	rawPrefixes []ReportedIPv6Prefix,
) (state *RouterPrefixState, changed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	normalized := NormalizePrefixes(rawPrefixes, observedAt)

	existing, err := s.store.GetByNetwork(networkID)
	if err != nil && !errors.Is(err, ErrNetworkNotFound) {
		return nil, false, err
	}

	if existing == nil {
		newState := RouterPrefixState{
			RouterDeviceID: routerDeviceID,
			NetworkID:      networkID,
			Revision:       revision,
			ObservedAt:     observedAt,
			LastSeenAt:     now,
			Prefixes:       normalized,
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
		if PrefixesEqual(existing.Prefixes, normalized) {
			existing.LastSeenAt = now
			_ = s.store.Save(*existing)
			return existing, false, nil
		}
		return nil, false, ErrRevisionContentMismatch
	}

	// revision > existing.Revision
	changed = !PrefixesEqual(existing.Prefixes, normalized)
	existing.Revision = revision
	existing.RouterDeviceID = routerDeviceID
	existing.ObservedAt = observedAt
	existing.LastSeenAt = now
	existing.Prefixes = normalized

	if err := s.store.Save(*existing); err != nil {
		return nil, false, err
	}
	return existing, changed, nil
}

// GetActivePrefixes retrieves active (preferred and unexpired) prefixes for the specified network.
// If the router state has not been seen within staleTTL, isStale is returned as true.
func (s *Service) GetActivePrefixes(networkID string, now time.Time, staleTTL time.Duration) ([]netip.Prefix, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := s.store.GetByNetwork(networkID)
	if err != nil {
		return nil, false, err
	}

	isStale := false
	if staleTTL > 0 && now.Sub(st.LastSeenAt) > staleTTL {
		isStale = true
	}

	var active []netip.Prefix
	for _, p := range st.Prefixes {
		if !IsValidPrefix(p, now) {
			continue
		}
		pref, err := netip.ParsePrefix(strings.TrimSpace(p.Prefix))
		if err == nil && pref.IsValid() {
			active = append(active, pref.Masked())
		}
	}

	return active, isStale, nil
}

func (s *Service) GetByNetwork(networkID string) (*RouterPrefixState, error) {
	return s.store.GetByNetwork(networkID)
}
