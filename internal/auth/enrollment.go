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
	// ErrClaimTokenNotFound 表示 Claim Token 不存在或无效
	ErrClaimTokenNotFound = errors.New("claim token not found")
	// ErrClaimTokenExpired 表示 Claim Token 已过有效期
	ErrClaimTokenExpired = errors.New("claim token expired")
	// ErrClaimTokenExhausted 表示 Claim Token 使用次数已耗尽
	ErrClaimTokenExhausted = errors.New("claim token max uses exhausted")
)

const (
	// DefaultClaimTTL 默认短期认领凭据有效期（15分钟）
	DefaultClaimTTL = 15 * time.Minute
)

// ClaimToken 保存短期认领凭据的元数据与哈希
type ClaimToken struct {
	ID            string    `json:"id"`
	TokenHash     string    `json:"token_hash"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	MaxUses       int       `json:"max_uses"`
	RemainingUses int       `json:"remaining_uses"`
	Description   string    `json:"description"`
}

type enrollmentStoreData struct {
	Tokens map[string]*ClaimToken `json:"tokens"`
}

// EnrollmentManager 线程安全地管理短期设备认领凭据（Claim Token）的生成、原子核销与持久化
type EnrollmentManager struct {
	mu        sync.Mutex
	storePath string
	tokens    map[string]*ClaimToken // key 为 token_hash
}

// NewEnrollmentManager 初始化认领凭据管理器
func NewEnrollmentManager(storePath string) (*EnrollmentManager, error) {
	em := &EnrollmentManager{
		storePath: storePath,
		tokens:    make(map[string]*ClaimToken),
	}
	if storePath == "" {
		return em, nil
	}

	b, err := os.ReadFile(storePath)
	if errors.Is(err, os.ErrNotExist) {
		return em, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read enrollment store: %w", err)
	}

	var data enrollmentStoreData
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, fmt.Errorf("decode enrollment store: %w", err)
	}

	if data.Tokens != nil {
		em.tokens = data.Tokens
	}

	em.cleanExpiredLocked()
	_ = em.saveLocked()
	return em, nil
}

// CreateClaimToken 生成新的 Claim Token，返回一次性明文 Token 与元数据
func (em *EnrollmentManager) CreateClaimToken(ttl time.Duration, maxUses int, description string) (string, *ClaimToken, error) {
	if ttl <= 0 {
		ttl = DefaultClaimTTL
	}
	if maxUses <= 0 {
		maxUses = 1
	}

	rawToken, err := GenerateSecureToken("claim_", 32)
	if err != nil {
		return "", nil, err
	}
	tokenHash := HashToken(rawToken)

	// ID 取前缀 + 截断标识符用于管理端展示
	id := rawToken
	if len(rawToken) > 18 {
		id = rawToken[:18]
	}

	now := time.Now().UTC()
	token := &ClaimToken{
		ID:            id,
		TokenHash:     tokenHash,
		CreatedAt:     now,
		ExpiresAt:     now.Add(ttl),
		MaxUses:       maxUses,
		RemainingUses: maxUses,
		Description:   strings.TrimSpace(description),
	}

	em.mu.Lock()
	defer em.mu.Unlock()

	em.cleanExpiredLocked()
	em.tokens[tokenHash] = token
	if err := em.saveLocked(); err != nil {
		return "", nil, err
	}

	return rawToken, token, nil
}

// ConsumeClaimToken 原子化校验并扣减 Claim Token（单实例锁保证并发双花安全）
func (em *EnrollmentManager) ConsumeClaimToken(rawToken string) (*ClaimToken, error) {
	if strings.TrimSpace(rawToken) == "" {
		return nil, ErrClaimTokenNotFound
	}
	tokenHash := HashToken(rawToken)

	em.mu.Lock()
	defer em.mu.Unlock()

	token, ok := em.tokens[tokenHash]
	if !ok {
		return nil, ErrClaimTokenNotFound
	}

	now := time.Now().UTC()
	if now.After(token.ExpiresAt) {
		delete(em.tokens, tokenHash)
		_ = em.saveLocked()
		return nil, ErrClaimTokenExpired
	}

	if token.RemainingUses <= 0 {
		delete(em.tokens, tokenHash)
		_ = em.saveLocked()
		return nil, ErrClaimTokenExhausted
	}

	token.RemainingUses--
	tokenCopy := *token

	if token.RemainingUses <= 0 {
		delete(em.tokens, tokenHash)
	}

	_ = em.saveLocked()
	return &tokenCopy, nil
}

// ListActiveTokens 返回当前所有未过期且剩余可用次数大于 0 的认领凭据列表
func (em *EnrollmentManager) ListActiveTokens() []*ClaimToken {
	em.mu.Lock()
	defer em.mu.Unlock()

	em.cleanExpiredLocked()
	var list []*ClaimToken
	for _, t := range em.tokens {
		tCopy := *t
		// 清除内部哈希，仅暴露 ID 与元数据
		tCopy.TokenHash = ""
		list = append(list, &tCopy)
	}
	return list
}

// RevokeToken 根据 ID 手动作废认领凭据
func (em *EnrollmentManager) RevokeToken(id string) error {
	if id == "" {
		return nil
	}

	em.mu.Lock()
	defer em.mu.Unlock()

	for hash, t := range em.tokens {
		if t.ID == id || strings.HasPrefix(t.ID, id) {
			delete(em.tokens, hash)
		}
	}
	return em.saveLocked()
}

func (em *EnrollmentManager) cleanExpiredLocked() {
	now := time.Now().UTC()
	for hash, t := range em.tokens {
		if now.After(t.ExpiresAt) || t.RemainingUses <= 0 {
			delete(em.tokens, hash)
		}
	}
}

func (em *EnrollmentManager) saveLocked() error {
	if em.storePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(em.storePath), 0700); err != nil {
		return err
	}

	data := enrollmentStoreData{
		Tokens: em.tokens,
	}

	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := em.storePath + ".tmp"
	if err := os.WriteFile(tmpPath, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, em.storePath)
}
