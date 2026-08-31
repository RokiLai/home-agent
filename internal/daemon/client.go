package daemon

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"homeagent/internal/githubsync"
	"homeagent/internal/sshsync"
	"homeagent/internal/version"
)

// Config 保存 Agent 守护进程连接控制平面 SSE 的各项参数、重连退避策略与公钥路径。
type Config struct {
	ServerURL          string
	ServerURLs         []string
	Token              string
	DeviceID           string
	AuthorizedKeysPath string
	DropbearKeysPath   string
	GitHubKeyPath      string
	GHHostsPath        string
	SSHConfigPath      string
	Logger             *slog.Logger
	HTTPClient         *http.Client
	RetryInitialWait   time.Duration
	RetryMaxWait       time.Duration
	RestartCallback    func() error
	ShutdownRunner     CommandRunner
	CommandLedgerPath  string
}

// Daemon 维护与 HomeAgent 控制平面的长连接，监听并分发 SSE 事件（密钥同步、升级、GitHub 凭据同步等）。
type Daemon struct {
	cfg             Config
	log             *slog.Logger
	mu              sync.RWMutex
	activeServerURL string
	ledger          *commandLedger
}

type commandIDContextKey struct{}

// New 初始化 Agent Daemon 实例，校验必填参数并自动补全平台默认路径。
func New(cfg Config) (*Daemon, error) {
	if len(cfg.ServerURLs) == 0 && cfg.ServerURL != "" {
		cfg.ServerURLs = []string{cfg.ServerURL}
	}
	if cfg.ServerURL == "" && len(cfg.ServerURLs) > 0 {
		cfg.ServerURL = cfg.ServerURLs[0]
	}
	if len(cfg.ServerURLs) == 0 || cfg.Token == "" || cfg.DeviceID == "" {
		return nil, errors.New("server URL(s), token, and device ID are required")
	}

	for i, u := range cfg.ServerURLs {
		cfg.ServerURLs[i] = strings.TrimRight(strings.TrimSpace(u), "/")
	}
	cfg.ServerURL = cfg.ServerURLs[0]

	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{
			Timeout: 0, // 长连接 SSE 流，流式读取无总超时
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   5 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          10,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   5 * time.Second,
				ResponseHeaderTimeout: 5 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		}
	}
	if cfg.RetryInitialWait <= 0 {
		cfg.RetryInitialWait = 1 * time.Second
	}
	if cfg.RetryMaxWait <= 0 {
		cfg.RetryMaxWait = 30 * time.Second
	}
	if cfg.AuthorizedKeysPath == "" {
		p, err := DefaultAuthorizedKeysPath()
		if err != nil {
			return nil, err
		}
		cfg.AuthorizedKeysPath = p
	}
	if cfg.DropbearKeysPath == "" && runtime.GOOS == "linux" {
		if _, err := os.Stat("/etc/dropbear"); err == nil {
			cfg.DropbearKeysPath = "/etc/dropbear/authorized_keys"
		}
	}
	if cfg.GitHubKeyPath == "" {
		sshDir, err := githubsync.DefaultSSHDir()
		if err == nil {
			cfg.GitHubKeyPath = filepath.Join(sshDir, githubsync.DefaultGitHubKeyFilename)
		}
	}
	if cfg.GHHostsPath == "" {
		p, err := githubsync.DefaultGitHubHostsPath()
		if err == nil {
			cfg.GHHostsPath = p
		}
	}
	if cfg.SSHConfigPath == "" {
		p, err := githubsync.DefaultSSHConfigPath()
		if err == nil {
			cfg.SSHConfigPath = p
		}
	}
	ledger, err := openCommandLedger(cfg.CommandLedgerPath)
	if err != nil {
		return nil, fmt.Errorf("open command ledger: %w", err)
	}
	return &Daemon{
		cfg:             cfg,
		log:             cfg.Logger,
		activeServerURL: cfg.ServerURL,
		ledger:          ledger,
	}, nil
}

// GetActiveServerURL 返回当前已成功连接或首选的服务端地址。
func (d *Daemon) GetActiveServerURL() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.activeServerURL != "" {
		return d.activeServerURL
	}
	if len(d.cfg.ServerURLs) > 0 {
		return d.cfg.ServerURLs[0]
	}
	return d.cfg.ServerURL
}

func (d *Daemon) setActiveServerURL(url string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.activeServerURL = strings.TrimRight(strings.TrimSpace(url), "/")
}

// GetServerURLs 返回所有配置的候选服务端地址列表。
func (d *Daemon) GetServerURLs() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if len(d.cfg.ServerURLs) > 0 {
		return append([]string(nil), d.cfg.ServerURLs...)
	}
	if d.cfg.ServerURL != "" {
		return []string{d.cfg.ServerURL}
	}
	return nil
}

// Run 启动守护进程主循环，包含自动指数退避重连、抖动及优雅停机信号捕获。
func (d *Daemon) Run(ctx context.Context) error {

	backoff := d.cfg.RetryInitialWait

	d.log.Info("agent_daemon_started", "device_id", d.cfg.DeviceID, "servers", d.GetServerURLs())
	d.recoverInterruptedCommands(ctx)
	go d.retryCommittedAcks(ctx)

	for {
		select {
		case <-ctx.Done():
			d.log.Info("agent_daemon_stopping", "reason", ctx.Err())
			return ctx.Err()
		default:
		}

		err := d.connectAndListen(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		d.log.Warn("sse_connection_lost_reconnecting", "error", err, "retry_in", backoff)

		// Sleep with jitter
		jitter := d.calculateJitter(backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff + jitter):
		}

		// Exponential backoff increase
		backoff = backoff * 2
		if backoff > d.cfg.RetryMaxWait {
			backoff = d.cfg.RetryMaxWait
		}
	}
}

func (d *Daemon) retryCommittedAcks(ctx context.Context) {
	delay := time.Second
	for {
		pending := d.ledger.pendingAcks()
		failed := false
		for _, ack := range pending {
			if err := d.postAck(ctx, ack); err != nil {
				failed = true
			}
		}
		if failed {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		} else if len(pending) > 0 {
			delay = time.Second
		} else {
			delay = 30 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func (d *Daemon) connectAndListen(ctx context.Context) error {
	urls := d.GetServerURLs()
	var lastErr error

	for _, srvURL := range urls {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		endpoint := fmt.Sprintf("%s/api/v1/devices/%s/events", srvURL, d.cfg.DeviceID)
		req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if err != nil {
			lastErr = fmt.Errorf("create request (%s): %w", srvURL, err)
			continue
		}
		req.Header.Set("Authorization", "Bearer "+d.cfg.Token)
		req.Header.Set("Accept", "text/event-stream")

		resp, err := d.cfg.HTTPClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("connect to SSE endpoint (%s): %w", srvURL, err)
			d.log.Debug("sse_candidate_unreachable", "server", srvURL, "error", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			lastErr = fmt.Errorf("unexpected HTTP %d from %s: %s", resp.StatusCode, srvURL, bytes.TrimSpace(body))
			d.log.Debug("sse_candidate_rejected", "server", srvURL, "status", resp.StatusCode)
			continue
		}

		d.setActiveServerURL(srvURL)
		d.log.Info("sse_connected_successfully", "device_id", d.cfg.DeviceID, "server", srvURL)

		return d.readStream(ctx, resp.Body)
	}

	return lastErr
}

func (d *Daemon) readStream(ctx context.Context, body io.ReadCloser) error {
	defer body.Close()
	reader := bufio.NewReader(body)
	var currentEvent, currentData, currentID string

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("stream read error: %w", err)
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			// Empty line indicates dispatch of event
			if currentEvent != "" || currentData != "" {
				eventCtx := ctx
				if currentID != "" {
					eventCtx = context.WithValue(ctx, commandIDContextKey{}, currentID)
				}
				d.handleEvent(eventCtx, currentEvent, currentData)
				currentEvent = ""
				currentData = ""
				currentID = ""
			}
			continue
		}

		if strings.HasPrefix(line, ":") {
			// SSE comment / keep-alive
			continue
		}

		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "id:") {
			currentID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		} else if strings.HasPrefix(line, "data:") {
			dataVal := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if currentData == "" {
				currentData = dataVal
			} else {
				currentData = currentData + "\n" + dataVal
			}
		}
	}
}

func (d *Daemon) handleEvent(ctx context.Context, eventType, data string) {
	d.log.Debug("sse_event_received", "type", eventType, "data_length", len(data))
	if commandID, ok := ctx.Value(commandIDContextKey{}).(string); ok && commandID != "" && eventType != "ping" {
		record, execute, err := d.ledger.begin(commandID, eventType, data)
		if err != nil {
			d.log.Error("command_ledger_persist_failed", "command_id", commandID, "error", err)
			return
		}
		if !execute && record.Stage == "side_effect_started" {
			if eventType == "shutdown" {
				_ = d.sendAck(ctx, "shutdown", "failed", 0, "", "outcome_uncertain")
				return
			}
			if err = d.ledger.resetPrepared(commandID); err != nil {
				return
			}
			execute = true
		}
		if !execute && record.Stage != "prepared" {
			if record.Stage == "result_committed" && len(record.Ack) > 0 {
				_ = d.postAck(ctx, record.Ack)
			}
			d.log.Info("duplicate_command_suppressed", "command_id", commandID, "stage", record.Stage)
			return
		}
	}

	switch eventType {
	case "key_sync":
		var payload sshsync.KeySyncPayload
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			d.log.Error("failed_to_unmarshal_key_sync_payload", "error", err, "data", data)
			_ = d.sendAck(ctx, "ssh_keys", "error", 0, "", fmt.Sprintf("invalid payload: %v", err))
			return
		}
		if !d.startCommand(ctx) {
			return
		}
		d.sendAccepted(ctx, "ssh_keys")

		// Apply keys with local retry
		err := d.applyKeysWithRetry(ctx, payload.Keys)
		if err != nil {
			d.log.Error("failed_to_apply_keys", "error", err, "version", payload.Version)
			_ = d.sendAck(ctx, "ssh_keys", "error", payload.Version, payload.Hash, err.Error())
			return
		}

		d.log.Info("keys_synced_successfully", "version", payload.Version, "key_count", len(payload.Keys), "hash", payload.Hash)
		_ = d.sendAckResult(ctx, "ssh_keys", "synced", payload.Version, payload.Hash, "", map[string]any{"applied_version": payload.Version, "applied_hash": payload.Hash, "key_count": len(payload.Keys)})

	case "upgrade":
		var payload UpgradePayload
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			d.log.Error("failed_to_unmarshal_upgrade_payload", "error", err, "data", data)
			_ = d.sendAck(ctx, "upgrade", "error", 0, "", fmt.Sprintf("invalid payload: %v", err))
			return
		}
		if !d.startCommand(ctx) {
			return
		}
		d.sendAccepted(ctx, "upgrade")
		targetVer := payload.TargetVersion
		if targetVer == "" {
			targetVer = payload.Version
		}
		downloadURL := d.resolveDownloadURL(payload.URL)
		d.log.Info("upgrade_instruction_received", "target_version", targetVer, "url", downloadURL, "original_url", payload.URL, "force", payload.Force)

		cmdID, _ := ctx.Value(commandIDContextKey{}).(string)
		opts := UpgradeOptions{
			CommandID:       cmdID,
			TargetVersion:   targetVer,
			URL:             downloadURL,
			SHA256:          payload.SHA256,
			Force:           payload.Force,
			HTTPClient:      d.cfg.HTTPClient,
			RestartCallback: nil,
			Logger:          d.log,
		}
		result, err := PerformSelfUpgrade(ctx, opts)
		if err != nil {
			d.log.Error("self_upgrade_failed", "command_id", cmdID, "error", err, "target_version", targetVer)
			_ = d.sendAck(ctx, "upgrade", "error", 0, "", fmt.Sprintf("upgrade failed: %v", err))
			return
		}
		if !result.Updated {
			d.log.Info("self_upgrade_skipped", "command_id", cmdID, "message", result.Message, "total_ms", result.Timing.TotalDurationMs)
			_ = d.sendAckResult(ctx, "upgrade", "synced", 0, "", result.Message, map[string]any{
				"previous_version":  result.PreviousVersion,
				"target_version":    result.TargetVersion,
				"binary_replaced":   false,
				"restart_scheduled": false,
				"timing":            result.Timing,
			})
			return
		}
		d.log.Info("self_upgrade_successful", "command_id", cmdID, "previous", result.PreviousVersion, "target", result.TargetVersion, "total_ms", result.Timing.TotalDurationMs)
		_ = d.sendAckResult(ctx, "upgrade", "upgraded", 0, "", fmt.Sprintf("upgraded from %s to %s", result.PreviousVersion, result.TargetVersion), map[string]any{
			"previous_version":  result.PreviousVersion,
			"target_version":    result.TargetVersion,
			"binary_replaced":   true,
			"restart_scheduled": true,
			"timing":            result.Timing,
		})
		if d.cfg.RestartCallback != nil {
			go func() {
				time.Sleep(500 * time.Millisecond)
				_ = d.cfg.RestartCallback()
			}()
		} else {
			go func() {
				time.Sleep(500 * time.Millisecond)
				d.log.Info("agent_process_exiting_for_restart")
				os.Exit(0)
			}()
		}

	case "github_credentials_sync":
		var payload githubsync.SyncPayload
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			d.log.Error("failed_to_unmarshal_github_sync_payload", "error", err, "data", data)
			_ = d.sendAck(ctx, "github_credentials", "error", 0, "", fmt.Sprintf("invalid payload: %v", err))
			return
		}
		if !d.startCommand(ctx) {
			return
		}
		d.sendAccepted(ctx, "github_credentials")
		d.handleGitHubSync(ctx, payload)

	case "github_credentials_revoke":
		var payload githubsync.RevokePayload
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			d.log.Error("failed_to_unmarshal_github_revoke_payload", "error", err, "data", data)
			_ = d.sendAck(ctx, "github_credentials", "error", 0, "", fmt.Sprintf("invalid payload: %v", err))
			return
		}
		if !d.startCommand(ctx) {
			return
		}
		d.sendAccepted(ctx, "github_credentials")
		d.handleGitHubRevoke(ctx, payload)

	case "shutdown":
		var payload ShutdownPayload
		if data != "" {
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				d.log.Warn("invalid_shutdown_payload_using_default", "error", err, "data", data)
			}
		}
		if !d.startCommand(ctx) {
			return
		}
		d.sendAccepted(ctx, "shutdown")
		d.log.Info("shutdown_instruction_received", "reason", payload.Reason, "delay_seconds", payload.DelaySeconds, "force", payload.Force)
		ScheduleShutdownWithResult(ctx, payload, d.log, d.cfg.ShutdownRunner, func(err error) {
			if err != nil {
				_ = d.sendAck(ctx, "shutdown", "failed", 0, "", err.Error())
				return
			}
			_ = d.sendAckResult(ctx, "shutdown", "succeeded", 0, "", "", map[string]any{"os_command_started": true, "scheduled_delay_seconds": payload.DelaySeconds})
		})

	case "ping":
		d.log.Debug("ping_received", "timestamp", data)
	default:
		d.log.Warn("unhandled_event_type", "type", eventType)
	}
}

func (d *Daemon) recoverInterruptedCommands(ctx context.Context) {
	for _, record := range d.ledger.interrupted() {
		eventCtx := context.WithValue(ctx, commandIDContextKey{}, record.CommandID)
		if record.Kind == "shutdown" {
			_ = d.sendAck(eventCtx, "shutdown", "failed", 0, "", "outcome_uncertain")
			continue
		}
		d.handleEvent(eventCtx, record.Kind, string(record.Payload))
	}
}

func (d *Daemon) handleGitHubSync(ctx context.Context, payload githubsync.SyncPayload) {
	d.log.Info("github_credentials_sync_received", "version", payload.Version, "user", payload.GHConfig.User)

	// 1. Ensure local Ed25519 SSH key
	pubStr, fp, _, err := githubsync.EnsureEd25519KeyPair(d.cfg.GitHubKeyPath, fmt.Sprintf("homeagent-%s", d.cfg.DeviceID))
	if err != nil {
		d.log.Error("failed_to_ensure_github_ssh_key", "error", err)
		_ = d.sendAck(ctx, "github_credentials", "error", payload.Version, payload.Hash, fmt.Sprintf("generate ssh key failed: %v", err))
		return
	}

	// 2. Report public key to server
	if err := d.reportGitHubSSHKey(ctx, pubStr, fp); err != nil {
		d.log.Error("failed_to_report_github_ssh_key", "error", err)
		_ = d.sendAckWithFingerprint(ctx, "github_credentials", "error", payload.Version, payload.Hash, fp, fmt.Sprintf("report ssh key failed: %v", err))
		return
	}

	// 3. Atomically configure ~/.config/gh/hosts.yml
	if err := githubsync.ApplyGHHostsFile(d.cfg.GHHostsPath, payload.GHConfig.Host, payload.GHConfig.User, payload.GHConfig.OAuthToken, payload.GHConfig.GitProtocol); err != nil {
		d.log.Error("failed_to_apply_gh_hosts", "error", err)
		_ = d.sendAckWithFingerprint(ctx, "github_credentials", "error", payload.Version, payload.Hash, fp, fmt.Sprintf("apply gh hosts failed: %v", err))
		return
	}

	// 4. Atomically configure ~/.ssh/config managed block
	if err := githubsync.ApplySSHConfigFile(d.cfg.SSHConfigPath, d.cfg.GitHubKeyPath); err != nil {
		d.log.Error("failed_to_apply_ssh_config", "error", err)
		_ = d.sendAckWithFingerprint(ctx, "github_credentials", "error", payload.Version, payload.Hash, fp, fmt.Sprintf("apply ssh config failed: %v", err))
		return
	}

	d.log.Info("github_credentials_applied_successfully", "version", payload.Version, "user", payload.GHConfig.User, "fingerprint", fp)
	_ = d.sendAckWithFingerprintResult(ctx, "github_credentials", "synced", payload.Version, payload.Hash, fp, "", map[string]any{"version": payload.Version, "hash": payload.Hash, "ssh_fingerprint": fp})
}

func (d *Daemon) handleGitHubRevoke(ctx context.Context, payload githubsync.RevokePayload) {
	d.log.Info("github_credentials_revoke_received", "reason", payload.Reason)

	err := errors.Join(githubsync.RemoveEd25519KeyPair(d.cfg.GitHubKeyPath), githubsync.CleanGHHostsFile(d.cfg.GHHostsPath, "github.com"), githubsync.CleanSSHConfigFile(d.cfg.SSHConfigPath))
	if err != nil {
		d.log.Error("github_credentials_cleanup_failed", "error", err)
		_ = d.sendAck(ctx, "github_credentials", "failed", 0, "", err.Error())
		return
	}

	d.log.Info("github_credentials_cleaned_successfully", "reason", payload.Reason)
	_ = d.sendAckResult(ctx, "github_credentials", "revoked", 0, "", "", map[string]any{"credentials_removed": true, "config_removed": true})
}

func (d *Daemon) reportGitHubSSHKey(ctx context.Context, pubKey, fingerprint string) error {
	endpoint := fmt.Sprintf("%s/api/v1/devices/%s/github/ssh-key", d.GetActiveServerURL(), d.cfg.DeviceID)
	reqBody := githubsync.RegisterSSHKeyRequest{
		PublicKey:   pubKey,
		Fingerprint: fingerprint,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "POST", endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+d.cfg.Token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	return nil
}

func (d *Daemon) applyKeysWithRetry(ctx context.Context, keys []sshsync.Key) error {
	retryDelays := []time.Duration{0, 2 * time.Second, 5 * time.Second, 15 * time.Second}
	var lastErr error

	for _, delay := range retryDelays {
		if delay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		err := d.applyKeysToDestinations(keys)
		if err == nil {
			return nil
		}
		lastErr = err
		d.log.Warn("apply_keys_failed_will_retry", "error", err, "next_delay", delay)
	}
	return fmt.Errorf("apply keys failed after retries: %w", lastErr)
}

func (d *Daemon) applyKeysToDestinations(keys []sshsync.Key) error {
	// 1. Primary authorized_keys
	if err := updateAuthorizedKeysFile(d.cfg.AuthorizedKeysPath, keys); err != nil {
		return fmt.Errorf("primary authorized_keys (%s): %w", d.cfg.AuthorizedKeysPath, err)
	}

	// 2. Dropbear authorized_keys if present (OpenWrt / Linux)
	if d.cfg.DropbearKeysPath != "" {
		if err := updateAuthorizedKeysFile(d.cfg.DropbearKeysPath, keys); err != nil {
			d.log.Warn("dropbear_keys_update_failed", "path", d.cfg.DropbearKeysPath, "error", err)
		}
	}

	return nil
}

func (d *Daemon) sendAck(ctx context.Context, module, status string, versionVal int64, hash, errMsg string) error {
	return d.sendAckResult(ctx, module, status, versionVal, hash, errMsg, nil)
}

func (d *Daemon) sendAckResult(ctx context.Context, module, status string, versionVal int64, hash, errMsg string, result any) error {
	return d.sendAckWithFingerprintResult(ctx, module, status, versionVal, hash, "", errMsg, result)
}

func (d *Daemon) sendAckWithFingerprint(ctx context.Context, module, status string, versionVal int64, hash, fingerprint, errMsg string) error {
	return d.sendAckWithFingerprintResult(ctx, module, status, versionVal, hash, fingerprint, errMsg, nil)
}

func (d *Daemon) sendAckWithFingerprintResult(ctx context.Context, module, status string, versionVal int64, hash, fingerprint, errMsg string, result any) error {
	wireStatus := status
	if commandID, ok := ctx.Value(commandIDContextKey{}).(string); ok && commandID != "" {
		switch status {
		case "error":
			wireStatus = "failed"
		case "synced", "upgraded", "revoked", "shutting_down":
			wireStatus = "succeeded"
		}
	}
	bodyMap := map[string]any{
		"module":          module,
		"status":          wireStatus,
		"applied_version": versionVal,
		"applied_hash":    hash,
		"ssh_fingerprint": fingerprint,
		"error_message":   errMsg,
		"agent_version":   version.Get(),
	}
	if result != nil {
		bodyMap["result"] = result
	}
	if module == "github_credentials" {
		bodyMap["github_version"] = versionVal
	}
	if commandID, ok := ctx.Value(commandIDContextKey{}).(string); ok && commandID != "" {
		bodyMap["command_id"] = commandID
		bodyMap["protocol"] = 1
		bodyMap["ack_mode"] = "two_phase"
	}
	bodyBytes, _ := json.Marshal(bodyMap)
	if commandID, ok := ctx.Value(commandIDContextKey{}).(string); ok && commandID != "" && status != "accepted" {
		if err := d.ledger.commit(commandID, bodyBytes); err != nil {
			return err
		}
	}
	return d.postAck(ctx, bodyBytes)
}

func (d *Daemon) postAck(ctx context.Context, bodyBytes []byte) error {
	endpoint := fmt.Sprintf("%s/api/v1/devices/%s/ack", d.GetActiveServerURL(), d.cfg.DeviceID)
	var decoded map[string]any
	_ = json.Unmarshal(bodyBytes, &decoded)

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "POST", endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+d.cfg.Token)
	req.Header.Set("Content-Type", "application/json")

	client := d.cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		d.log.Warn("failed_to_send_ack", "error", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		d.log.Warn("ack_endpoint_returned_non_200", "status", resp.StatusCode)
		return fmt.Errorf("ack status: %d", resp.StatusCode)
	}
	if id, ok := decoded["command_id"].(string); ok && id != "" {
		_ = d.ledger.confirm(id)
	}
	return nil
}

func (d *Daemon) sendAccepted(ctx context.Context, module string) {
	if commandID, ok := ctx.Value(commandIDContextKey{}).(string); ok && commandID != "" {
		_ = d.sendAck(ctx, module, "accepted", 0, "", "")
	}
}

func (d *Daemon) startCommand(ctx context.Context) bool {
	id, ok := ctx.Value(commandIDContextKey{}).(string)
	if !ok || id == "" {
		return true
	}
	started, err := d.ledger.start(id)
	if err != nil {
		d.log.Error("command_ledger_persist_failed", "command_id", id, "error", err)
		return false
	}
	if !started {
		d.log.Info("duplicate_command_suppressed", "command_id", id)
		return false
	}
	return true
}

func (d *Daemon) calculateJitter(base time.Duration) time.Duration {
	maxJitterMs := int64(base / time.Millisecond / 4)
	if maxJitterMs <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(maxJitterMs))
	if err != nil {
		return 0
	}
	return time.Duration(n.Int64()) * time.Millisecond
}

func updateAuthorizedKeysFile(path string, keys []sshsync.Key) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	updated, err := sshsync.UpdateManagedBlock(existing, keys)
	if err != nil {
		return err
	}
	return AtomicWrite(path, updated)
}

// DefaultAuthorizedKeysPath 返回平台特定的默认 authorized_keys 文件路径。
// 在 Windows 上以 SYSTEM 或 Administrator 身份运行时，自动指向 administrators_authorized_keys。
func DefaultAuthorizedKeysPath() (string, error) {
	if runtime.GOOS == "windows" {
		user := os.Getenv("USERNAME")
		if strings.HasSuffix(user, "$") || strings.EqualFold(user, "Administrator") || strings.EqualFold(user, "SYSTEM") || user == "" {
			base := os.Getenv("ProgramData")
			if base == "" {
				base = `C:\ProgramData`
			}
			return filepath.Join(base, "ssh", "administrators_authorized_keys"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh", "authorized_keys"), nil
}

// AtomicWrite 将数据写入同目录下的临时文件并原子替换至目标路径。
func AtomicWrite(path string, data []byte) error {

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".authorized_keys-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		if err := applyWindowsACL(name); err != nil {
			return err
		}
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	ok = true
	return nil
}

func applyWindowsACL(path string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	user := os.Getenv("USERNAME")
	var cmd *exec.Cmd
	if user == "" || strings.HasSuffix(user, "$") || strings.EqualFold(user, "SYSTEM") || strings.EqualFold(user, "Administrator") {
		cmd = exec.Command("icacls.exe", path, "/inheritance:r", "/grant:r", "SYSTEM:F", "Administrators:F")
	} else {
		cmd = exec.Command("icacls.exe", path, "/inheritance:r", "/grant:r", user+":F", "SYSTEM:F", "Administrators:F")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("set Windows ACL: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func (d *Daemon) resolveDownloadURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	active := d.GetActiveServerURL()
	if active == "" {
		return rawURL
	}
	activeParsed, err := url.Parse(active)
	if err != nil {
		return rawURL
	}
	if strings.HasPrefix(rawURL, "/") {
		return activeParsed.Scheme + "://" + activeParsed.Host + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	// 如果下发的 URL 主机名为 127.0.0.1 或 localhost，而客户端连接的实际服务端非 loopback，则自动替换为当前活跃服务端的 Host
	if (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost") && activeParsed.Hostname() != "127.0.0.1" && activeParsed.Hostname() != "localhost" {
		u.Host = activeParsed.Host
		u.Scheme = activeParsed.Scheme
		return u.String()
	}
	return rawURL
}
