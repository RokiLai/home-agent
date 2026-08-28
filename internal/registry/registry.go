// Package registry 提供设备信息的持久化注册表管理、原子文件读写与元数据更新能力。
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"homeagent/internal/auth"
	"homeagent/internal/device"
	"homeagent/internal/wol"
)

// ErrNotFound 当在注册表中查询不存在的设备 ID 时返回此错误。
var ErrNotFound = errors.New("device not found")

type fileData struct {
	Devices []device.Device `json:"devices"`
}

// Registry 维护内存中的设备列表，并通过线程安全的互斥锁提供持久化 JSON 文件的原子读写操作。
type Registry struct {
	mu              sync.RWMutex
	path            string
	devices         map[string]device.Device
	legacyJoinToken string
}

// Open 从指定的 JSON 文件路径加载设备注册表；若文件不存在则初始化空注册表。
func Open(path string) (*Registry, error) {
	r := &Registry{path: path, devices: map[string]device.Device{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var data fileData
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, fmt.Errorf("decode registry: %w", err)
	}
	for _, d := range data.Devices {
		d.Addresses = device.FilterAndSortAddresses(d.Addresses)
		r.devices[d.ID] = d
	}
	_ = r.writeLocked()
	return r, nil
}

// Save 校验并持久化保存设备信息。
// 若设备已存在，则自动保留其 Alias、MAC、GitHub 状态等用户配置字段，并更新 LastSeenAt 时间。
func (r *Registry) Save(d device.Device) (device.Device, error) {

	if err := device.Validate(d); err != nil {
		return device.Device{}, err
	}
	if strings.TrimSpace(d.MAC) != "" {
		_, norm, err := wol.ParseAndValidateMAC(d.MAC)
		if err != nil {
			return device.Device{}, err
		}
		d.MAC = norm
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	if old, ok := r.devices[d.ID]; ok {
		d.CreatedAt = old.CreatedAt
		if d.Alias == "" {
			d.Alias = old.Alias
		}
		if d.MAC == "" {
			d.MAC = old.MAC
		}
		if d.AgentVersion == "" {
			d.AgentVersion = old.AgentVersion
		}
		if d.SyncStatus == "" {
			d.SyncStatus = old.SyncStatus
		}
		if d.AppliedVersion == 0 {
			d.AppliedVersion = old.AppliedVersion
		}
		if d.AppliedHash == "" {
			d.AppliedHash = old.AppliedHash
		}
		if d.SyncError == "" {
			d.SyncError = old.SyncError
		}
		if d.SyncUpdatedAt.IsZero() {
			d.SyncUpdatedAt = old.SyncUpdatedAt
		}
		if !d.GitHubSyncEnabled && old.GitHubSyncEnabled {
			d.GitHubSyncEnabled = old.GitHubSyncEnabled
		}
		if d.GitHubStatus == "" {
			d.GitHubStatus = old.GitHubStatus
		}
		if d.GitHubKeyID == 0 {
			d.GitHubKeyID = old.GitHubKeyID
		}
		if d.GitHubFingerprint == "" {
			d.GitHubFingerprint = old.GitHubFingerprint
		}
		if d.GitHubUpdatedAt.IsZero() {
			d.GitHubUpdatedAt = old.GitHubUpdatedAt
		}
		if d.DeviceTokenHash == "" {
			d.DeviceTokenHash = old.DeviceTokenHash
		}
		if d.ControlProtocols == nil {
			d.ControlProtocols = old.ControlProtocols
		}
	} else {
		d.CreatedAt = now
	}
	d.Addresses = device.FilterAndSortAddresses(d.Addresses)
	d.UpdatedAt, d.LastSeenAt = now, now
	old, existed := r.devices[d.ID]
	r.devices[d.ID] = d
	if err := r.writeLocked(); err != nil {
		if existed {
			r.devices[d.ID] = old
		} else {
			delete(r.devices, d.ID)
		}
		return device.Device{}, err
	}
	return d, nil
}

// List 返回所有已注册设备的切片快照。
func (r *Registry) List() []device.Device {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]device.Device, 0, len(r.devices))
	for _, d := range r.devices {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

// Get 根据设备 ID 获取设备详情；若不存在则返回 ErrNotFound。
func (r *Registry) Get(id string) (device.Device, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.devices[id]
	if !ok {
		return device.Device{}, ErrNotFound
	}
	return d, nil
}

// Delete 从注册表中删除指定设备并同步写入磁盘。
func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.devices[id]
	if !ok {
		return ErrNotFound
	}
	delete(r.devices, id)
	if err := r.writeLocked(); err != nil {
		r.devices[id] = old
		return err
	}
	return nil
}

// UpdateAlias 更新指定设备的自定义别名。
func (r *Registry) UpdateAlias(id string, alias string) (device.Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok {
		return device.Device{}, ErrNotFound
	}
	now := time.Now().UTC()
	d.Alias = strings.TrimSpace(alias)
	d.UpdatedAt = now
	old := r.devices[id]
	r.devices[id] = d
	if err := r.writeLocked(); err != nil {
		r.devices[id] = old
		return device.Device{}, err
	}
	return d, nil
}

// UpdateMAC 校验并更新指定设备的物理 MAC 地址。
func (r *Registry) UpdateMAC(id string, mac string) (device.Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok {
		return device.Device{}, ErrNotFound
	}
	clean := strings.TrimSpace(mac)
	if clean != "" {
		_, norm, err := wol.ParseAndValidateMAC(clean)
		if err != nil {
			return device.Device{}, err
		}
		d.MAC = norm
	} else {
		d.MAC = ""
	}
	now := time.Now().UTC()
	d.UpdatedAt = now
	old := r.devices[id]
	r.devices[id] = d
	if err := r.writeLocked(); err != nil {
		r.devices[id] = old
		return device.Device{}, err
	}
	return d, nil
}

// UpdateDevice 原子更新设备的别名、MAC 地址与 GitHub 同步开关。
func (r *Registry) UpdateDevice(id string, alias *string, mac *string, gitHubSyncEnabled *bool) (device.Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok {
		return device.Device{}, ErrNotFound
	}
	now := time.Now().UTC()
	if alias != nil {
		d.Alias = strings.TrimSpace(*alias)
	}
	if mac != nil {
		clean := strings.TrimSpace(*mac)
		if clean != "" {
			_, norm, err := wol.ParseAndValidateMAC(clean)
			if err != nil {
				return device.Device{}, err
			}
			d.MAC = norm
		} else {
			d.MAC = ""
		}
	}
	if gitHubSyncEnabled != nil {
		d.GitHubSyncEnabled = *gitHubSyncEnabled
		d.GitHubUpdatedAt = now
	}
	d.UpdatedAt = now
	old := r.devices[id]
	r.devices[id] = d
	if err := r.writeLocked(); err != nil {
		r.devices[id] = old
		return device.Device{}, err
	}
	return d, nil
}

// UpdateGitHubSyncEnabled 更新指定设备的 GitHub 同步开启/关闭状态。
func (r *Registry) UpdateGitHubSyncEnabled(id string, enabled bool) (device.Device, error) {
	return r.UpdateDevice(id, nil, nil, &enabled)
}

// UpdateGitHubStatus 更新指定设备的 GitHub SSH 密钥注册状态、公钥 ID 及指纹。
func (r *Registry) UpdateGitHubStatus(id string, status string, keyID int64, fingerprint string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().UTC()
	d.GitHubStatus = status
	if keyID > 0 {
		d.GitHubKeyID = keyID
	}
	if fingerprint != "" {
		d.GitHubFingerprint = fingerprint
	}
	d.GitHubUpdatedAt = now
	d.UpdatedAt = now
	old := r.devices[id]
	r.devices[id] = d
	if err := r.writeLocked(); err != nil {
		r.devices[id] = old
		return err
	}
	return nil
}

// UpdateAgentVersion 记录 Agent 上报的当前运行版本号并刷新 LastSeenAt。
func (r *Registry) UpdateAgentVersion(id string, agentVersion string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok {
		return ErrNotFound
	}
	clean := strings.TrimSpace(agentVersion)
	if clean != "" {
		d.AgentVersion = clean
	}
	now := time.Now().UTC()
	d.UpdatedAt = now
	d.LastSeenAt = now
	old := r.devices[id]
	r.devices[id] = d
	if err := r.writeLocked(); err != nil {
		r.devices[id] = old
		return err
	}
	return nil
}

// TouchLastSeen 更新指定设备的活跃时间 LastSeenAt 与 UpdatedAt。
func (r *Registry) TouchLastSeen(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().UTC()
	d.UpdatedAt = now
	d.LastSeenAt = now
	old := r.devices[id]
	r.devices[id] = d
	if err := r.writeLocked(); err != nil {
		r.devices[id] = old
		return err
	}
	return nil
}

// UpdateSyncStatus 记录 Agent 状态 ACK 回执（同步状态、版本号、哈希、错误信息及最新活跃时间）。
func (r *Registry) UpdateSyncStatus(id string, status string, version int64, hash string, errMsg string) error {

	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().UTC()
	d.SyncStatus = status
	d.AppliedVersion = version
	d.AppliedHash = hash
	d.SyncError = errMsg
	d.SyncUpdatedAt = now
	d.LastSeenAt = now
	old := r.devices[id]
	r.devices[id] = d
	if err := r.writeLocked(); err != nil {
		r.devices[id] = old
		return err
	}
	return nil
}

func (r *Registry) writeLocked() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0700); err != nil {
		return err
	}
	data := fileData{Devices: make([]device.Device, 0, len(r.devices))}
	for _, d := range r.devices {
		data.Devices = append(data.Devices, d)
	}
	sort.Slice(data.Devices, func(i, j int) bool { return data.Devices[i].ID < data.Devices[j].ID })
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(r.path), ".devices-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, r.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(r.path))
	if err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	ok = true
	return nil
}

// SetLegacyJoinToken 设置旧版兼容凭据（仅用于平滑迁移，无管理员权限）
func (r *Registry) SetLegacyJoinToken(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.legacyJoinToken = strings.TrimSpace(token)
}

// AuthorizeDevice 校验设备专用 Token 并防范跨设备越权（IDOR）
func (r *Registry) AuthorizeDevice(rawToken, deviceID string) error {
	if rawToken == "" || deviceID == "" {
		return auth.ErrDeviceNotFound
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	reqDev, ok := r.devices[deviceID]
	if !ok {
		return auth.ErrDeviceNotFound
	}

	tokenHash := auth.HashToken(rawToken)

	// 1. 若目标设备已绑定专用 DeviceTokenHash
	if reqDev.DeviceTokenHash != "" {
		if auth.SecureCompareHash(reqDev.DeviceTokenHash, tokenHash) {
			return nil
		}
		// 检查该 Token 是否属于其他设备（IDOR 检查）
		for otherID, otherDev := range r.devices {
			if otherID != deviceID && otherDev.DeviceTokenHash != "" {
				if auth.SecureCompareHash(otherDev.DeviceTokenHash, tokenHash) {
					return auth.ErrDeviceMismatch
				}
			}
		}
		return auth.ErrUnauthorized
	}

	// 2. 若目标设备未绑定 Token（存量旧设备），且配置了 legacyJoinToken 且比对成功
	if r.legacyJoinToken != "" && auth.SecureCompareHash(auth.HashToken(r.legacyJoinToken), tokenHash) {
		return nil
	}

	return auth.ErrUnauthorized
}
