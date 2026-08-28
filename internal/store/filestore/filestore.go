// Package filestore 实现基于本地 JSON 文件的默认 FileStore 存储驱动
package filestore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"homeagent/internal/auth"
	"homeagent/internal/device"
	"homeagent/internal/store"
)

// FileStore 实现 store.UserStore, store.SessionStore, store.DeviceStore, store.AuditStore
type FileStore struct {
	mu          sync.RWMutex
	authPath    string
	devicesPath string

	// 内存缓存
	users    map[string]*auth.User    // key: user_id
	userKeys map[string]string        // key: username_key -> user_id
	sessions map[string]*auth.Session // key: token_hash

	devices     map[string]*device.Device                 // key: device_id
	grants      map[string]map[string]*device.DeviceGrant // key: device_id -> user_id -> grant
	claimTokens map[string]*auth.ClaimToken               // tokenHash -> ClaimToken
	auditEvents []auth.AuditEvent
	auditCap    int
}

// NewFileStore 创建并初始化本地 FileStore
func NewFileStore(authPath, devicesPath string) (*FileStore, error) {
	fs := &FileStore{
		authPath:    authPath,
		devicesPath: devicesPath,
		users:       make(map[string]*auth.User),
		userKeys:    make(map[string]string),
		sessions:    make(map[string]*auth.Session),
		devices:     make(map[string]*device.Device),
		grants:      make(map[string]map[string]*device.DeviceGrant),
		claimTokens: make(map[string]*auth.ClaimToken),
		auditCap:    500,
	}

	if err := fs.loadAuth(); err != nil {
		return nil, fmt.Errorf("load auth file: %w", err)
	}
	if err := fs.loadDevices(); err != nil {
		return nil, fmt.Errorf("load devices file: %w", err)
	}

	return fs, nil
}

// UserStore 实现

func (fs *FileStore) GetUser(id string) (*auth.User, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	u, ok := fs.users[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *u
	return &cp, nil
}

func (fs *FileStore) GetUserByUsernameKey(key string) (*auth.User, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	id, ok := fs.userKeys[key]
	if !ok {
		return nil, store.ErrNotFound
	}
	u, ok := fs.users[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *u
	return &cp, nil
}

func (fs *FileStore) ListUsers() ([]*auth.User, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	list := make([]*auth.User, 0, len(fs.users))
	for _, u := range fs.users {
		cp := *u
		list = append(list, &cp)
	}
	return list, nil
}

func (fs *FileStore) SaveUser(u *auth.User) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if u.ID == "" {
		u.ID = auth.GenerateUserID()
	}
	if u.UsernameKey == "" {
		u.UsernameKey = auth.NormalizeUsernameKey(u.Username)
	}
	if existingID, exists := fs.userKeys[u.UsernameKey]; exists && existingID != u.ID {
		return store.ErrConflict
	}

	cp := *u
	fs.users[u.ID] = &cp
	fs.userKeys[u.UsernameKey] = u.ID
	return fs.saveAuthLocked()
}

func (fs *FileStore) DeleteUser(id string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	u, ok := fs.users[id]
	if !ok {
		return store.ErrNotFound
	}

	delete(fs.users, id)
	delete(fs.userKeys, u.UsernameKey)

	for hash, s := range fs.sessions {
		if s.UserID == id {
			delete(fs.sessions, hash)
		}
	}
	return fs.saveAuthLocked()
}

func (fs *FileStore) CountActiveOwners() (int, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	count := 0
	for _, u := range fs.users {
		if u.Status == auth.UserStatusActive && u.Role == auth.RoleOwner {
			count++
		}
	}
	return count, nil
}

// SessionStore 实现

func (fs *FileStore) GetSession(tokenHash string) (*auth.Session, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	s, ok := fs.sessions[tokenHash]
	if !ok {
		return nil, store.ErrNotFound
	}
	if time.Now().UTC().After(s.ExpiresAt) {
		return nil, store.ErrNotFound
	}
	cp := *s
	return &cp, nil
}

func (fs *FileStore) SaveSession(s *auth.Session) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	cp := *s
	fs.sessions[s.TokenHash] = &cp
	return fs.saveAuthLocked()
}

func (fs *FileStore) DeleteSession(tokenHash string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	delete(fs.sessions, tokenHash)
	return fs.saveAuthLocked()
}

func (fs *FileStore) DeleteSessionsByUser(userID string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	for hash, s := range fs.sessions {
		if s.UserID == userID {
			delete(fs.sessions, hash)
		}
	}
	return fs.saveAuthLocked()
}

func (fs *FileStore) CleanExpired() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	now := time.Now().UTC()
	for hash, s := range fs.sessions {
		if now.After(s.ExpiresAt) {
			delete(fs.sessions, hash)
		}
	}
	return fs.saveAuthLocked()
}

// DeviceStore 实现

func (fs *FileStore) GetDevice(id string) (*device.Device, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	d, ok := fs.devices[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *d
	return &cp, nil
}

func (fs *FileStore) ListDevices() ([]*device.Device, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	list := make([]*device.Device, 0, len(fs.devices))
	for _, d := range fs.devices {
		cp := *d
		list = append(list, &cp)
	}
	return list, nil
}

func (fs *FileStore) SaveDevice(dev *device.Device) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	cp := *dev
	fs.devices[dev.ID] = &cp
	return fs.saveDevicesLocked()
}

func (fs *FileStore) DeleteDevice(id string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	delete(fs.devices, id)
	delete(fs.grants, id)
	return fs.saveDevicesLocked()
}

func (fs *FileStore) DeleteDevicesByOwner(ownerUserID string) ([]string, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	var deleted []string
	for id, dev := range fs.devices {
		if dev.OwnerUserID == ownerUserID {
			delete(fs.devices, id)
			delete(fs.grants, id)
			deleted = append(deleted, id)
		}
	}
	if len(deleted) > 0 {
		_ = fs.saveDevicesLocked()
	}
	return deleted, nil
}

func (fs *FileStore) ListGrants(deviceID string) ([]*device.DeviceGrant, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	devGrants, ok := fs.grants[deviceID]
	if !ok {
		return []*device.DeviceGrant{}, nil
	}
	list := make([]*device.DeviceGrant, 0, len(devGrants))
	for _, g := range devGrants {
		cp := *g
		list = append(list, &cp)
	}
	return list, nil
}

func (fs *FileStore) GetGrant(deviceID, userID string) (*device.DeviceGrant, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	devGrants, ok := fs.grants[deviceID]
	if !ok {
		return nil, store.ErrNotFound
	}
	g, ok := devGrants[userID]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *g
	return &cp, nil
}

func (fs *FileStore) SaveGrant(grant *device.DeviceGrant) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if _, ok := fs.grants[grant.DeviceID]; !ok {
		fs.grants[grant.DeviceID] = make(map[string]*device.DeviceGrant)
	}
	cp := *grant
	fs.grants[grant.DeviceID][grant.UserID] = &cp
	return fs.saveDevicesLocked()
}

func (fs *FileStore) DeleteGrant(deviceID, userID string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if devGrants, ok := fs.grants[deviceID]; ok {
		delete(devGrants, userID)
	}
	return fs.saveDevicesLocked()
}

func (fs *FileStore) DeleteGrantsByDevice(deviceID string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	delete(fs.grants, deviceID)
	return fs.saveDevicesLocked()
}

func (fs *FileStore) DeleteGrantsByUser(userID string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	for _, devGrants := range fs.grants {
		delete(devGrants, userID)
	}
	return fs.saveDevicesLocked()
}

// ================= EnrollmentStore 实现 =================

func (fs *FileStore) GetClaimToken(tokenHash string) (*auth.ClaimToken, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	tok, ok := fs.claimTokens[tokenHash]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *tok
	return &cp, nil
}

func (fs *FileStore) ListClaimTokens(ownerUserID string) ([]*auth.ClaimToken, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	var list []*auth.ClaimToken
	for _, tok := range fs.claimTokens {
		if ownerUserID == "" || tok.OwnerUserID == ownerUserID {
			cp := *tok
			list = append(list, &cp)
		}
	}
	return list, nil
}

func (fs *FileStore) SaveClaimToken(token *auth.ClaimToken) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	cp := *token
	fs.claimTokens[token.TokenHash] = &cp
	return nil
}

func (fs *FileStore) DeleteClaimToken(tokenHash string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	delete(fs.claimTokens, tokenHash)
	return nil
}

// AuditStore 实现

func (fs *FileStore) Record(event auth.AuditEvent) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if fs.auditCap <= 0 {
		fs.auditCap = 500
	}
	if len(fs.auditEvents) >= fs.auditCap {
		fs.auditEvents = fs.auditEvents[1:]
	}
	fs.auditEvents = append(fs.auditEvents, event)
	return nil
}

func (fs *FileStore) Recent(limit int) ([]auth.AuditEvent, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	if limit <= 0 || limit > len(fs.auditEvents) {
		limit = len(fs.auditEvents)
	}
	res := make([]auth.AuditEvent, limit)
	total := len(fs.auditEvents)
	for i := 0; i < limit; i++ {
		res[i] = fs.auditEvents[total-1-i]
	}
	return res, nil
}

// 内部落盘与加载辅助函数

func (fs *FileStore) loadAuth() error {
	if fs.authPath == "" {
		return nil
	}
	data, err := os.ReadFile(fs.authPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var payload struct {
		SchemaVersion int                      `json:"schema_version"`
		Users         map[string]*auth.User    `json:"users"`
		Sessions      map[string]*auth.Session `json:"sessions"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	if payload.Users != nil {
		fs.users = payload.Users
		for _, u := range fs.users {
			key := u.UsernameKey
			if key == "" {
				key = auth.NormalizeUsernameKey(u.Username)
			}
			fs.userKeys[key] = u.ID
		}
	}
	if payload.Sessions != nil {
		fs.sessions = payload.Sessions
	}
	return nil
}

func (fs *FileStore) saveAuthLocked() error {
	if fs.authPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(fs.authPath), 0700); err != nil {
		return err
	}
	payload := map[string]any{
		"schema_version": 2,
		"users":          fs.users,
		"sessions":       fs.sessions,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	tmp := fs.authPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, fs.authPath)
}

func (fs *FileStore) loadDevices() error {
	if fs.devicesPath == "" {
		return nil
	}
	data, err := os.ReadFile(fs.devicesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	// 1. 优先解析标准包含 {"devices": [...], "grants": [...]} 的包装结构
	var payload struct {
		Devices []*device.Device      `json:"devices"`
		Grants  []*device.DeviceGrant `json:"grants"`
	}
	if err := json.Unmarshal(data, &payload); err == nil && (payload.Devices != nil || payload.Grants != nil) {
		for _, d := range payload.Devices {
			fs.devices[d.ID] = d
		}
		for _, g := range payload.Grants {
			if _, ok := fs.grants[g.DeviceID]; !ok {
				fs.grants[g.DeviceID] = make(map[string]*device.DeviceGrant)
			}
			fs.grants[g.DeviceID][g.UserID] = g
		}
		return nil
	}

	// 2. 兼容数组格式
	var devList []*device.Device
	if err := json.Unmarshal(data, &devList); err != nil {
		return err
	}
	for _, d := range devList {
		fs.devices[d.ID] = d
	}
	return nil
}

func (fs *FileStore) saveDevicesLocked() error {
	if fs.devicesPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(fs.devicesPath), 0700); err != nil {
		return err
	}
	list := make([]*device.Device, 0, len(fs.devices))
	for _, d := range fs.devices {
		list = append(list, d)
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := fs.devicesPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, fs.devicesPath)
}
