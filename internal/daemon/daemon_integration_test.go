package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homeagent/internal/api"
	"homeagent/internal/broker"
	"homeagent/internal/device"
	"homeagent/internal/registry"
)

func TestFullControlPlaneLifecycle(t *testing.T) {
	tempDir := t.TempDir()
	regPath := filepath.Join(tempDir, "devices.json")
	authKeysPath := filepath.Join(tempDir, "authorized_keys")

	r, err := registry.Open(regPath)
	if err != nil {
		t.Fatal(err)
	}

	b := broker.New()
	apiServer := &api.Server{
		Registry:       r,
		Broker:         b,
		Token:          "secret-token",
		AdminPublicKey: "ssh-ed25519 ADMIN_PUB",
		PingInterval:   50 * time.Millisecond,
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	// 1. Pre-register node-1
	node1 := device.Device{
		ID:        "node-1",
		Hostname:  "node-1",
		OS:        "linux",
		Arch:      "amd64",
		SSHUser:   "root",
		SSHPort:   22,
		PublicKey: "ssh-ed25519 NODE1_PUB",
		Addresses: []string{"192.168.1.101"},
	}
	if _, err := r.Save(node1); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(apiServer.Handler())
	defer ts.Close()

	// 2. Start Agent Daemon for node-1 in background
	daemonCfg := Config{
		ServerURL:          ts.URL,
		Token:              "secret-token",
		DeviceID:           "node-1",
		AuthorizedKeysPath: authKeysPath,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		RetryInitialWait:   10 * time.Millisecond,
		RetryMaxWait:       50 * time.Millisecond,
	}
	d, err := New(daemonCfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = d.Run(ctx)
	}()

	// 3. Verify node-1 gets initial snapshot and ACKs
	waitForCondition(t, 2*time.Second, func() bool {
		dev, err := r.Get("node-1")
		if err != nil {
			return false
		}
		return dev.SyncStatus == "synced" && dev.AppliedVersion > 0
	}, "initial snapshot sync for node-1")

	// Verify local authorized_keys has admin key
	content, _ := os.ReadFile(authKeysPath)
	if !strings.Contains(string(content), "ADMIN_PUB") {
		t.Fatalf("expected ADMIN_PUB in %s", string(content))
	}

	// 4. Register node-2 and verify real-time broadcast to node-1
	node2 := device.Device{
		ID:        "node-2",
		Hostname:  "node-2",
		OS:        "darwin",
		Arch:      "arm64",
		SSHUser:   "user",
		SSHPort:   22,
		PublicKey: "ssh-ed25519 NODE2_PUB",
		Addresses: []string{"192.168.1.102"},
	}
	n2Bytes, _ := json.Marshal(node2)
	regReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/devices/register", bytes.NewReader(n2Bytes))
	regReq.Header.Set("Authorization", "Bearer secret-token")
	regReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(regReq)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("failed to register node-2: %v", err)
	}
	resp.Body.Close()

	// 5. Verify node-1 receives broadcast and updates authorized_keys with NODE2_PUB
	waitForCondition(t, 2*time.Second, func() bool {
		c, err := os.ReadFile(authKeysPath)
		if err != nil {
			return false
		}
		return strings.Contains(string(c), "NODE2_PUB")
	}, "broadcast key update containing NODE2_PUB")

	// 6. Delete node-2 and verify broadcast key removal
	delReq, _ := http.NewRequest("DELETE", ts.URL+"/api/v1/devices/node-2", nil)
	delReq.Header.Set("Authorization", "Bearer secret-token")
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil || delResp.StatusCode != 204 {
		t.Fatalf("failed to delete node-2: %v", err)
	}
	delResp.Body.Close()

	waitForCondition(t, 2*time.Second, func() bool {
		c, err := os.ReadFile(authKeysPath)
		if err != nil {
			return false
		}
		return !strings.Contains(string(c), "NODE2_PUB")
	}, "broadcast key removal after node-2 deletion")
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool, desc string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for condition: %s", desc)
}
