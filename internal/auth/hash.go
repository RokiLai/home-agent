// Package auth 提供 HTTP API 端点的身份鉴权、会话管理、Claim 凭据与密码哈希工具。
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

const (
	// DefaultBcryptCost 是密码哈希的标准计算代价
	DefaultBcryptCost = 12
)

// HashToken 计算任意明文 Token 的 SHA-256 十六进制哈希串。
func HashToken(rawToken string) string {
	if rawToken == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}

// SecureCompareHash 使用恒定时间算法比较两个哈希字符串，防止时序攻击。
func SecureCompareHash(hash1, hash2 string) bool {
	if hash1 == "" || hash2 == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hash1), []byte(hash2)) == 1
}

// HashPassword 使用 bcrypt 算法生成强密码哈希。
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), DefaultBcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(bytes), nil
}

// CheckPassword 校验明文密码与 bcrypt 哈希是否匹配。
func CheckPassword(hash, password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// GenerateSecureToken 使用系统密码学安全随机源生成指定前缀的高强度随机 Token。
func GenerateSecureToken(prefix string, byteLen int) (string, error) {
	if byteLen <= 0 {
		byteLen = 32
	}
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return prefix + hex.EncodeToString(buf), nil
}
