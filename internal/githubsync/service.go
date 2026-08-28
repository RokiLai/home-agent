package githubsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	// ErrNotConnected 表示当前尚未通过 OAuth 授权连接 GitHub 账户。
	ErrNotConnected = errors.New("github not connected")
	// ErrDeviceFlowActive 表示当前已有一个正在进行的 Device Flow 授权轮询会话。
	ErrDeviceFlowActive = errors.New("device flow already active")
)

// Service 管理服务端的 GitHub OAuth 凭据持久化、授权流程轮询及 GitHub 账户公钥注册。
type Service struct {
	mu                   sync.RWMutex
	DataDir              string
	CredsPath            string
	DeviceKeysPath       string
	ClientID             string
	Client               *Client
	Logger               *slog.Logger
	OnCredentialsUpdated func(creds *GitHubCredentials)

	creds          *GitHubCredentials
	deviceKeys     DeviceKeysStore
	deviceFlowCode *DeviceCodeResponse
	deviceFlowExp  time.Time
	pollCancel     context.CancelFunc
}

// NewService 创建并初始化 GitHub 凭据同步服务，自动从数据目录加载已持久化的凭据与密钥记录。
func NewService(dataDir string, client *Client, logger *slog.Logger) (*Service, error) {

	if client == nil {
		client = NewClient(nil)
	}
	if logger == nil {
		logger = slog.Default()
	}
	if dataDir == "" {
		dataDir = "."
	}
	credsPath := filepath.Join(dataDir, "github_credentials.json")
	deviceKeysPath := filepath.Join(dataDir, "github_device_keys.json")

	s := &Service{
		DataDir:        dataDir,
		CredsPath:      credsPath,
		DeviceKeysPath: deviceKeysPath,
		ClientID:       DefaultClientID,
		Client:         client,
		Logger:         logger,
		deviceKeys:     DeviceKeysStore{Devices: make(map[string]DeviceKeyRecord)},
	}

	if err := s.loadFromDisk(); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Service) loadFromDisk() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Load credentials
	if b, err := os.ReadFile(s.CredsPath); err == nil {
		var creds GitHubCredentials
		if err := json.Unmarshal(b, &creds); err == nil {
			s.creds = &creds
		} else {
			s.Logger.Warn("failed_to_decode_github_credentials", "error", err)
		}
	}

	// Load device keys
	if b, err := os.ReadFile(s.DeviceKeysPath); err == nil {
		var store DeviceKeysStore
		if err := json.Unmarshal(b, &store); err == nil {
			if store.Devices == nil {
				store.Devices = make(map[string]DeviceKeyRecord)
			}
			s.deviceKeys = store
		} else {
			s.Logger.Warn("failed_to_decode_github_device_keys", "error", err)
		}
	}

	return nil
}

// IsConnected 判断当前是否已安全存储有效的 GitHub OAuth 访问凭据。
func (s *Service) IsConnected() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.creds != nil && s.creds.Auth.AccessToken != ""
}

// InDeviceFlow 判断当前是否存在正在进行且尚未过期的 Device Flow 授权流程。
func (s *Service) InDeviceFlow() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.deviceFlowCode != nil && time.Now().Before(s.deviceFlowExp)
}

// GetCredentials 获取已存储的 GitHub 凭据副本；若未连接则返回 ErrNotConnected。
func (s *Service) GetCredentials() (GitHubCredentials, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.creds == nil || s.creds.Auth.AccessToken == "" {
		return GitHubCredentials{}, ErrNotConnected
	}
	return *s.creds, nil
}

// GetStatus 返回服务端 GitHub 连通状态、授权用户画像及设备密钥同步统计。
func (s *Service) GetStatus(totalEnabled, syncedDevices int) StatusResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	res := StatusResponse{
		Connected:     s.creds != nil && s.creds.Auth.AccessToken != "",
		SyncedDevices: syncedDevices,
		TotalEnabled:  totalEnabled,
	}

	if s.creds != nil {
		res.User = s.creds.User
		res.RedactedToken = RedactToken(s.creds.Auth.AccessToken)
		res.Version = s.creds.Version
		res.UpdatedAt = s.creds.UpdatedAt
	}

	if s.deviceFlowCode != nil && time.Now().Before(s.deviceFlowExp) {
		res.InDeviceFlow = true
		res.DeviceUserCode = s.deviceFlowCode.UserCode
		res.DeviceURL = s.deviceFlowCode.VerificationURI
	}

	return res
}

// StartDeviceFlow 发起新的 GitHub OAuth 设备授权流程并在后台启动令牌轮询协程。
func (s *Service) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {

	s.mu.Lock()
	if s.deviceFlowCode != nil && time.Now().Before(s.deviceFlowExp) {
		code := *s.deviceFlowCode
		s.mu.Unlock()
		return &code, nil
	}
	if s.pollCancel != nil {
		s.pollCancel()
		s.pollCancel = nil
	}
	s.mu.Unlock()

	codeResp, err := s.Client.RequestDeviceCode(ctx, s.ClientID, DefaultScope)
	if err != nil {
		return nil, fmt.Errorf("request device code: %w", err)
	}

	s.mu.Lock()
	s.deviceFlowCode = codeResp
	expiresIn := codeResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 900
	}
	s.deviceFlowExp = time.Now().Add(time.Duration(expiresIn) * time.Second)

	pollCtx, cancel := context.WithTimeout(context.Background(), time.Duration(expiresIn)*time.Second)
	s.pollCancel = cancel
	s.mu.Unlock()

	s.Logger.Info("github_device_flow_started", "user_code", codeResp.UserCode, "verification_uri", codeResp.VerificationURI)

	// Start a single controlled background polling worker honoring rate-limits
	go func(code *DeviceCodeResponse, initialInterval int) {
		interval := time.Duration(initialInterval) * time.Second
		if interval < 5*time.Second {
			interval = 5 * time.Second
		}
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-time.After(interval):
				creds, err := s.PollAndSaveDeviceFlow(pollCtx)
				if err == nil && creds != nil {
					s.Logger.Info("github_device_flow_completed_successfully", "user", creds.User.Login)
					if s.OnCredentialsUpdated != nil {
						s.OnCredentialsUpdated(creds)
					}
					return
				}
				if errors.Is(err, ErrSlowDown) {
					interval += 5 * time.Second
					s.Logger.Warn("github_poll_slow_down", "new_interval", interval)
				} else if errors.Is(err, ErrCodeExpired) || errors.Is(err, ErrAccessDenied) {
					return
				}
			}
		}
	}(codeResp, codeResp.Interval)

	return codeResp, nil
}

func (s *Service) PollAndSaveDeviceFlow(ctx context.Context) (*GitHubCredentials, error) {
	pollCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s.mu.RLock()
	code := s.deviceFlowCode
	exp := s.deviceFlowExp
	s.mu.RUnlock()

	if code == nil || time.Now().After(exp) {
		return nil, errors.New("no active device flow session")
	}

	tokResp, err := s.Client.PollAccessToken(pollCtx, s.ClientID, code.DeviceCode)
	if err != nil {
		if !errors.Is(err, ErrAuthPending) && !errors.Is(err, ErrSlowDown) {
			s.Logger.Warn("github_poll_token_failed", "error", err, "user_code", code.UserCode)
		}
		return nil, err
	}

	s.Logger.Info("github_token_acquired_fetching_user_profile", "scope", tokResp.Scope)

	// Fetch user profile
	user, err := s.Client.GetUserProfile(pollCtx, tokResp.AccessToken)
	if err != nil {
		s.Logger.Error("github_get_user_profile_failed", "error", err)
		return nil, fmt.Errorf("fetch user profile: %w", err)
	}

	now := time.Now().UTC()
	s.mu.Lock()
	var newVersion int64 = 1
	if s.creds != nil {
		newVersion = s.creds.Version + 1
	}
	newCreds := GitHubCredentials{
		Version: newVersion,
		User:    *user,
		Auth: GitHubAuth{
			TokenType:   tokResp.TokenType,
			AccessToken: tokResp.AccessToken,
			Scope:       tokResp.Scope,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.creds = &newCreds
	s.deviceFlowCode = nil
	s.mu.Unlock()

	if err := s.saveCredsToDisk(newCreds); err != nil {
		s.Logger.Error("failed_to_save_github_credentials", "error", err)
		return &newCreds, fmt.Errorf("save credentials to disk: %w", err)
	}

	s.Logger.Info("github_authenticated_successfully", "login", user.Login, "id", user.ID)
	return &newCreds, nil
}

func (s *Service) saveCredsToDisk(creds GitHubCredentials) error {
	b, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(s.CredsPath, append(b, '\n'), 0600)
}

func (s *Service) saveDeviceKeysToDisk() error {
	b, err := json.MarshalIndent(s.deviceKeys, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(s.DeviceKeysPath, append(b, '\n'), 0600)
}

// Disconnect 清除本地保存的 GitHub OAuth 凭据、重置设备公钥映射，并自动从 GitHub 远程账户删除已托管的公钥。
// 返回成功删除的远程公钥数量。
func (s *Service) Disconnect(ctx context.Context) (int, error) {
	s.mu.Lock()
	token := ""
	if s.creds != nil {
		token = s.creds.Auth.AccessToken
	}
	s.creds = nil
	s.deviceFlowCode = nil
	keysToDelete := make([]DeviceKeyRecord, 0, len(s.deviceKeys.Devices))
	for _, k := range s.deviceKeys.Devices {
		keysToDelete = append(keysToDelete, k)
	}
	s.deviceKeys.Devices = make(map[string]DeviceKeyRecord)
	s.mu.Unlock()

	_ = os.Remove(s.CredsPath)
	_ = os.Remove(s.DeviceKeysPath)
	_ = os.Remove(filepath.Join(s.DataDir, "github_avatar.png"))

	deletedCount := 0
	if token != "" {
		for _, rec := range keysToDelete {
			if rec.GitHubKeyID > 0 {
				if err := s.Client.DeletePublicKey(ctx, token, rec.GitHubKeyID); err == nil {
					deletedCount++
				} else {
					s.Logger.Warn("failed_to_delete_github_public_key_during_disconnect", "key_id", rec.GitHubKeyID, "device_id", rec.DeviceID, "error", err)
				}
			}
		}
	}

	s.Logger.Info("github_account_disconnected", "deleted_keys_count", deletedCount)
	return deletedCount, nil
}

// GetAvatar 返回本地缓存的 GitHub 头像图像或从 GitHub CDN 下载并缓存。
func (s *Service) GetAvatar(ctx context.Context) ([]byte, string, error) {
	s.mu.RLock()
	if s.creds == nil || s.creds.User.AvatarURL == "" {
		s.mu.RUnlock()
		return nil, "", errors.New("no avatar available")
	}
	avatarURL := s.creds.User.AvatarURL
	s.mu.RUnlock()

	avatarPath := filepath.Join(s.DataDir, "github_avatar.png")
	if b, err := os.ReadFile(avatarPath); err == nil && len(b) > 0 {
		return b, "image/png", nil
	}

	req, err := http.NewRequestWithContext(ctx, "GET", avatarURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create avatar request: %w", err)
	}
	req.Header.Set("User-Agent", "HomeAgent-Server/v0.3.0")

	resp, err := s.Client.HTTPClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch avatar: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("avatar fetch status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read avatar data: %w", err)
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/png"
	}

	_ = AtomicWriteFile(avatarPath, data, 0644)
	return data, ct, nil
}

// RegisterDeviceKey 幂等地将设备的 SSH 公钥注册到 GitHub 账户，并持久化映射记录。
func (s *Service) RegisterDeviceKey(ctx context.Context, deviceID, hostname, publicKey, fingerprint string) (int64, error) {
	s.mu.RLock()
	if s.creds == nil || s.creds.Auth.AccessToken == "" {
		s.mu.RUnlock()
		return 0, ErrNotConnected
	}
	token := s.creds.Auth.AccessToken
	existing, exists := s.deviceKeys.Devices[deviceID]
	s.mu.RUnlock()

	// 幂等性检查：若已存在且指纹一致则直接复用已有 keyID
	if exists && existing.GitHubKeyID > 0 && existing.Fingerprint == fingerprint {
		return existing.GitHubKeyID, nil
	}

	title := fmt.Sprintf("homeagent-%s", deviceID)
	if hostname != "" {
		title = fmt.Sprintf("homeagent-%s", hostname)
	}

	keyID, err := s.Client.RegisterPublicKey(ctx, token, title, publicKey)
	if err != nil {
		return 0, fmt.Errorf("register public key on github: %w", err)
	}

	now := time.Now().UTC()
	record := DeviceKeyRecord{
		DeviceID:    deviceID,
		GitHubKeyID: keyID,
		Fingerprint: fingerprint,
		KeyTitle:    title,
		CreatedAt:   now,
		SyncStatus:  "synced",
	}

	s.mu.Lock()
	s.deviceKeys.Devices[deviceID] = record
	_ = s.saveDeviceKeysToDisk()
	s.mu.Unlock()

	s.Logger.Info("device_github_ssh_key_registered", "device_id", deviceID, "github_key_id", keyID, "fingerprint", fingerprint)
	return keyID, nil
}

// DeleteDeviceKey 从 GitHub 远程账户及本地元数据中删除指定设备的 SSH 公钥。
func (s *Service) DeleteDeviceKey(ctx context.Context, deviceID string) error {
	s.mu.Lock()
	rec, exists := s.deviceKeys.Devices[deviceID]
	token := ""
	if s.creds != nil {
		token = s.creds.Auth.AccessToken
	}
	delete(s.deviceKeys.Devices, deviceID)
	_ = s.saveDeviceKeysToDisk()
	s.mu.Unlock()

	if !exists || rec.GitHubKeyID <= 0 || token == "" {
		return nil
	}

	if err := s.Client.DeletePublicKey(ctx, token, rec.GitHubKeyID); err != nil {
		s.Logger.Warn("failed_to_delete_github_device_key", "device_id", deviceID, "github_key_id", rec.GitHubKeyID, "error", err)
		return err
	}

	s.Logger.Info("device_github_ssh_key_deleted", "device_id", deviceID, "github_key_id", rec.GitHubKeyID)
	return nil
}

// GetDeviceKey 查询指定设备的已注册 GitHub 公钥记录。
func (s *Service) GetDeviceKey(deviceID string) (DeviceKeyRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.deviceKeys.Devices[deviceID]
	return rec, ok
}

// ListDeviceKeys 返回所有已注册的 GitHub 设备公钥记录映射副本。
func (s *Service) ListDeviceKeys() map[string]DeviceKeyRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]DeviceKeyRecord, len(s.deviceKeys.Devices))
	for k, v := range s.deviceKeys.Devices {
		out[k] = v
	}
	return out
}

// ResolveSyncPayload 构造用于下发给 Agent 的 GitHub 凭据同步 SSE 事件载荷。
func (s *Service) ResolveSyncPayload() (SyncPayload, error) {

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.creds == nil || s.creds.Auth.AccessToken == "" {
		return SyncPayload{}, ErrNotConnected
	}

	ver := s.creds.Version
	if ver == 0 {
		ver = 1
	}

	hash := ComputeSyncHash(ver, s.creds.User.Login, s.creds.Auth.AccessToken, "ssh")

	return SyncPayload{
		Version: ver,
		Hash:    hash,
		GHConfig: GHConfig{
			Host:        "github.com",
			User:        s.creds.User.Login,
			OAuthToken:  s.creds.Auth.AccessToken,
			GitProtocol: "ssh",
		},
		SSH: SSHSyncConfig{
			EnsureKey:   true,
			KeyFilename: DefaultGitHubKeyFilename,
		},
	}, nil
}
