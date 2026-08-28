package githubsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	// ErrAuthPending 当用户尚未在网页端完成授权验证时返回此错误。
	ErrAuthPending = errors.New("authorization_pending")
	// ErrSlowDown 当轮询令牌接口频率过快时返回此错误。
	ErrSlowDown = errors.New("slow_down")
	// ErrCodeExpired 当用户验证码已过期时返回此错误。
	ErrCodeExpired = errors.New("expired_token")
	// ErrAccessDenied 当用户在 GitHub 页面上拒绝授权时返回此错误。
	ErrAccessDenied = errors.New("access_denied")
)

// Client 封装与 GitHub OAuth 及 REST API 的交互，处理设备授权流、用户资料拉取及公钥增删。
type Client struct {
	HTTPClient *http.Client
	OAuthBase  string // 默认为 "https://github.com"
	APIBase    string // 默认为 "https://api.github.com"
}

// NewClient 构建 GitHub API Client 实例。
func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{
		HTTPClient: httpClient,
		OAuthBase:  "https://github.com",
		APIBase:    "https://api.github.com",
	}
}

// RequestDeviceCode 向 GitHub 申请新的设备核验代码与用户验证 URI。
func (c *Client) RequestDeviceCode(ctx context.Context, clientID, scope string) (*DeviceCodeResponse, error) {

	if clientID == "" {
		clientID = DefaultClientID
	}
	if scope == "" {
		scope = DefaultScope
	}

	endpoint := fmt.Sprintf("%s/login/device/code", strings.TrimRight(c.OAuthBase, "/"))
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scope", scope)

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "HomeAgent-Server/v0.3.0")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request device code: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var res DeviceCodeResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("decode device code response: %w", err)
	}

	if res.Interval <= 0 {
		res.Interval = 5
	}
	return &res, nil
}

// PollAccessToken 使用 device_code 向 GitHub 轮询单次 OAuth 访问令牌。
func (c *Client) PollAccessToken(ctx context.Context, clientID, deviceCode string) (*TokenResponse, error) {
	if clientID == "" {
		clientID = DefaultClientID
	}

	endpoint := fmt.Sprintf("%s/login/oauth/access_token", strings.TrimRight(c.OAuthBase, "/"))
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("device_code", deviceCode)
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "HomeAgent-Server/v0.3.0")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("poll access token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}

	var res TokenResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}

	if res.Error != "" {
		switch res.Error {
		case "authorization_pending":
			return &res, ErrAuthPending
		case "slow_down":
			return &res, ErrSlowDown
		case "expired_token":
			return &res, ErrCodeExpired
		case "access_denied":
			return &res, ErrAccessDenied
		default:
			return &res, fmt.Errorf("oauth error: %s (%s)", res.Error, res.ErrorDesc)
		}
	}

	if res.AccessToken == "" {
		return nil, fmt.Errorf("empty access token in response: %s", string(body))
	}

	return &res, nil
}

// GetUserProfile 查询已认证用户的 GitHub 个人资料（用户名、ID、头像等）。
func (c *Client) GetUserProfile(ctx context.Context, token string) (*GitHubUser, error) {
	endpoint := fmt.Sprintf("%s/user", strings.TrimRight(c.APIBase, "/"))
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create get user request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "HomeAgent-Server/v0.3.0")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get user profile: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read user profile response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api status %d: %s", resp.StatusCode, string(body))
	}

	var user GitHubUser
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, fmt.Errorf("decode user profile: %w", err)
	}

	return &user, nil
}

type keyCreatePayload struct {
	Title string `json:"title"`
	Key   string `json:"key"`
}

type keyCreateResponse struct {
	ID        int64  `json:"id"`
	Key       string `json:"key"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
}

// RegisterPublicKey 在已授权的 GitHub 账户中注册 SSH 公钥，返回 GitHub 分配的公钥 ID。
func (c *Client) RegisterPublicKey(ctx context.Context, token, title, publicKey string) (int64, error) {
	endpoint := fmt.Sprintf("%s/user/keys", strings.TrimRight(c.APIBase, "/"))
	payload := keyCreatePayload{
		Title: title,
		Key:   strings.TrimSpace(publicKey),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal key create payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		return 0, fmt.Errorf("create register key request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "HomeAgent-Server/v0.3.0")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("register public key: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read register key response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("failed to register public key (status %d): %s", resp.StatusCode, string(body))
	}

	var res keyCreateResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return 0, fmt.Errorf("decode register key response: %w", err)
	}

	return res.ID, nil
}

// DeletePublicKey 根据公钥 ID 从 GitHub 账户中删除指定的 SSH 公钥。
func (c *Client) DeletePublicKey(ctx context.Context, token string, keyID int64) error {

	if keyID <= 0 {
		return nil
	}
	endpoint := fmt.Sprintf("%s/user/keys/%d", strings.TrimRight(c.APIBase, "/"), keyID)
	req, err := http.NewRequestWithContext(ctx, "DELETE", endpoint, nil)
	if err != nil {
		return fmt.Errorf("create delete key request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "HomeAgent-Server/v0.3.0")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete public key: %w", err)
	}
	defer resp.Body.Close()

	// 204 No Content is standard success; 404 Not Found means key was already deleted
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("failed to delete public key (status %d): %s", resp.StatusCode, string(body))
}
