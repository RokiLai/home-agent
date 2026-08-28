package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	// ErrUnauthorized 表示鉴权失败或凭据无效
	ErrUnauthorized = errors.New("unauthorized")
	// ErrSessionExpired 表示会话已过期或已被吊销
	ErrSessionExpired = errors.New("session expired")
	// ErrAdminAlreadyExists 表示已存在用户，禁止 Bootstrap 初始化覆盖
	ErrAdminAlreadyExists = errors.New("admin user already exists")
)

const (
	// SessionDurationRememberMe 勾选“记住我”时的会话有效期（7天）
	SessionDurationRememberMe = 7 * 24 * time.Hour
	// SessionDurationDefault 默认会话有效期（24小时）
	SessionDurationDefault = 24 * time.Hour
)

// AdminUser 为向后兼容旧测试及旧数据反序列化保留的结构
type AdminUser struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Session 保存用户会话的哈希、所属用户与版本状态
type Session struct {
	TokenHash        string    `json:"token_hash"`
	UserID           string    `json:"user_id"`
	Username         string    `json:"username,omitempty"`
	Role             string    `json:"role,omitempty"`
	IssuedSessionVer uint64    `json:"issued_session_ver"`
	CreatedAt        time.Time `json:"created_at"`
	LastSeenAt       time.Time `json:"last_seen_at,omitempty"`
	ExpiresAt        time.Time `json:"expires_at"`
	RememberMe       bool      `json:"remember_me"`
}

// SessionManager 线程安全地管理多用户账户、会话生命周期、SessionVersion 失效与原子持久化
type SessionManager struct {
	mu            sync.RWMutex
	storePath     string
	schemaVersion int
	users         map[string]*User    // key: user_id
	userKeys      map[string]string   // key: username_key -> user_id
	sessions      map[string]*Session // key: token_hash
}

// NewSessionManager 初始化会话管理器；若提供 storePath 则自动加载已有状态并支持向后兼容平滑迁移
func NewSessionManager(storePath string) (*SessionManager, error) {
	sm := &SessionManager{
		storePath:     storePath,
		schemaVersion: CurrentSchemaVersion,
		users:         make(map[string]*User),
		userKeys:      make(map[string]string),
		sessions:      make(map[string]*Session),
	}
	if storePath == "" {
		return sm, nil
	}

	b, err := os.ReadFile(storePath)
	if errors.Is(err, os.ErrNotExist) {
		return sm, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read auth store: %w", err)
	}

	migratedData, migrated, err := MigrateAuthStoreData(b, storePath)
	if err != nil {
		return nil, fmt.Errorf("migrate auth store: %w", err)
	}

	sm.schemaVersion = migratedData.SchemaVersion
	sm.users = migratedData.Users
	sm.sessions = migratedData.Sessions

	// 建立 username_key -> user_id 索引
	for _, u := range sm.users {
		if u.UsernameKey == "" {
			u.UsernameKey = NormalizeUsernameKey(u.Username)
		}
		sm.userKeys[u.UsernameKey] = u.ID
	}

	sm.cleanExpiredLocked()
	if migrated {
		_ = sm.saveLocked()
	}
	return sm, nil
}

// HasAdmin 判断是否已配置至少一个活跃的管理员/Owner 账号
func (sm *SessionManager) HasAdmin() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	for _, u := range sm.users {
		if u.Status == UserStatusActive && (u.Role == RoleOwner || u.Role == RoleAdmin) {
			return true
		}
	}
	return false
}

// GetAdminUsername 获取当前首个配置的活跃管理员/Owner 用户名
func (sm *SessionManager) GetAdminUsername() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	for _, u := range sm.users {
		if u.Status == UserStatusActive && u.Role == RoleOwner {
			return u.Username
		}
	}
	for _, u := range sm.users {
		if u.Status == UserStatusActive && u.Role == RoleAdmin {
			return u.Username
		}
	}
	return ""
}

// InitAdminBootstrap 仅在系统首次初始化且不存在任何用户时设置首个 Owner 账号
func (sm *SessionManager) InitAdminBootstrap(username, password string) (bool, error) {
	if strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" {
		return false, errors.New("username and password cannot be empty")
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 若已存在任何用户，绝对不自动覆盖已有密码
	if len(sm.users) > 0 {
		return false, nil
	}

	if err := ValidateUsernameFormat(username); err != nil {
		return false, err
	}
	if len(password) < 6 {
		return false, ErrPasswordTooShort
	}

	hash, err := HashPassword(password)
	if err != nil {
		return false, err
	}

	now := time.Now().UTC()
	userID := GenerateUserID()
	key := NormalizeUsernameKey(username)

	owner := &User{
		ID:             userID,
		Username:       strings.TrimSpace(username),
		UsernameKey:    key,
		PasswordHash:   hash,
		Role:           RoleOwner,
		Status:         UserStatusActive,
		SessionVersion: 1,
		CreatedBy:      "bootstrap",
		CreatedAt:      now,
		UpdatedAt:      now,
		Revision:       1,
	}

	sm.users[userID] = owner
	sm.userKeys[key] = userID

	if err := sm.saveLocked(); err != nil {
		return false, err
	}
	return true, nil
}

// UpsertUser 将外部用户记录原子同步/导入至 SessionManager 内存与索引中
func (sm *SessionManager) UpsertUser(u *User) error {
	if u == nil || u.ID == "" {
		return errors.New("invalid user")
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()

	key := u.UsernameKey
	if key == "" {
		key = NormalizeUsernameKey(u.Username)
	}
	cp := *u
	sm.users[u.ID] = &cp
	sm.userKeys[key] = u.ID
	return nil
}

// AuthenticateUser 校验用户名与密码，成功则返回对应 User 实体
func (sm *SessionManager) AuthenticateUser(username, password string) (*User, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	key := NormalizeUsernameKey(username)
	userID, ok := sm.userKeys[key]
	if !ok {
		return nil, ErrUnauthorized
	}

	u, ok := sm.users[userID]
	if !ok || u.Status != UserStatusActive || u.PasswordHash == "" {
		return nil, ErrUnauthorized
	}

	if !CheckPassword(u.PasswordHash, password) {
		return nil, ErrUnauthorized
	}

	userCopy := *u
	return &userCopy, nil
}

// AuthenticateAdmin 兼容旧版管理员密码校验
func (sm *SessionManager) AuthenticateAdmin(username, password string) error {
	_, err := sm.AuthenticateUser(username, password)
	return err
}

// CreateUserSession 为指定用户创建新 Session，返回明文 Session Token 与 Session 元数据
func (sm *SessionManager) CreateUserSession(userID string, rememberMe bool) (string, *Session, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	u, ok := sm.users[userID]
	if !ok || u.Status != UserStatusActive {
		return "", nil, ErrUnauthorized
	}

	rawToken, err := GenerateSecureToken("agt_sess_", 32)
	if err != nil {
		return "", nil, err
	}
	tokenHash := HashToken(rawToken)

	duration := SessionDurationDefault
	if rememberMe {
		duration = SessionDurationRememberMe
	}

	now := time.Now().UTC()
	session := &Session{
		TokenHash:        tokenHash,
		UserID:           u.ID,
		Username:         u.Username,
		Role:             string(u.Role),
		IssuedSessionVer: u.SessionVersion,
		CreatedAt:        now,
		LastSeenAt:       now,
		ExpiresAt:        now.Add(duration),
		RememberMe:       rememberMe,
	}

	sm.cleanExpiredLocked()
	sm.sessions[tokenHash] = session
	if err := sm.saveLocked(); err != nil {
		return "", nil, err
	}

	return rawToken, session, nil
}

// CreateSession 兼容按用户名创建会话
func (sm *SessionManager) CreateSession(username, role string, rememberMe bool) (string, *Session, error) {
	sm.mu.RLock()
	key := NormalizeUsernameKey(username)
	userID, ok := sm.userKeys[key]
	sm.mu.RUnlock()

	if !ok {
		return "", nil, ErrUserNotFound
	}
	return sm.CreateUserSession(userID, rememberMe)
}

// ValidateSession 校验明文 Token 对应的会话合法性，并实时加载用户状态与 SessionVersion
func (sm *SessionManager) ValidateSession(rawToken string) (*Session, error) {
	if strings.TrimSpace(rawToken) == "" {
		return nil, ErrUnauthorized
	}
	tokenHash := HashToken(rawToken)

	sm.mu.Lock()
	defer sm.mu.Unlock()

	sess, ok := sm.sessions[tokenHash]
	if !ok {
		return nil, ErrUnauthorized
	}

	now := time.Now().UTC()
	if now.After(sess.ExpiresAt) {
		delete(sm.sessions, tokenHash)
		_ = sm.saveLocked()
		return nil, ErrSessionExpired
	}

	u, ok := sm.users[sess.UserID]
	if !ok || u.Status != UserStatusActive || sess.IssuedSessionVer != u.SessionVersion {
		delete(sm.sessions, tokenHash)
		_ = sm.saveLocked()
		return nil, ErrSessionExpired
	}

	sess.LastSeenAt = now
	sess.Username = u.Username
	sess.Role = string(u.Role)

	sessCopy := *sess
	return &sessCopy, nil
}

// RevokeSession 立即作废指定的 Session Token
func (sm *SessionManager) RevokeSession(rawToken string) error {
	if strings.TrimSpace(rawToken) == "" {
		return nil
	}
	tokenHash := HashToken(rawToken)

	sm.mu.Lock()
	defer sm.mu.Unlock()

	delete(sm.sessions, tokenHash)
	return sm.saveLocked()
}

// RevokeAllSessions 作废所有活跃会话
func (sm *SessionManager) RevokeAllSessions() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.sessions = make(map[string]*Session)
	return sm.saveLocked()
}

// RevokeUserSessions 立即递增用户 SessionVersion 并作废该用户全部会话
func (sm *SessionManager) RevokeUserSessions(userID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	u, ok := sm.users[userID]
	if !ok {
		return ErrUserNotFound
	}

	u.SessionVersion++
	u.UpdatedAt = time.Now().UTC()

	for hash, sess := range sm.sessions {
		if sess.UserID == userID {
			delete(sm.sessions, hash)
		}
	}

	return sm.saveLocked()
}

// UpdateAdminPassword 验证旧密码后更新管理员密码（兼容方法）
func (sm *SessionManager) UpdateAdminPassword(username, oldPassword, newPassword string) error {
	sm.mu.RLock()
	key := NormalizeUsernameKey(username)
	userID, ok := sm.userKeys[key]
	sm.mu.RUnlock()

	if !ok {
		return ErrUnauthorized
	}
	return sm.UpdateUserPassword(userID, oldPassword, newPassword)
}

// UpdateUserPassword 验证旧密码后更新指定用户的密码，并使该用户其它旧 Session 失效
func (sm *SessionManager) UpdateUserPassword(userID, oldPassword, newPassword string) error {
	if strings.TrimSpace(newPassword) == "" {
		return errors.New("new password cannot be empty")
	}
	if len(newPassword) < 6 {
		return ErrPasswordTooShort
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	u, ok := sm.users[userID]
	if !ok {
		return ErrUserNotFound
	}
	if !CheckPassword(u.PasswordHash, oldPassword) {
		return errors.New("current password is incorrect")
	}

	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	u.PasswordHash = hash
	u.SessionVersion++
	u.UpdatedAt = now
	u.Revision++

	// 清理该用户除当前操作外的所有旧 Session（由 SessionVersion 保证）
	for hash, sess := range sm.sessions {
		if sess.UserID == userID {
			delete(sm.sessions, hash)
		}
	}

	return sm.saveLocked()
}

// ResetUserPassword 管理员重置他人密码，并递增 SessionVersion 吊销其全部会话
func (sm *SessionManager) ResetUserPassword(userID, newPassword string) error {
	if len(newPassword) < 6 {
		return ErrPasswordTooShort
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	u, ok := sm.users[userID]
	if !ok {
		return ErrUserNotFound
	}

	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	u.PasswordHash = hash
	u.SessionVersion++
	u.UpdatedAt = now
	u.Revision++

	for hash, sess := range sm.sessions {
		if sess.UserID == userID {
			delete(sm.sessions, hash)
		}
	}

	return sm.saveLocked()
}

// CreateUser 创建新用户
func (sm *SessionManager) CreateUser(username, password string, role Role, createdBy string) (*User, error) {
	if err := ValidateUsernameFormat(username); err != nil {
		return nil, err
	}
	if len(password) < 6 {
		return nil, ErrPasswordTooShort
	}
	if !IsValidRole(role) {
		return nil, ErrInvalidRole
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	key := NormalizeUsernameKey(username)
	if _, exists := sm.userKeys[key]; exists {
		return nil, ErrUsernameConflict
	}

	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	userID := GenerateUserID()
	user := &User{
		ID:             userID,
		Username:       strings.TrimSpace(username),
		UsernameKey:    key,
		PasswordHash:   hash,
		Role:           role,
		Status:         UserStatusActive,
		SessionVersion: 1,
		CreatedBy:      createdBy,
		CreatedAt:      now,
		UpdatedAt:      now,
		Revision:       1,
	}

	sm.users[userID] = user
	sm.userKeys[key] = userID

	if err := sm.saveLocked(); err != nil {
		delete(sm.users, userID)
		delete(sm.userKeys, key)
		return nil, err
	}

	userCopy := *user
	return &userCopy, nil
}

// UpdateUserRole 修改用户角色（若降级最后一个活跃 owner 则安全拒绝）
func (sm *SessionManager) UpdateUserRole(userID string, newRole Role) error {
	if !IsValidRole(newRole) {
		return ErrInvalidRole
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	u, ok := sm.users[userID]
	if !ok {
		return ErrUserNotFound
	}

	if u.Role == RoleOwner && newRole != RoleOwner {
		if sm.countActiveOwnersLocked() <= 1 {
			return ErrLastOwnerRequired
		}
	}

	now := time.Now().UTC()
	u.Role = newRole
	u.SessionVersion++
	u.UpdatedAt = now
	u.Revision++

	for hash, sess := range sm.sessions {
		if sess.UserID == userID {
			delete(sm.sessions, hash)
		}
	}

	return sm.saveLocked()
}

// DisableUser 禁用用户账号（若禁用最后一个活跃 owner 则安全拒绝）
func (sm *SessionManager) DisableUser(userID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	u, ok := sm.users[userID]
	if !ok {
		return ErrUserNotFound
	}
	if u.Status == UserStatusDisabled {
		return nil
	}

	if u.Role == RoleOwner {
		if sm.countActiveOwnersLocked() <= 1 {
			return ErrLastOwnerRequired
		}
	}

	now := time.Now().UTC()
	u.Status = UserStatusDisabled
	u.DisabledAt = &now
	u.SessionVersion++
	u.UpdatedAt = now
	u.Revision++

	for hash, sess := range sm.sessions {
		if sess.UserID == userID {
			delete(sm.sessions, hash)
		}
	}

	return sm.saveLocked()
}

// EnableUser 启用已禁用的用户账号
func (sm *SessionManager) EnableUser(userID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	u, ok := sm.users[userID]
	if !ok {
		return ErrUserNotFound
	}
	if u.Status == UserStatusActive {
		return nil
	}

	now := time.Now().UTC()
	u.Status = UserStatusActive
	u.DisabledAt = nil
	u.UpdatedAt = now
	u.Revision++

	return sm.saveLocked()
}

// DeleteUser 删除用户（若删除最后一个活跃 owner 则安全拒绝）
func (sm *SessionManager) DeleteUser(userID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	u, ok := sm.users[userID]
	if !ok {
		return ErrUserNotFound
	}

	if u.Role == RoleOwner && u.Status == UserStatusActive {
		if sm.countActiveOwnersLocked() <= 1 {
			return ErrLastOwnerRequired
		}
	}

	delete(sm.users, userID)
	delete(sm.userKeys, u.UsernameKey)

	for hash, sess := range sm.sessions {
		if sess.UserID == userID {
			delete(sm.sessions, hash)
		}
	}

	return sm.saveLocked()
}

// GetUser 获取指定用户详情副本
func (sm *SessionManager) GetUser(userID string) (*User, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	u, ok := sm.users[userID]
	if !ok {
		return nil, ErrUserNotFound
	}
	userCopy := *u
	return &userCopy, nil
}

// GetUserByUsername 根据用户名查找用户
func (sm *SessionManager) GetUserByUsername(username string) (*User, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	key := NormalizeUsernameKey(username)
	userID, ok := sm.userKeys[key]
	if !ok {
		return nil, ErrUserNotFound
	}

	u, ok := sm.users[userID]
	if !ok {
		return nil, ErrUserNotFound
	}
	userCopy := *u
	return &userCopy, nil
}

// ListUsers 获取所有用户列表
func (sm *SessionManager) ListUsers() []*User {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	list := make([]*User, 0, len(sm.users))
	for _, u := range sm.users {
		uCopy := *u
		// 移除敏感密码哈希后返回摘要
		uCopy.PasswordHash = ""
		list = append(list, &uCopy)
	}
	return list
}

// CountActiveOwners 返回当前活跃状态的 Owner 数量
func (sm *SessionManager) CountActiveOwners() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.countActiveOwnersLocked()
}

func (sm *SessionManager) countActiveOwnersLocked() int {
	count := 0
	for _, u := range sm.users {
		if u.Status == UserStatusActive && u.Role == RoleOwner {
			count++
		}
	}
	return count
}

func (sm *SessionManager) cleanExpiredLocked() {
	now := time.Now().UTC()
	for hash, sess := range sm.sessions {
		if now.After(sess.ExpiresAt) {
			delete(sm.sessions, hash)
		}
	}
}

func (sm *SessionManager) saveLocked() error {
	if sm.storePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(sm.storePath), 0700); err != nil {
		return err
	}

	data := authStoreDataV2{
		SchemaVersion: sm.schemaVersion,
		Users:         sm.users,
		Sessions:      sm.sessions,
	}

	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := sm.storePath + ".tmp"
	if err := os.WriteFile(tmpPath, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, sm.storePath)
}
