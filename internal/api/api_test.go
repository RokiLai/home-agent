package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homeagent/internal/broker"
	"homeagent/internal/device"
	"homeagent/internal/registry"
	"homeagent/internal/sshsync"
)

func TestRegisterListDelete(t *testing.T) {
	r, _ := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	b := broker.New()
	s := &Server{Registry: r, Broker: b, Token: "secret", AdminPublicKey: "ssh-ed25519 ADMIN", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.Handler()
	d := device.Device{ID: "a", Hostname: "a", OS: "linux", Arch: "amd64", SSHUser: "u", SSHPort: 22, PublicKey: "ssh-ed25519 AAAA", Addresses: []string{"192.168.1.2"}}
	buf, _ := json.Marshal(d)
	req := httptest.NewRequest("POST", "/api/v1/devices/register", bytes.NewReader(buf))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("register status %d: %s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest("GET", "/api/v1/devices", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("status %d", w.Code)
	}
	req = httptest.NewRequest("DELETE", "/api/v1/devices/a", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatalf("status %d", w.Code)
	}
}

func TestSSEStreamingAndBroadcast(t *testing.T) {
	r, _ := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	b := broker.New()
	s := &Server{
		Registry:       r,
		Broker:         b,
		Token:          "test-token",
		AdminPublicKey: "ssh-ed25519 ADMIN_KEY",
		PingInterval:   100 * time.Millisecond,
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	devA := device.Device{ID: "dev-a", Hostname: "dev-a", OS: "linux", Arch: "amd64", SSHUser: "user", SSHPort: 22, PublicKey: "ssh-ed25519 KEY_A", Addresses: []string{"10.0.0.1"}}
	if _, err := r.Save(devA); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/v1/devices/dev-a/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected Content-Type text/event-stream, got %s", ct)
	}

	reader := bufio.NewReader(resp.Body)

	// Step 1: Read initial snapshot key_sync
	firstEvent, firstData := readSSEEvent(t, reader)
	if firstEvent != "key_sync" {
		t.Fatalf("expected first event to be key_sync, got %s", firstEvent)
	}
	var payload sshsync.KeySyncPayload
	if err := json.Unmarshal([]byte(firstData), &payload); err != nil {
		t.Fatalf("failed to decode initial payload: %v (data=%q)", err, firstData)
	}
	if len(payload.Keys) != 1 || payload.Keys[0].DeviceID != "homeagent-admin" {
		t.Fatalf("unexpected keys in initial snapshot: %+v", payload.Keys)
	}

	// Step 2: Register a new device dev-b, verify dev-a receives broadcast key_sync
	devB := device.Device{ID: "dev-b", Hostname: "dev-b", OS: "darwin", Arch: "arm64", SSHUser: "user", SSHPort: 22, PublicKey: "ssh-ed25519 KEY_B", Addresses: []string{"10.0.0.2"}}
	bJSON, _ := json.Marshal(devB)
	regReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/devices/register", bytes.NewReader(bJSON))
	regReq.Header.Set("Authorization", "Bearer test-token")
	regReq.Header.Set("Content-Type", "application/json")
	regResp, err := http.DefaultClient.Do(regReq)
	if err != nil || regResp.StatusCode != 200 {
		t.Fatalf("failed to register dev-b: %v", err)
	}
	regResp.Body.Close()

	// Dev-a should receive the broadcast event (skip any ping events in between)
	for {
		ev, data := readSSEEvent(t, reader)
		if ev == "ping" {
			continue
		}
		if ev == "key_sync" {
			var updatedPayload sshsync.KeySyncPayload
			if err := json.Unmarshal([]byte(data), &updatedPayload); err != nil {
				t.Fatalf("failed to decode broadcast payload: %v", err)
			}
			if len(updatedPayload.Keys) != 2 {
				t.Fatalf("expected 2 keys after dev-b registered, got %+v", updatedPayload.Keys)
			}
			break
		}
		t.Fatalf("unexpected event: %s", ev)
	}
}

func TestAckEndpointAndSelfHealing(t *testing.T) {
	r, _ := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	b := broker.New()
	s := &Server{
		Registry:       r,
		Broker:         b,
		Token:          "secret",
		AdminPublicKey: "ssh-ed25519 ADMIN_KEY",
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	dev := device.Device{ID: "node-1", Hostname: "node-1", OS: "linux", Arch: "amd64", SSHUser: "user", SSHPort: 22, PublicKey: "ssh-ed25519 K1", Addresses: []string{"10.0.0.1"}}
	if _, err := r.Save(dev); err != nil {
		t.Fatal(err)
	}

	// Subscribe to broker to observe any resync pushes
	ch, unsub := b.Subscribe("node-1")
	defer unsub()

	// 1. Normal ACK
	ackBody := `{"module":"ssh_keys","status":"synced","applied_version":1,"applied_hash":"correct","error_message":""}`
	req := httptest.NewRequest("POST", "/api/v1/devices/node-1/ack", strings.NewReader(ackBody))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("ack status %d: %s", w.Code, w.Body.String())
	}

	saved, _ := r.Get("node-1")
	if saved.SyncStatus != "synced" || saved.AppliedVersion != 1 {
		t.Fatalf("registry not updated: %+v", saved)
	}

	// 2. Hash mismatch ACK should trigger self-healing push
	mismatchAck := `{"module":"ssh_keys","status":"synced","applied_version":1,"applied_hash":"corrupted_hash","error_message":""}`
	req = httptest.NewRequest("POST", "/api/v1/devices/node-1/ack", strings.NewReader(mismatchAck))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("ack status %d", w.Code)
	}

	select {
	case ev := <-ch:
		if ev.Type != "key_sync" {
			t.Fatalf("expected key_sync self-healing event, got %+v", ev)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for self-healing key_sync event")
	}
}

func TestKeysEndpoint(t *testing.T) {
	r, _ := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	s := &Server{
		Registry:       r,
		Token:          "secret",
		AdminPublicKey: "ssh-ed25519 ADMIN_KEY",
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	dev := device.Device{ID: "node-1", Hostname: "node-1", OS: "linux", Arch: "amd64", SSHUser: "user", SSHPort: 22, PublicKey: "ssh-ed25519 K1", Addresses: []string{"10.0.0.1"}}
	r.Save(dev)

	req := httptest.NewRequest("GET", "/api/v1/devices/node-1/keys", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}

	var payload sshsync.KeySyncPayload
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Keys) != 1 || payload.Keys[0].DeviceID != "homeagent-admin" {
		t.Fatalf("unexpected keys: %+v", payload.Keys)
	}
	if payload.Hash == "" {
		t.Fatal("expected non-empty hash")
	}
}

func readSSEEvent(t *testing.T, r *bufio.Reader) (eventType, data string) {
	t.Helper()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("error reading SSE: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if eventType != "" || data != "" {
				return eventType, data
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
}

func TestWebUIDashboard(t *testing.T) {
	r, _ := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	b := broker.New()
	s := &Server{
		Registry:       r,
		Broker:         b,
		Token:          "secret",
		AdminPublicKey: "ssh-ed25519 ADMIN_KEY",
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h := s.Handler()

	// Test GET /
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET / returned %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "HomeAgent") {
		t.Fatalf("expected HomeAgent title in HTML, got: %s", w.Body.String())
	}

	// Test GET /dashboard
	req = httptest.NewRequest("GET", "/dashboard", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /dashboard returned %d", w.Code)
	}

	// Test GET /static/style.css
	req = httptest.NewRequest("GET", "/static/style.css", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /static/style.css returned %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "--bg-main") {
		t.Fatalf("expected CSS content, got: %s", w.Body.String())
	}

	// Test that the per-device sync control is shipped in the dashboard script.
	req = httptest.NewRequest("GET", "/static/app.js", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /static/app.js returned %d", w.Code)
	}
	for _, want := range []string{"btn-sync-device", "/api/v1/devices/${encodeURIComponent(id)}/sync"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("dashboard script missing %q", want)
		}
	}
}

func TestSyncAllTriggersBroadcast(t *testing.T) {
	r, _ := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	b := broker.New()
	s := &Server{
		Registry:       r,
		Broker:         b,
		Token:          "secret",
		AdminPublicKey: "ssh-ed25519 ADMIN_KEY",
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	devA := device.Device{ID: "dev-a", Hostname: "dev-a", OS: "linux", Arch: "amd64", SSHUser: "user", SSHPort: 22, PublicKey: "ssh-ed25519 KEY_A", Addresses: []string{"10.0.0.1"}}
	_, _ = r.Save(devA)

	ch, unsub := b.Subscribe("dev-a")
	defer unsub()

	h := s.Handler()
	req := httptest.NewRequest("POST", "/api/v1/sync", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("POST /api/v1/sync returned %d: %s", w.Code, w.Body.String())
	}

	select {
	case ev := <-ch:
		if ev.Type != "key_sync" {
			t.Fatalf("expected key_sync event, got %s", ev.Type)
		}
		var payload sshsync.KeySyncPayload
		if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
			t.Fatalf("failed to unmarshal payload: %v", err)
		}
		if payload.Version < 1 {
			t.Fatalf("expected version >= 1, got %d", payload.Version)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for key_sync broadcast on syncAll")
	}
}

func TestSyncDeviceOnlyPushesTargetDevice(t *testing.T) {
	r, _ := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	b := broker.New()
	s := &Server{
		Registry:       r,
		Broker:         b,
		Token:          "secret",
		AdminPublicKey: "ssh-ed25519 ADMIN_KEY",
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, id := range []string{"dev-a", "dev-b"} {
		_, _ = r.Save(device.Device{ID: id, Hostname: id, OS: "linux", Arch: "amd64", SSHUser: "user", SSHPort: 22, PublicKey: "ssh-ed25519 KEY_" + id, Addresses: []string{"10.0.0.1"}})
	}

	targetEvents, unsubscribeTarget := b.Subscribe("dev-a")
	defer unsubscribeTarget()
	otherEvents, unsubscribeOther := b.Subscribe("dev-b")
	defer unsubscribeOther()

	req := httptest.NewRequest("POST", "/api/v1/devices/dev-a/sync", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("sync target returned %d: %s", w.Code, w.Body.String())
	}

	select {
	case ev := <-targetEvents:
		if ev.Type != "key_sync" {
			t.Fatalf("expected key_sync event, got %q", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for target device key_sync")
	}
	select {
	case ev := <-otherEvents:
		t.Fatalf("non-target device received unexpected event: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSyncDeviceRejectsUnknownDevice(t *testing.T) {
	r, _ := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	s := &Server{Registry: r, Broker: broker.New(), Token: "secret"}
	req := httptest.NewRequest("POST", "/api/v1/devices/missing/sync", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown device sync returned %d, want 404", w.Code)
	}
}
