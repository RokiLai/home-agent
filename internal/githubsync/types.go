// Package githubsync 提供 GitHub OAuth Device Authorization Flow 认证集成、GitHub 账户公钥注册管理及 Agent 凭据分发服务。
package githubsync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// DefaultClientID 是用于 GitHub Device Authorization Flow 授权的默认 GitHub CLI Client ID。
const DefaultClientID = "178c6fc778ccc68e1d6a"

// DefaultScope 是向 GitHub 请求授权的默认 OAuth 权限范围。
const DefaultScope = "repo,read:user,admin:public_key"

// GitHubUser 表示 GitHub 用户个人资料信息。
type GitHubUser struct {
	Login     string `json:"login"`
	ID        int64  `json:"id"`
	Name      string `json:"name,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

// GitHubAuth 包含 OAuth 访问令牌与授权范围。
type GitHubAuth struct {
	TokenType   string `json:"token_type"`
	AccessToken string `json:"access_token"`
	Scope       string `json:"scope"`
}

// GitHubCredentials 封装服务端持久化存储的 GitHub OAuth 凭据、用户信息及版本号。
type GitHubCredentials struct {
	Version   int64      `json:"version"`
	User      GitHubUser `json:"user"`
	Auth      GitHubAuth `json:"auth"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// DeviceKeyRecord 跟踪已自动注册到 GitHub 账户的单台设备 SSH 公钥元数据。
type DeviceKeyRecord struct {
	DeviceID    string    `json:"device_id"`
	GitHubKeyID int64     `json:"github_key_id"`
	Fingerprint string    `json:"fingerprint"`
	KeyTitle    string    `json:"key_title"`
	CreatedAt   time.Time `json:"created_at"`
	SyncStatus  string    `json:"sync_status"`
}

// DeviceKeysStore 结构化存储已在 GitHub 注册的全部设备公钥集合。
type DeviceKeysStore struct {
	Devices map[string]DeviceKeyRecord `json:"devices"`
}

// DeviceCodeResponse 表示向 GitHub 申请 Device Code 成功后返回的核验代码与跳转 URL。
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// TokenResponse 表示 GitHub 令牌轮询接口返回的授权响应。
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Error       string `json:"error,omitempty"`
	ErrorDesc   string `json:"error_description,omitempty"`
	ErrorURI    string `json:"error_uri,omitempty"`
}

// GHConfig 表示通过 SSE 注入到 Agent 端的 GitHub CLI 配置（~/.config/gh/hosts.yml）。
type GHConfig struct {
	Host        string `json:"host"`
	User        string `json:"user"`
	OAuthToken  string `json:"oauth_token"`
	GitProtocol string `json:"git_protocol"`
}

// SSHSyncConfig 表示在 github_credentials_sync 中下发的 SSH 密钥操作指示。
type SSHSyncConfig struct {
	EnsureKey   bool   `json:"ensure_key"`
	KeyFilename string `json:"key_filename"`
}

// SyncPayload 表示通过 SSE 推送给 Agent 的 GitHub 凭据与 SSH 配置注入载荷。
type SyncPayload struct {
	Version  int64         `json:"version"`
	Hash     string        `json:"hash"`
	GHConfig GHConfig      `json:"gh_config"`
	SSH      SSHSyncConfig `json:"ssh"`
}

// RevokePayload 表示通过 SSE 下发要求 Agent 清理 GitHub 凭据与 SSH 块的通知。
type RevokePayload struct {
	Timestamp int64  `json:"timestamp"`
	Reason    string `json:"reason"` // "sync_disabled" 或 "account_disconnected"
}

// RegisterSSHKeyRequest 表示 Agent 请求在 GitHub 上注册其公钥的数据包。
type RegisterSSHKeyRequest struct {
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
}

// StatusResponse 表示服务端当前 GitHub 连通状态、授权用户及多设备同步汇总。
type StatusResponse struct {
	Connected      bool       `json:"connected"`
	User           GitHubUser `json:"user,omitempty"`
	RedactedToken  string     `json:"redacted_token,omitempty"`
	SyncedDevices  int        `json:"synced_devices"`
	TotalEnabled   int        `json:"total_enabled"`
	Version        int64      `json:"version,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at,omitempty"`
	InDeviceFlow   bool       `json:"in_device_flow,omitempty"`
	DeviceUserCode string     `json:"device_user_code,omitempty"`
	DeviceURL      string     `json:"device_url,omitempty"`
}

// ComputeSyncHash 计算 SyncPayload 关键属性的 SHA256 十六进制哈希，用于 Agent 端的幂等校验。
func ComputeSyncHash(version int64, user string, token string, gitProtocol string) string {
	raw := fmt.Sprintf("v=%d|u=%s|t=%s|p=%s", version, user, token, gitProtocol)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// RedactToken 对敏感 OAuth Token 执行脱敏遮蔽（保留前 4 位和后 4 位，中间以星号替代）。
func RedactToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if len(token) <= 8 {
		return "********"
	}
	prefix := ""
	if strings.HasPrefix(token, "gho_") || strings.HasPrefix(token, "ghp_") || strings.HasPrefix(token, "github_pat_") {
		parts := strings.SplitN(token, "_", 2)
		prefix = parts[0] + "_"
		token = parts[1]
	}
	if len(token) <= 4 {
		return prefix + "****"
	}
	return prefix + "****" + token[len(token)-4:]
}

