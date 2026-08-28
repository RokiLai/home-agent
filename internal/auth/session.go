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
	// ErrSessionExpired 表示会话已过期
	ErrSessionExpired = errors.New("session expired")
	// ErrAdminAlreadyExists 表示管理员用户已存在，禁止初始化覆盖
	ErrAdminAlreadyExists = errors.New("admin user already exists")
)

const (
	// SessionDurationRememberMe 勾选“记住我”时的会话有效期（7天）
	SessionDurationRememberMe = 7 * 24 * time.Hour
	// SessionDurationDefault 默认会话有效期（24小时）
	SessionDurationDefault = 24 * time.Hour
)

// AdminUser 保存管理员账号信息与密码哈希
type AdminUser struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Session 保存管理员会话的哈希与状态
type Session struct {
	TokenHash  string    `json:"token_hash"`
	Username   string    `json:"username"`
	Role       string    `json:"role"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	RememberMe bool      `json:"remember_me"`
}

type authStoreData struct {
	Admin    *AdminUser          `json:"admin,omitempty"`
	Sessions map[string]*Session `json:"sessions,omitempty"`
}

// SessionManager 线程安全地管理管理员账户密码、会话生命周期与持久化
type SessionManager struct {
	mu        sync.RWMutex
	storePath string
	admin     *AdminUser
	sessions  map[string]*Session // key 为 token_hash
}

// NewSessionManager 初始化会话管理器；若提供 storePath 则自动加载已有状态
func NewSessionManager(storePath string) (*SessionManager, error) {
	sm := &SessionManager{
		storePath: storePath,
		sessions:  make(map[string]*Session),
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

	var data authStoreData
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, fmt.Errorf("decode auth store: %w", err)
	}

	sm.admin = data.Admin
	if data.Sessions != nil {
		sm.sessions = data.Sessions
	}

	sm.cleanExpiredLocked()
	_ = sm.saveLocked()
	return sm, nil
}

// HasAdmin 判断是否已配置管理员用户
func (sm *SessionManager) HasAdmin() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.admin != nil && sm.admin.PasswordHash != ""
}

// GetAdminUsername 获取当前配置的管理员用户名
func (sm *SessionManager) GetAdminUsername() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.admin == nil {
		return ""
	}
	return sm.admin.Username
}

// InitAdminBootstrap 仅在系统首次初始化且不存在管理员时设置管理员账户（遵循 Bootstrap 规范）
func (sm *SessionManager) InitAdminBootstrap(username, password string) (bool, error) {
	if username == "" || password == "" {
		return false, errors.New("username and password cannot be empty")
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 若已存在管理员，绝对不自动覆盖已有密码
	if sm.admin != nil && sm.admin.PasswordHash != "" {
		return false, nil
	}

	hash, err := HashPassword(password)
	if err != nil {
		return false, err
	}

	now := time.Now().UTC()
	sm.admin = &AdminUser{
		Username:     username,
		PasswordHash: hash,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := sm.saveLocked(); err != nil {
		return false, err
	}
	return true, nil
}

// AuthenticateAdmin 校验管理员用户名和密码
func (sm *SessionManager) AuthenticateAdmin(username, password string) error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.admin == nil || sm.admin.PasswordHash == "" {
		return errors.New("no admin configured")
	}
	if sm.admin.Username != username {
		return ErrUnauthorized
	}
	if !CheckPassword(sm.admin.PasswordHash, password) {
		return ErrUnauthorized
	}
	return nil
}

// UpdateAdminPassword 验证旧密码后原子更新管理员密码
func (sm *SessionManager) UpdateAdminPassword(username, oldPassword, newPassword string) error {
	if strings.TrimSpace(newPassword) == "" {
		return errors.New("new password cannot be empty")
	}
	if len(newPassword) < 6 {
		return errors.New("new password must be at least 6 characters")
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.admin == nil || sm.admin.PasswordHash == "" {
		return errors.New("no admin configured")
	}
	if sm.admin.Username != username {
		return ErrUnauthorized
	}
	if !CheckPassword(sm.admin.PasswordHash, oldPassword) {
		return errors.New("current password is incorrect")
	}

	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}

	sm.admin.PasswordHash = hash
	sm.admin.UpdatedAt = time.Now().UTC()

	return sm.saveLocked()
}

// CreateSession 创建并保存一个新的管理员会话，返回明文 Session Token
func (sm *SessionManager) CreateSession(username, role string, rememberMe bool) (string, *Session, error) {
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
		TokenHash:  tokenHash,
		Username:   username,
		Role:       role,
		CreatedAt:  now,
		ExpiresAt:  now.Add(duration),
		RememberMe: rememberMe,
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.cleanExpiredLocked()
	sm.sessions[tokenHash] = session
	if err := sm.saveLocked(); err != nil {
		return "", nil, err
	}

	return rawToken, session, nil
}

// ValidateSession 校验明文 Token 对应的会话是否合法且未过期
func (sm *SessionManager) ValidateSession(rawToken string) (*Session, error) {
	if rawToken == "" {
		return nil, ErrUnauthorized
	}
	tokenHash := HashToken(rawToken)

	sm.mu.Lock()
	defer sm.mu.Unlock()

	sess, ok := sm.sessions[tokenHash]
	if !ok {
		return nil, ErrUnauthorized
	}

	if time.Now().UTC().After(sess.ExpiresAt) {
		delete(sm.sessions, tokenHash)
		_ = sm.saveLocked()
		return nil, ErrSessionExpired
	}

	return sess, nil
}

// RevokeSession 立即作废指定的 Session Token（登出时调用）
func (sm *SessionManager) RevokeSession(rawToken string) error {
	if rawToken == "" {
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

	data := authStoreData{
		Admin:    sm.admin,
		Sessions: sm.sessions,
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
