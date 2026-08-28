package daemon

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"homeagent/internal/githubsync"
	"homeagent/internal/sshsync"
)

func TestLaunchdPlistAssociatesStableAppIdentity(t *testing.T) {
	mgr := &ServiceManager{
		BinaryPath: "/Applications/HomeAgent.app/Contents/MacOS/homeagent-agent",
		ServerURL:  "http://192.168.50.10:8888?a=1&b=2",
		Token:      `token\"; touch /tmp/injected; #`,
	}
	plist := mgr.launchdPlistContent()

	for _, want := range []string{
		"<key>AssociatedBundleIdentifiers</key>",
		"<string>com.homeagent.app</string>",
		"<key>LimitLoadToSessionType</key>",
		"<string>/Applications/HomeAgent.app/Contents/MacOS/homeagent-agent</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("launchd plist missing %q:\n%s", want, plist)
		}
	}
	if strings.Contains(plist, "/bin/zsh") || strings.Contains(plist, "<string>-c</string>") {
		t.Fatalf("launchd plist must execute the agent directly, got:\n%s", plist)
	}
	if strings.Contains(plist, "a=1&b=2") {
		t.Fatalf("launchd plist contains unescaped XML data:\n%s", plist)
	}
	var decoded struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal([]byte(plist), &decoded); err != nil {
		t.Fatalf("launchd plist is not valid XML: %v", err)
	}
}

func TestDaemonSSEAndACKFlow(t *testing.T) {
	tempDir := t.TempDir()
	authKeysPath := filepath.Join(tempDir, "authorized_keys")

	var ackReceived int32
	var lastAckMap map[string]any

	// Mock server that serves SSE stream and accepts ACK
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/devices/test-node/events" {
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming unsupported", 500)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher.Flush()

			// Send snapshot
			payload := sshsync.KeySyncPayload{
				Version: 10,
				Hash:    "mock-hash-123",
				Keys: []sshsync.Key{
					{DeviceID: "server", PublicKey: "ssh-ed25519 ADMIN_KEY"},
					{DeviceID: "peer-1", PublicKey: "ssh-ed25519 PEER_KEY"},
				},
			}
			b, _ := json.Marshal(payload)
			fmt.Fprintf(w, "event: key_sync\ndata: %s\n\n", b)
			flusher.Flush()

			// Keep open for a bit
			time.Sleep(500 * time.Millisecond)
			return
		}

		if r.URL.Path == "/api/v1/devices/test-node/ack" && r.Method == "POST" {
			var m map[string]any
			if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			lastAckMap = m
			atomic.AddInt32(&ackReceived, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
			return
		}

		http.NotFound(w, r)
	}))
	defer ts.Close()

	cfg := Config{
		ServerURL:          ts.URL,
		Token:              "test-token",
		DeviceID:           "test-node",
		AuthorizedKeysPath: authKeysPath,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		RetryInitialWait:   10 * time.Millisecond,
		RetryMaxWait:       50 * time.Millisecond,
	}

	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_ = d.Run(ctx)

	// Check if authorized_keys file was written with keys
	content, err := os.ReadFile(authKeysPath)
	if err != nil {
		t.Fatalf("failed to read authorized_keys: %v", err)
	}
	if !strings.Contains(string(content), "ADMIN_KEY") || !strings.Contains(string(content), "PEER_KEY") {
		t.Fatalf("authorized_keys missing expected keys: %s", string(content))
	}

	// Check if ACK was received
	if atomic.LoadInt32(&ackReceived) == 0 {
		t.Fatal("expected ACK to be sent and received")
	}
	if lastAckMap["status"] != "synced" {
		t.Fatalf("expected status synced, got %v", lastAckMap["status"])
	}
}

func TestUpdateAuthorizedKeysFile(t *testing.T) {
	tempDir := t.TempDir()
	authKeysPath := filepath.Join(tempDir, "authorized_keys")

	keys := []sshsync.Key{
		{DeviceID: "node1", PublicKey: "ssh-ed25519 KEY1"},
	}

	if err := updateAuthorizedKeysFile(authKeysPath, keys); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(authKeysPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "KEY1") {
		t.Fatalf("expected KEY1 in content, got %s", string(content))
	}
}

func TestDaemonGitHubCredentialSyncAndRevoke(t *testing.T) {
	tempDir := t.TempDir()
	authKeysPath := filepath.Join(tempDir, "authorized_keys")
	sshKeyPath := filepath.Join(tempDir, "homeagent_github_id_ed25519")
	ghHostsPath := filepath.Join(tempDir, "hosts.yml")
	sshConfigPath := filepath.Join(tempDir, "config")

	var registerKeyReceived int32
	var ackReceived int32
	var lastAckModule, lastAckStatus, lastAckFingerprint string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/devices/test-node/events":
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming unsupported", 500)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher.Flush()

			// 1. Send github_credentials_sync
			syncPayload := githubsync.SyncPayload{
				Version: 2,
				Hash:    "test-hash-2",
				GHConfig: githubsync.GHConfig{
					Host:        "github.com",
					User:        "gh-user",
					OAuthToken:  "gho_sample_token_123",
					GitProtocol: "ssh",
				},
				SSH: githubsync.SSHSyncConfig{
					EnsureKey:   true,
					KeyFilename: "homeagent_github_id_ed25519",
				},
			}
			b, _ := json.Marshal(syncPayload)
			fmt.Fprintf(w, "event: github_credentials_sync\ndata: %s\n\n", b)
			flusher.Flush()

			time.Sleep(100 * time.Millisecond)

			// 2. Send github_credentials_revoke
			revokePayload := githubsync.RevokePayload{
				Timestamp: time.Now().Unix(),
				Reason:    "account_disconnected",
			}
			rb, _ := json.Marshal(revokePayload)
			fmt.Fprintf(w, "event: github_credentials_revoke\ndata: %s\n\n", rb)
			flusher.Flush()

			time.Sleep(300 * time.Millisecond)
			return

		case r.URL.Path == "/api/v1/devices/test-node/github/ssh-key" && r.Method == "POST":
			var req githubsync.RegisterSSHKeyRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if !strings.HasPrefix(req.PublicKey, "ssh-ed25519 ") || !strings.HasPrefix(req.Fingerprint, "SHA256:") {
				t.Errorf("invalid public key or fingerprint: %+v", req)
			}
			atomic.AddInt32(&registerKeyReceived, 1)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "github_key_id": 999})
			return

		case r.URL.Path == "/api/v1/devices/test-node/ack" && r.Method == "POST":
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			lastAckModule = fmt.Sprintf("%v", m["module"])
			lastAckStatus = fmt.Sprintf("%v", m["status"])
			if fp, ok := m["ssh_fingerprint"]; ok {
				lastAckFingerprint = fmt.Sprintf("%v", fp)
			}
			atomic.AddInt32(&ackReceived, 1)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	cfg := Config{
		ServerURL:          ts.URL,
		Token:              "test-token",
		DeviceID:           "test-node",
		AuthorizedKeysPath: authKeysPath,
		GitHubKeyPath:      sshKeyPath,
		GHHostsPath:        ghHostsPath,
		SSHConfigPath:      sshConfigPath,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		RetryInitialWait:   10 * time.Millisecond,
		RetryMaxWait:       50 * time.Millisecond,
	}

	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	_ = d.Run(ctx)

	// Verify public key was reported to server
	if atomic.LoadInt32(&registerKeyReceived) == 0 {
		t.Fatal("expected reportGitHubSSHKey to be called")
	}

	// Verify ACK was received
	if atomic.LoadInt32(&ackReceived) < 2 {
		t.Fatalf("expected at least 2 ACKs (sync and revoke), got %d", atomic.LoadInt32(&ackReceived))
	}
	if lastAckModule != "github_credentials" || lastAckStatus != "revoked" {
		t.Fatalf("expected final ack to be revoked, got: %s / %s", lastAckModule, lastAckStatus)
	}
	_ = lastAckFingerprint
}

func TestDaemonMultiServerFailover(t *testing.T) {
	tempDir := t.TempDir()
	authKeysPath := filepath.Join(tempDir, "authorized_keys")

	// 1. First server is dead / returns 503
	deadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server unavailable", http.StatusServiceUnavailable)
	}))
	defer deadServer.Close()

	// 2. Second server is healthy and serves SSE
	var ackReceived int32
	liveServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/devices/failover-node/events" {
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming unsupported", 500)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher.Flush()

			payload := sshsync.KeySyncPayload{
				Version: 1,
				Hash:    "failover-hash-1",
				Keys: []sshsync.Key{
					{DeviceID: "server", PublicKey: "ssh-ed25519 ADMIN_KEY"},
				},
			}
			b, _ := json.Marshal(payload)
			fmt.Fprintf(w, "event: key_sync\ndata: %s\n\n", b)
			flusher.Flush()

			time.Sleep(200 * time.Millisecond)
			return
		}

		if r.URL.Path == "/api/v1/devices/failover-node/ack" && r.Method == "POST" {
			atomic.AddInt32(&ackReceived, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
			return
		}
	}))
	defer liveServer.Close()

	cfg := Config{
		ServerURLs:         []string{deadServer.URL, liveServer.URL},
		Token:              "test-token",
		DeviceID:           "failover-node",
		AuthorizedKeysPath: authKeysPath,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		RetryInitialWait:   10 * time.Millisecond,
		RetryMaxWait:       50 * time.Millisecond,
	}

	d, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create daemon: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_ = d.Run(ctx)

	// Verify failover succeeded and connected to liveServer
	if d.GetActiveServerURL() != liveServer.URL {
		t.Fatalf("expected active server URL to be %s, got %s", liveServer.URL, d.GetActiveServerURL())
	}
	if atomic.LoadInt32(&ackReceived) == 0 {
		t.Fatal("expected ACK to be received on liveServer after failover")
	}
}
