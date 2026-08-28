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
	// ErrRevisionConflict 当接收到的前缀上报版本号小于已保存版本时返回此错误。
	ErrRevisionConflict = errors.New("prefix revision conflict: received older revision")
	// ErrRevisionContentMismatch 当版本号相同但上报的前缀内容不一致时返回此错误。
	ErrRevisionContentMismatch = errors.New("prefix revision conflict: identical revision with different payload")
	// ErrNetworkNotFound 当指定的局域网网络 ID 无对应前缀记录时返回此错误。
	ErrNetworkNotFound = errors.New("network prefix state not found")
)

// RevisionConflictKind 表示版本冲突的错误类型。
type RevisionConflictKind string

const (
	// RevisionConflictOlder 表示上报的前缀版本号小于当前已保存版本。
	RevisionConflictOlder RevisionConflictKind = "revision_conflict"
	// RevisionConflictMismatch 表示版本号相同但上报的前缀内容不一致。
	RevisionConflictMismatch RevisionConflictKind = "revision_content_mismatch"
)

// RevisionConflictError 记录带原子状态快照的前缀版本冲突错误。
type RevisionConflictError struct {
	Kind     RevisionConflictKind
	Current  uint64
	Received uint64
}

func (e *RevisionConflictError) Error() string {
	if e.Kind == RevisionConflictMismatch {
		return fmt.Sprintf("prefix revision conflict: identical revision %d with different payload (current %d)", e.Received, e.Current)
	}
	return fmt.Sprintf("prefix revision conflict: received %d, current is %d", e.Received, e.Current)
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

// Store 定义 RouterPrefixState 数据的持久化存储接口。
type Store interface {
	GetByNetwork(networkID string) (*RouterPrefixState, error)
	Save(state RouterPrefixState) error
	List() ([]RouterPrefixState, error)
}

// MemoryStore 是 Store 接口的线程安全内存存储实现。
type MemoryStore struct {
	mu     sync.RWMutex
	states map[string]RouterPrefixState // 以 networkID 为键
}

// NewMemoryStore 创建内存路由器前缀存储实例。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		states: make(map[string]RouterPrefixState),
	}
}

// GetByNetwork 根据局域网网络 ID 获取其前缀状态。
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

// Save 保存或更新指定网络的路由器前缀状态。
func (s *MemoryStore) Save(state RouterPrefixState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[state.NetworkID] = state
	return nil
}

// List 返回所有已存储的路由器前缀状态列表。
func (s *MemoryStore) List() ([]RouterPrefixState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]RouterPrefixState, 0, len(s.states))
	for _, st := range s.states {
		list = append(list, st)
	}
	return list, nil
}

// Service 协调路由器前缀快照的处理、更新及有效前缀提取。
type Service struct {
	store Store
	mu    sync.Mutex
}

// NewService 创建基于指定存储实现的 prefixstate Service。
func NewService(store Store) *Service {
	if store == nil {
		store = NewMemoryStore()
	}
	return &Service{store: store}
}

// UpdateRouterPrefixes 处理路由器 Agent 上报的前缀快照，校验版本修订号并持久化更新。
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

	// 版本号防回退校验
	if revision < existing.Revision {
		return nil, false, &RevisionConflictError{
			Kind:     RevisionConflictOlder,
			Current:  existing.Revision,
			Received: revision,
		}
	}

	if revision == existing.Revision {
		if PrefixesEqual(existing.Prefixes, normalized) {
			existing.LastSeenAt = now
			_ = s.store.Save(*existing)
			return existing, false, nil
		}
		return nil, false, &RevisionConflictError{
			Kind:     RevisionConflictMismatch,
			Current:  existing.Revision,
			Received: revision,
		}
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

// GetActivePrefixes 获取指定网络当前未过期且首选的活动前缀列表；若路由器心跳超过 staleTTL，则 isStale 返回 true。
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

// GetByNetwork 根据局域网网络 ID 获取其前缀状态快照。
func (s *Service) GetByNetwork(networkID string) (*RouterPrefixState, error) {
	return s.store.GetByNetwork(networkID)
}
