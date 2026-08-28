// Package registry 提供设备信息的持久化注册表管理、原子文件读写、共享授权与所有权转移能力。
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
	Devices []device.Device      `json:"devices"`
	Grants  []device.DeviceGrant `json:"grants,omitempty"`
}

// Registry 维护内存中的设备与共享授权列表，并通过线程安全的互斥锁提供持久化 JSON 文件的原子读写操作。
type Registry struct {
	mu              sync.RWMutex
	path            string
	devices         map[string]device.Device
	grants          map[string]map[string]*device.DeviceGrant // deviceID -> userID -> DeviceGrant
	userGrants      map[string]map[string]*device.DeviceGrant // userID -> deviceID -> DeviceGrant
	defaultOwnerID  string
	legacyJoinToken string
}

// Open 从指定的 JSON 文件路径加载设备与授权注册表；若文件不存在则初始化空注册表。
func Open(path string) (*Registry, error) {
	r := &Registry{
		path:       path,
		devices:    map[string]device.Device{},
		grants:     map[string]map[string]*device.DeviceGrant{},
		userGrants: map[string]map[string]*device.DeviceGrant{},
	}
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
	for _, g := range data.Grants {
		gCopy := g
		r.addGrantIndexLocked(&gCopy)
	}
	_ = r.writeLocked()
	return r, nil
}

// SetDefaultOwnerID 设置首选 Owner ID 用于存量设备向后兼容归属绑定
func (r *Registry) SetDefaultOwnerID(ownerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultOwnerID = strings.TrimSpace(ownerID)
	// 若存量设备没有 OwnerUserID，自动绑定
	if r.defaultOwnerID != "" {
		changed := false
		for id, d := range r.devices {
			if d.OwnerUserID == "" {
				d.OwnerUserID = r.defaultOwnerID
				r.devices[id] = d
				changed = true
			}
		}
		if changed {
			_ = r.writeLocked()
		}
	}
}

// Save 校验并持久化保存设备信息。
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
		if d.OwnerUserID == "" {
			d.OwnerUserID = old.OwnerUserID
		}
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
		if d.OwnerUserID == "" && r.defaultOwnerID != "" {
			d.OwnerUserID = r.defaultOwnerID
		}
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

// FilterDevicesForUser 返回对指定用户可见的所有设备列表快照
func (r *Registry) FilterDevicesForUser(userID string, isOwnerRole bool) []device.Device {
	r.mu.RLock()
	defer r.mu.RUnlock()

	list := make([]device.Device, 0, len(r.devices))
	for _, d := range r.devices {
		if isOwnerRole || d.OwnerUserID == userID || r.hasUserGrantLocked(userID, d.ID) {
			list = append(list, d)
		}
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

// Delete 从注册表中删除指定设备，并原子级联清理其所有共享授权记录。
func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.devices[id]
	if !ok {
		return ErrNotFound
	}
	delete(r.devices, id)
	// 级联清理该设备的全部 Grant
	if devGrants, ok := r.grants[id]; ok {
		for uID := range devGrants {
			if ug, exists := r.userGrants[uID]; exists {
				delete(ug, id)
			}
		}
		delete(r.grants, id)
	}

	if err := r.writeLocked(); err != nil {
		r.devices[id] = old
		return err
	}
	return nil
}

// DeleteDevicesByOwner 级联物理删除指定用户所拥有的全部设备及授权（支持用户删除时物理级联清理）
func (r *Registry) DeleteDevicesByOwner(ownerUserID string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var deletedIDs []string
	for id, d := range r.devices {
		if d.OwnerUserID == ownerUserID {
			deletedIDs = append(deletedIDs, id)
			delete(r.devices, id)
			if devGrants, ok := r.grants[id]; ok {
				for uID := range devGrants {
					if ug, exists := r.userGrants[uID]; exists {
						delete(ug, id)
					}
				}
				delete(r.grants, id)
			}
		}
	}

	if len(deletedIDs) > 0 {
		if err := r.writeLocked(); err != nil {
			return nil, err
		}
	}
	return deletedIDs, nil
}

// SetGrant 创建或更新单台设备的共享授权，禁止向设备所有者创建冗余授权
func (r *Registry) SetGrant(deviceID, userID string, level device.GrantLevel, grantedBy string) (*device.DeviceGrant, error) {
	if !device.IsValidGrantLevel(level) {
		return nil, device.ErrInvalidGrantLevel
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	d, ok := r.devices[deviceID]
	if !ok {
		return nil, ErrNotFound
	}
	if d.OwnerUserID == userID {
		return nil, device.ErrGrantToOwner
	}

	now := time.Now().UTC()
	var g *device.DeviceGrant
	if devGrants, exists := r.grants[deviceID]; exists {
		if existing, found := devGrants[userID]; found {
			existing.Level = level
			existing.GrantedBy = grantedBy
			existing.UpdatedAt = now
			existing.Revision++
			g = existing
		}
	}

	if g == nil {
		g = &device.DeviceGrant{
			ID:        device.GenerateGrantID(),
			DeviceID:  deviceID,
			UserID:    userID,
			Level:     level,
			GrantedBy: grantedBy,
			CreatedAt: now,
			UpdatedAt: now,
			Revision:  1,
		}
		r.addGrantIndexLocked(g)
	}

	if err := r.writeLocked(); err != nil {
		return nil, err
	}

	gCopy := *g
	return &gCopy, nil
}

// RevokeGrant 撤销指定用户的设备共享授权
func (r *Registry) RevokeGrant(deviceID, userID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.devices[deviceID]; !ok {
		return ErrNotFound
	}

	if devGrants, ok := r.grants[deviceID]; ok {
		delete(devGrants, userID)
	}
	if uGrants, ok := r.userGrants[userID]; ok {
		delete(uGrants, deviceID)
	}

	return r.writeLocked()
}

// ListGrants 列出指定设备的所有共享授权
func (r *Registry) ListGrants(deviceID string) []*device.DeviceGrant {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var list []*device.DeviceGrant
	if devGrants, ok := r.grants[deviceID]; ok {
		for _, g := range devGrants {
			gCopy := *g
			list = append(list, &gCopy)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

// GetUserGrants 列出指定用户获得的所有设备授权
func (r *Registry) GetUserGrants(userID string) []*device.DeviceGrant {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var list []*device.DeviceGrant
	if uGrants, ok := r.userGrants[userID]; ok {
		for _, g := range uGrants {
			gCopy := *g
			list = append(list, &gCopy)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].DeviceID < list[j].DeviceID })
	return list
}

// TransferOwnership 原子转移设备所有权，清理新所有者冗余 Grant，并可选择为旧所有者保留共享权限
func (r *Registry) TransferOwnership(deviceID, newOwnerID, currentActorID string, retainLevel *device.GrantLevel) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	d, ok := r.devices[deviceID]
	if !ok {
		return ErrNotFound
	}

	oldOwnerID := d.OwnerUserID
	d.OwnerUserID = newOwnerID
	d.UpdatedAt = time.Now().UTC()
	r.devices[deviceID] = d

	// 清除新 Owner 的原有 Grant（因为 Owner 自动获得全量权限）
	if devGrants, ok := r.grants[deviceID]; ok {
		delete(devGrants, newOwnerID)
	}
	if uGrants, ok := r.userGrants[newOwnerID]; ok {
		delete(uGrants, deviceID)
	}

	// 若显式指定为旧 Owner 保留权限
	if retainLevel != nil && device.IsValidGrantLevel(*retainLevel) && oldOwnerID != "" && oldOwnerID != newOwnerID {
		now := time.Now().UTC()
		g := &device.DeviceGrant{
			ID:        device.GenerateGrantID(),
			DeviceID:  deviceID,
			UserID:    oldOwnerID,
			Level:     *retainLevel,
			GrantedBy: currentActorID,
			CreatedAt: now,
			UpdatedAt: now,
			Revision:  1,
		}
		r.addGrantIndexLocked(g)
	} else if oldOwnerID != "" {
		// 默认切断旧 Owner 的所有访问权限
		if devGrants, ok := r.grants[deviceID]; ok {
			delete(devGrants, oldOwnerID)
		}
		if uGrants, ok := r.userGrants[oldOwnerID]; ok {
			delete(uGrants, deviceID)
		}
	}

	return r.writeLocked()
}

// addGrantIndexLocked 将 Grant 记录加入内存双向索引
func (r *Registry) addGrantIndexLocked(g *device.DeviceGrant) {
	if _, ok := r.grants[g.DeviceID]; !ok {
		r.grants[g.DeviceID] = make(map[string]*device.DeviceGrant)
	}
	r.grants[g.DeviceID][g.UserID] = g

	if _, ok := r.userGrants[g.UserID]; !ok {
		r.userGrants[g.UserID] = make(map[string]*device.DeviceGrant)
	}
	r.userGrants[g.UserID][g.DeviceID] = g
}

func (r *Registry) hasUserGrantLocked(userID, deviceID string) bool {
	if devGrants, ok := r.grants[deviceID]; ok {
		_, found := devGrants[userID]
		return found
	}
	return false
}

// ---------- 实现 auth.DeviceScopeResolver 接口 ----------

// IsDeviceVisible 判定设备是否存在且对该用户是否可见（属于其所有或获得共享授权）
func (r *Registry) IsDeviceVisible(userID string, deviceID string) (visible bool, exists bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	d, ok := r.devices[deviceID]
	if !ok {
		return false, false
	}
	if d.OwnerUserID == userID || r.hasUserGrantLocked(userID, deviceID) {
		return true, true
	}
	return false, true
}

// IsDeviceOwner 判定指定用户是否为该设备的所有者
func (r *Registry) IsDeviceOwner(userID string, deviceID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	d, ok := r.devices[deviceID]
	if !ok {
		return false
	}
	return d.OwnerUserID == userID
}

// HasDevicePermission 判定指定用户对该设备是否具备特定操作权限（基于设备授权级别）
func (r *Registry) HasDevicePermission(userID string, deviceID string, perm auth.Permission) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	d, ok := r.devices[deviceID]
	if !ok {
		return false
	}
	if d.OwnerUserID == userID {
		return true
	}

	devGrants, ok := r.grants[deviceID]
	if !ok {
		return false
	}
	g, ok := devGrants[userID]
	if !ok {
		return false
	}

	switch perm {
	case auth.PermDevicesRead, auth.PermCommandsRead, auth.PermHealthRead, auth.PermAlertsRead:
		return true
	case auth.PermDevicesSync, auth.PermDevicesWake, auth.PermDevicesShutdown, auth.PermDevicesUpgrade, auth.PermCommandsCancel:
		return g.Level == device.GrantLevelOperate || g.Level == device.GrantLevelManage
	case auth.PermDevicesUpdate, auth.PermAlertsManage:
		return g.Level == device.GrantLevelManage
	default:
		return false
	}
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

	devicesList := make([]device.Device, 0, len(r.devices))
	for _, d := range r.devices {
		devicesList = append(devicesList, d)
	}
	sort.Slice(devicesList, func(i, j int) bool { return devicesList[i].ID < devicesList[j].ID })

	grantsList := make([]device.DeviceGrant, 0)
	for _, devGrants := range r.grants {
		for _, g := range devGrants {
			grantsList = append(grantsList, *g)
		}
	}
	sort.Slice(grantsList, func(i, j int) bool { return grantsList[i].ID < grantsList[j].ID })

	data := fileData{
		Devices: devicesList,
		Grants:  grantsList,
	}

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
