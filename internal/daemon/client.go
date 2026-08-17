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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"homeagent/internal/sshsync"
)

type Config struct {
	ServerURL          string
	Token              string
	DeviceID           string
	AuthorizedKeysPath string
	DropbearKeysPath   string
	Logger             *slog.Logger
	HTTPClient         *http.Client
	RetryInitialWait   time.Duration
	RetryMaxWait       time.Duration
}

type Daemon struct {
	cfg Config
	log *slog.Logger
}

func New(cfg Config) (*Daemon, error) {
	if cfg.ServerURL == "" || cfg.Token == "" || cfg.DeviceID == "" {
		return nil, errors.New("server URL, token, and device ID are required")
	}
	cfg.ServerURL = strings.TrimRight(cfg.ServerURL, "/")
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 0} // Long-lived SSE stream
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
	return &Daemon{cfg: cfg, log: cfg.Logger}, nil
}

func (d *Daemon) Run(ctx context.Context) error {
	backoff := d.cfg.RetryInitialWait

	d.log.Info("agent_daemon_started", "device_id", d.cfg.DeviceID, "server", d.cfg.ServerURL)

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

func (d *Daemon) connectAndListen(ctx context.Context) error {
	endpoint := fmt.Sprintf("%s/api/v1/devices/%s/events", d.cfg.ServerURL, d.cfg.DeviceID)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+d.cfg.Token)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := d.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("connect to SSE endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("unexpected HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}

	d.log.Info("sse_connected_successfully", "device_id", d.cfg.DeviceID)

	reader := bufio.NewReader(resp.Body)
	var currentEvent, currentData string

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
				d.handleEvent(ctx, currentEvent, currentData)
				currentEvent = ""
				currentData = ""
			}
			continue
		}

		if strings.HasPrefix(line, ":") {
			// SSE comment / keep-alive
			continue
		}

		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
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

	switch eventType {
	case "key_sync":
		var payload sshsync.KeySyncPayload
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			d.log.Error("failed_to_unmarshal_key_sync_payload", "error", err, "data", data)
			_ = d.sendAck(ctx, "ssh_keys", "error", 0, "", fmt.Sprintf("invalid payload: %v", err))
			return
		}

		// Apply keys with local retry
		err := d.applyKeysWithRetry(ctx, payload.Keys)
		if err != nil {
			d.log.Error("failed_to_apply_keys", "error", err, "version", payload.Version)
			_ = d.sendAck(ctx, "ssh_keys", "error", payload.Version, payload.Hash, err.Error())
			return
		}

		d.log.Info("keys_synced_successfully", "version", payload.Version, "key_count", len(payload.Keys), "hash", payload.Hash)
		_ = d.sendAck(ctx, "ssh_keys", "synced", payload.Version, payload.Hash, "")

	case "ping":
		d.log.Debug("ping_received", "timestamp", data)
	default:
		d.log.Warn("unhandled_event_type", "type", eventType)
	}
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

func (d *Daemon) sendAck(ctx context.Context, module, status string, version int64, hash, errMsg string) error {
	endpoint := fmt.Sprintf("%s/api/v1/devices/%s/ack", d.cfg.ServerURL, d.cfg.DeviceID)
	bodyMap := map[string]any{
		"module":          module,
		"status":          status,
		"applied_version": version,
		"applied_hash":    hash,
		"error_message":   errMsg,
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "POST", endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+d.cfg.Token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		d.log.Warn("failed_to_send_ack", "error", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		d.log.Warn("ack_endpoint_returned_non_200", "status", resp.StatusCode)
		return fmt.Errorf("ack status: %d", resp.StatusCode)
	}
	return nil
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

func DefaultAuthorizedKeysPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" && strings.EqualFold(os.Getenv("USERNAME"), "Administrator") {
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "ssh", "administrators_authorized_keys"), nil
	}
	return filepath.Join(home, ".ssh", "authorized_keys"), nil
}

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
	cmd := exec.Command("icacls.exe", path, "/inheritance:r", "/grant:r", user+":F", "SYSTEM:F")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("set Windows ACL: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}
