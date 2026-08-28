package auth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultMaxFailedAttempts 触发锁定的最大连续失败次数
	DefaultMaxFailedAttempts = 5
	// DefaultLockoutDuration 锁定持续时间（15分钟）
	DefaultLockoutDuration = 15 * time.Minute
)

type ipRecord struct {
	failedAttempts int
	lockedUntil    time.Time
	lastAttempt    time.Time
}

// RateLimiter 基于客户端 IP 的防爆破滑动锁定器
type RateLimiter struct {
	mu              sync.Mutex
	maxFailures     int
	lockDuration    time.Duration
	records         map[string]*ipRecord
	cleanupInterval time.Duration
	lastCleanup     time.Time
}

// NewRateLimiter 初始化限流器
func NewRateLimiter(maxFailures int, lockDuration time.Duration) *RateLimiter {
	if maxFailures <= 0 {
		maxFailures = DefaultMaxFailedAttempts
	}
	if lockDuration <= 0 {
		lockDuration = DefaultLockoutDuration
	}
	return &RateLimiter{
		maxFailures:     maxFailures,
		lockDuration:    lockDuration,
		records:         make(map[string]*ipRecord),
		cleanupInterval: 5 * time.Minute,
		lastCleanup:     time.Now().UTC(),
	}
}

// RecordFailure 记录一次认证失败，若达到阈值则触发锁定并返回锁定截止时间
func (rl *RateLimiter) RecordFailure(ip string) (bool, time.Duration) {
	if ip == "" {
		return false, 0
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now().UTC()
	rl.maybeCleanupLocked(now)

	rec, ok := rl.records[ip]
	if !ok {
		rec = &ipRecord{}
		rl.records[ip] = rec
	}

	rec.lastAttempt = now
	rec.failedAttempts++
	if rec.failedAttempts >= rl.maxFailures {
		rec.lockedUntil = now.Add(rl.lockDuration)
		return true, rl.lockDuration
	}

	return false, 0
}

// RecordSuccess 记录一次认证成功，重置失败计数
func (rl *RateLimiter) RecordSuccess(ip string) {
	if ip == "" {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	delete(rl.records, ip)
}

// IsLocked 检查指定 IP 是否正处于锁定状态
func (rl *RateLimiter) IsLocked(ip string) (bool, time.Duration) {
	if ip == "" {
		return false, 0
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rec, ok := rl.records[ip]
	if !ok {
		return false, 0
	}

	now := time.Now().UTC()
	if now.Before(rec.lockedUntil) {
		return true, rec.lockedUntil.Sub(now)
	}

	// 锁定已过，重置计数
	if !rec.lockedUntil.IsZero() {
		delete(rl.records, ip)
	}

	return false, 0
}

func (rl *RateLimiter) maybeCleanupLocked(now time.Time) {
	if now.Sub(rl.lastCleanup) < rl.cleanupInterval {
		return
	}
	rl.lastCleanup = now
	for ip, rec := range rl.records {
		if now.After(rec.lockedUntil) && now.Sub(rec.lastAttempt) > rl.lockDuration {
			delete(rl.records, ip)
		}
	}
}

// ExtractClientIP 从 HTTP 请求中提取真实客户端 IP 地址
func ExtractClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			ip := strings.TrimSpace(parts[0])
			if ip != "" {
				return ip
			}
		}
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}
