package servernetwork

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RecordStatus 表示受管 DNS 记录的调和状态枚举。
type RecordStatus string

const (
	// StatusDisabled 未启用服务端 IPv6 自更新。
	StatusDisabled RecordStatus = "disabled"
	// StatusBlocked 配置非法或受管记录存在所有权冲突。
	StatusBlocked RecordStatus = "blocked"
	// StatusWaitingAddress 网卡采集失败或暂无有效候选公网 IPv6 地址。
	StatusWaitingAddress RecordStatus = "waiting_address"
	// StatusPending 探测到新候选地址或重启待核验，已保存期望版本，准备发起发布。
	StatusPending RecordStatus = "pending"
	// StatusSyncing 正在向 DNS 服务商发起 AAAA 记录更新请求。
	StatusSyncing RecordStatus = "syncing"
	// StatusVerifying 写入成功，正在向服务商查询权威记录查证收敛。
	StatusVerifying RecordStatus = "verifying"
	// StatusSynced 服务商记录查询与当前期望地址完全一致。
	StatusSynced RecordStatus = "synced"
	// StatusFailed 同步或查证失败，保留最后已确认值并进入退避重试。
	StatusFailed RecordStatus = "failed"
)

// RecordState 保存单个受管域名的完整领域状态与版本追踪元数据。
type RecordState struct {
	Record           string       `json:"record"`
	ConfigVersion    int64        `json:"config_version"`
	DesiredAddress   string       `json:"desired_address"`
	DesiredVersion   int64        `json:"desired_version"`
	ConfirmedAddress string       `json:"confirmed_address"`
	ConfirmedVersion int64        `json:"confirmed_version"`
	Status           RecordStatus `json:"status"`
	LastSuccessTime  *time.Time   `json:"last_success_time,omitempty"`
	LastError        string       `json:"last_error,omitempty"`
	NextRetryTime    *time.Time   `json:"next_retry_time,omitempty"`
	RetryCount       int          `json:"retry_count"`
	UpdatedAt        time.Time    `json:"updated_at"`
}

// PersistedState 表示持久化到磁盘的数据结构。
type PersistedState struct {
	Records map[string]*RecordState `json:"records"`
}

// PersistedConfig 保存服务端 IPv6 自举网络配置。
type PersistedConfig struct {
	Enabled   bool     `json:"enabled"`
	Interface string   `json:"interface"`
	Records   []string `json:"records"`
}

// ConfigStore 定义自举配置持久化接口。
type ConfigStore interface {
	LoadConfig() (*PersistedConfig, error)
	SaveConfig(cfg *PersistedConfig) error
}

// StateStore 定义状态持久化接口。
type StateStore interface {
	Load() (*PersistedState, error)
	Save(state *PersistedState) error
	ConfigStore
}

// FileStateStore 基于本地文件系统的原子持久化存储实现。
type FileStateStore struct {
	statePath  string
	configPath string
	mu         sync.RWMutex
}

// NewFileStateStore 创建基于文件目录的状态持久化存储器。
func NewFileStateStore(dataDir string) *FileStateStore {
	return &FileStateStore{
		statePath:  filepath.Join(dataDir, "server_network_state.json"),
		configPath: filepath.Join(dataDir, "server_network_config.json"),
	}
}

// Load 从磁盘读取持久化状态，若文件不存在或损坏则返回可用的空状态。
func (s *FileStateStore) Load() (*PersistedState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(s.statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &PersistedState{Records: make(map[string]*RecordState)}, nil
		}
		return nil, fmt.Errorf("read state file: %w", err)
	}

	var state PersistedState
	if err := json.Unmarshal(data, &state); err != nil {
		// 文件损坏时回退到空状态，防止系统阻塞
		return &PersistedState{Records: make(map[string]*RecordState)}, nil
	}
	if state.Records == nil {
		state.Records = make(map[string]*RecordState)
	}
	return &state, nil
}

// Save 将状态原子写入磁盘（先写临时文件，fsync 后重命名替换）。
func (s *FileStateStore) Save(state *PersistedState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if state == nil {
		state = &PersistedState{Records: make(map[string]*RecordState)}
	}
	if state.Records == nil {
		state.Records = make(map[string]*RecordState)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	dir := filepath.Dir(s.statePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, "server_network_state.*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("sync temp state file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp state file: %w", err)
	}

	if err := os.Rename(tmpPath, s.statePath); err != nil {
		return fmt.Errorf("atomic rename state file: %w", err)
	}

	return nil
}

// LoadConfig 从磁盘读取持久化配置，若文件不存在返回 nil, nil。
func (s *FileStateStore) LoadConfig() (*PersistedConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(s.configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var cfg PersistedConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &cfg, nil
}

// SaveConfig 原子保存网络自举配置。
func (s *FileStateStore) SaveConfig(cfg *PersistedConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cfg == nil {
		cfg = &PersistedConfig{}
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	dir := filepath.Dir(s.configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, "server_network_config.*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write temp config file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("sync temp config file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp config file: %w", err)
	}

	if err := os.Rename(tmpPath, s.configPath); err != nil {
		return fmt.Errorf("atomic rename config file: %w", err)
	}

	return nil
}
