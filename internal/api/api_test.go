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
	"homeagent/internal/devicestate"
	"homeagent/internal/prefixstate"
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
		Sync:           &sshsync.Controller{}, // Must remain unused: synchronization is SSE-only.
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
		Sync:           &sshsync.Controller{}, // Must remain unused: synchronization is SSE-only.
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

func TestDeviceNetworkStateAndPrefixAPI(t *testing.T) {
	r, _ := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	b := broker.New()
	devSvc := devicestate.NewService(nil)
	prefSvc := prefixstate.NewService(nil)

	s := &Server{
		Registry:           r,
		Broker:             b,
		Token:              "secret",
		DeviceStateService: devSvc,
		PrefixStateService: prefSvc,
	}
	handler := s.Handler()

	// 1. PUT network-state for dev-1
	body := `{
		"network_id": "home",
		"revision": 1,
		"observed_at": "2026-08-17T12:00:00Z",
		"ipv6_addresses": [
			{"address": "240e:10::1", "interface": "en0"}
		]
	}`
	req := httptest.NewRequest("PUT", "/api/v1/devices/dev-1/network-state", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT network-state returned %d: %s", w.Code, w.Body.String())
	}

	// 2. GET network-state for dev-1
	req = httptest.NewRequest("GET", "/api/v1/devices/dev-1/network-state", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET network-state returned %d: %s", w.Code, w.Body.String())
	}

	// 3. PUT older revision -> 409 Conflict
	olderBody := `{
		"network_id": "home",
		"revision": 0,
		"ipv6_addresses": []
	}`
	req = httptest.NewRequest("PUT", "/api/v1/devices/dev-1/network-state", strings.NewReader(olderBody))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for older revision, got %d", w.Code)
	}

	// 4. PUT router network-prefixes
	prefBody := `{
		"network_id": "home",
		"revision": 1,
		"observed_at": "2026-08-17T12:00:00Z",
		"prefixes": [
			{"prefix": "240e:10::/64"}
		]
	}`
	req = httptest.NewRequest("PUT", "/api/v1/devices/router-1/network-prefixes", strings.NewReader(prefBody))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT network-prefixes returned %d: %s", w.Code, w.Body.String())
	}

	// 5. GET network prefixes
	req = httptest.NewRequest("GET", "/api/v1/networks/home/prefixes", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET prefixes returned %d: %s", w.Code, w.Body.String())
	}

	// 6. GET /api/v1/devices/dev-1/ipv6 (plain text endpoint for ddns-go)
	req = httptest.NewRequest("GET", "/api/v1/devices/dev-1/ipv6", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /ipv6 returned %d: %s", w.Code, w.Body.String())
	}
	gotIP := strings.TrimSpace(w.Body.String())
	if gotIP != "240e:10::1" {
		t.Fatalf("expected 240e:10::1, got %q", gotIP)
	}
}

func TestPatchDeviceAlias(t *testing.T) {
	r, _ := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	b := broker.New()
	s := &Server{Registry: r, Broker: b, Token: "secret", AdminPublicKey: "ssh-ed25519 ADMIN", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.Handler()

	// 1. Unauthorized PATCH
	req := httptest.NewRequest("PATCH", "/api/v1/devices/dev-1", strings.NewReader(`{"alias":"客厅软路由"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}

	// 2. PATCH non-existent device -> 404
	req = httptest.NewRequest("PATCH", "/api/v1/devices/dev-1", strings.NewReader(`{"alias":"客厅软路由"}`))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}

	// Register device
	d := device.Device{ID: "dev-1", Hostname: "openwrt-box", OS: "linux", Arch: "arm64", SSHUser: "root", SSHPort: 22, PublicKey: "ssh-ed25519 KEY1", Addresses: []string{"192.168.1.1"}}
	_, err := r.Save(d)
	if err != nil {
		t.Fatal(err)
	}

	// 3. Valid PATCH alias
	req = httptest.NewRequest("PATCH", "/api/v1/devices/dev-1", strings.NewReader(`{"alias":"主路由器"}`))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resDev device.Device
	if err := json.Unmarshal(w.Body.Bytes(), &resDev); err != nil {
		t.Fatal(err)
	}
	if resDev.Alias != "主路由器" {
		t.Fatalf("expected alias '主路由器', got %q", resDev.Alias)
	}

	// 4. GET device returns updated alias
	req = httptest.NewRequest("GET", "/api/v1/devices/dev-1", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var getDev device.Device
	_ = json.Unmarshal(w.Body.Bytes(), &getDev)
	if getDev.Alias != "主路由器" {
		t.Fatalf("expected getDev alias '主路由器', got %q", getDev.Alias)
	}

	// 5. Invalid JSON -> 400
	req = httptest.NewRequest("PATCH", "/api/v1/devices/dev-1", strings.NewReader(`{invalid}`))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}
