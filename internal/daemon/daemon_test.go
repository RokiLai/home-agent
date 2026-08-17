package daemon

import (
	"context"
	"encoding/json"
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

	"homeagent/internal/sshsync"
)

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
