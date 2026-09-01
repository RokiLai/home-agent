package api

import (
	"bufio"
	"bytes"
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
	"testing"
	"time"

	"homeagent/internal/alerting"
	"homeagent/internal/auth"
	"homeagent/internal/broker"
	"homeagent/internal/command"
	commandfile "homeagent/internal/command/file"
	"homeagent/internal/daemon"
	"homeagent/internal/daemon/upgrade"
	"homeagent/internal/device"
	"homeagent/internal/devicestate"
	"homeagent/internal/githubrelease"
	"homeagent/internal/githubsync"
	"homeagent/internal/health"
	"homeagent/internal/prefixstate"
	"homeagent/internal/registry"
	"homeagent/internal/sshsync"
	"homeagent/internal/upgradeplan"
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

	// GET /devices authorized - verify connected field is returned
	req = httptest.NewRequest("GET", "/api/v1/devices", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /devices status %d: %s", w.Code, w.Body.String())
	}
	var devResp struct {
		ServerHash string `json:"server_hash"`
		Devices    []struct {
			ID        string `json:"id"`
			Connected bool   `json:"connected"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &devResp); err != nil || len(devResp.Devices) != 1 {
		t.Fatalf("unexpected devResp: %v (err: %v)", devResp, err)
	}
	if devResp.Devices[0].Connected {
		t.Fatal("expected device not connected initially")
	}
	expectedServerHash := sshsync.ComputeKeySetHash([]sshsync.Key{
		{DeviceID: "homeagent-admin", PublicKey: "ssh-ed25519 ADMIN"},
		{DeviceID: "a", PublicKey: "ssh-ed25519 AAAA"},
	})
	if devResp.ServerHash != expectedServerHash {
		t.Fatalf("server_hash = %q, want %q", devResp.ServerHash, expectedServerHash)
	}

	// Subscribe device in broker -> verify connected becomes true
	_, unsubscribe := b.Subscribe("a")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if err := json.Unmarshal(w.Body.Bytes(), &devResp); err != nil || !devResp.Devices[0].Connected {
		t.Fatalf("expected device connected after subscribing, got %v", devResp)
	}
	unsubscribe()

	req = httptest.NewRequest("DELETE", "/api/v1/devices/a", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatalf("status %d", w.Code)
	}
}

func TestNetworkRevisionConflictResponseIsStructured(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		body   func(uint64) string
		server func() *Server
	}{
		{
			name: "device network state",
			path: "/api/v1/devices/dev-1/network-state",
			body: func(rev uint64) string {
				return fmt.Sprintf(`{"network_id":"home","revision":%d,"observed_at":"2026-08-27T00:00:00Z","ipv6_addresses":[]}`, rev)
			},
			server: func() *Server { return &Server{Token: "secret", DeviceStateService: devicestate.NewService(nil)} },
		},
		{
			name: "router prefixes",
			path: "/api/v1/devices/router-1/network-prefixes",
			body: func(rev uint64) string {
				return fmt.Sprintf(`{"network_id":"home","revision":%d,"observed_at":"2026-08-27T00:00:00Z","prefixes":[]}`, rev)
			},
			server: func() *Server { return &Server{Token: "secret", PrefixStateService: prefixstate.NewService(nil)} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := tt.server().Handler()
			for _, step := range []struct {
				revision uint64
				status   int
			}{{9, http.StatusOK}, {1, http.StatusConflict}} {
				req := httptest.NewRequest(http.MethodPut, tt.path, strings.NewReader(tt.body(step.revision)))
				req.Header.Set("Authorization", "Bearer secret")
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != step.status {
					t.Fatalf("revision %d status=%d body=%s", step.revision, rec.Code, rec.Body.String())
				}
				if step.status == http.StatusConflict {
					if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
						t.Fatalf("Content-Type=%q", got)
					}
					var body map[string]any
					if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
						t.Fatal(err)
					}
					if body["error"] != "revision_conflict" || body["current_revision"] != float64(9) || body["received_revision"] != float64(1) {
						t.Fatalf("body=%v", body)
					}
					if _, hasToken := body["token"]; hasToken {
						t.Fatal("response must not contain token")
					}
					if _, hasAuth := body["authorization"]; hasAuth {
						t.Fatal("response must not contain authorization")
					}
				}
			}
		})
	}
}

func TestNetworkRevisionContentMismatchResponse(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		body1  string
		body2  string
		server func() *Server
	}{
		{
			name:  "device network state content mismatch",
			path:  "/api/v1/devices/dev-1/network-state",
			body1: `{"network_id":"home","revision":5,"observed_at":"2026-08-27T00:00:00Z","ipv6_addresses":[{"address":"2001:db8:1::1","interface":"eth0","temporary":false,"deprecated":false}]}`,
			body2: `{"network_id":"home","revision":5,"observed_at":"2026-08-27T00:00:00Z","ipv6_addresses":[{"address":"2001:db8:2::2","interface":"eth0","temporary":false,"deprecated":false}]}`,
			server: func() *Server {
				return &Server{Token: "secret", DeviceStateService: devicestate.NewService(nil)}
			},
		},
		{
			name:  "router prefixes content mismatch",
			path:  "/api/v1/devices/router-1/network-prefixes",
			body1: `{"network_id":"home","revision":5,"observed_at":"2026-08-27T00:00:00Z","prefixes":[{"prefix":"2001:db8:1::/64"}]}`,
			body2: `{"network_id":"home","revision":5,"observed_at":"2026-08-27T00:00:00Z","prefixes":[{"prefix":"2001:db8:2::/64"}]}`,
			server: func() *Server {
				return &Server{Token: "secret", PrefixStateService: prefixstate.NewService(nil)}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := tt.server().Handler()
			// Step 1: First request with revision 5
			req1 := httptest.NewRequest(http.MethodPut, tt.path, strings.NewReader(tt.body1))
			req1.Header.Set("Authorization", "Bearer secret")
			rec1 := httptest.NewRecorder()
			h.ServeHTTP(rec1, req1)
			if rec1.Code != http.StatusOK {
				t.Fatalf("first request failed: status=%d body=%s", rec1.Code, rec1.Body.String())
			}

			// Step 2: Second request with identical revision 5 but different payload
			req2 := httptest.NewRequest(http.MethodPut, tt.path, strings.NewReader(tt.body2))
			req2.Header.Set("Authorization", "Bearer secret")
			rec2 := httptest.NewRecorder()
			h.ServeHTTP(rec2, req2)
			if rec2.Code != http.StatusConflict {
				t.Fatalf("expected 409 conflict on content mismatch, got status=%d body=%s", rec2.Code, rec2.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec2.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["error"] != "revision_content_mismatch" || body["current_revision"] != float64(5) || body["received_revision"] != float64(5) {
				t.Fatalf("unexpected content mismatch error body: %+v", body)
			}
		})
	}
}

func TestNetworkRevisionIdempotentSuccess(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		body   string
		server func() (*Server, func() time.Time)
	}{
		{
			name: "device network state idempotent",
			path: "/api/v1/devices/dev-1/network-state",
			body: `{"network_id":"home","revision":7,"observed_at":"2026-08-27T00:00:00Z","ipv6_addresses":[{"address":"2001:db8:1::1","interface":"eth0","temporary":false,"deprecated":false}]}`,
			server: func() (*Server, func() time.Time) {
				svc := devicestate.NewService(nil)
				return &Server{Token: "secret", DeviceStateService: svc}, func() time.Time {
					st, _ := svc.Get("dev-1")
					if st == nil {
						return time.Time{}
					}
					return st.ObservedAt
				}
			},
		},
		{
			name: "router prefixes idempotent refreshes LastSeenAt",
			path: "/api/v1/devices/router-1/network-prefixes",
			body: `{"network_id":"home","revision":7,"observed_at":"2026-08-27T00:00:00Z","prefixes":[{"prefix":"2001:db8:1::/64"}]}`,
			server: func() (*Server, func() time.Time) {
				svc := prefixstate.NewService(nil)
				return &Server{Token: "secret", PrefixStateService: svc}, func() time.Time {
					st, _ := svc.GetByNetwork("home")
					if st == nil {
						return time.Time{}
					}
					return st.LastSeenAt
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, getLastSeen := tt.server()
			h := s.Handler()

			// First request
			req1 := httptest.NewRequest(http.MethodPut, tt.path, strings.NewReader(tt.body))
			req1.Header.Set("Authorization", "Bearer secret")
			rec1 := httptest.NewRecorder()
			h.ServeHTTP(rec1, req1)
			if rec1.Code != http.StatusOK {
				t.Fatalf("first request failed: status=%d body=%s", rec1.Code, rec1.Body.String())
			}
			var resp1 map[string]any
			_ = json.Unmarshal(rec1.Body.Bytes(), &resp1)
			if resp1["accepted_revision"] != float64(7) || resp1["changed"] != true {
				t.Fatalf("unexpected initial response: %+v", resp1)
			}
			seen1 := getLastSeen()

			time.Sleep(10 * time.Millisecond)

			// Idempotent second request: identical revision and payload
			req2 := httptest.NewRequest(http.MethodPut, tt.path, strings.NewReader(tt.body))
			req2.Header.Set("Authorization", "Bearer secret")
			rec2 := httptest.NewRecorder()
			h.ServeHTTP(rec2, req2)
			if rec2.Code != http.StatusOK {
				t.Fatalf("idempotent request failed: status=%d body=%s", rec2.Code, rec2.Body.String())
			}
			var resp2 map[string]any
			_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
			if resp2["accepted_revision"] != float64(7) || resp2["changed"] != false {
				t.Fatalf("idempotent response must have changed=false: %+v", resp2)
			}
			seen2 := getLastSeen()
			if strings.Contains(tt.name, "LastSeenAt") && !seen2.After(seen1) {
				t.Fatalf("LastSeenAt was not refreshed on idempotent call: seen1=%v seen2=%v", seen1, seen2)
			}
		})
	}
}

func TestPutDeviceFactsUpdatesDynamicFactsAndPreservesMACWhenMissing(t *testing.T) {
	r, err := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	original := device.Device{
		ID: "dev-facts", Hostname: "old-host", MAC: "02:00:00:00:00:01",
		AgentVersion: "v0.4.3", OS: "darwin", Arch: "arm64", SSHUser: "user",
		SSHPort: 22, PublicKey: "ssh-ed25519 AAAA", Addresses: []string{"192.168.1.10"},
	}
	if _, err := r.Save(original); err != nil {
		t.Fatal(err)
	}
	s := &Server{Registry: r, Token: "device-token", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.Handler()

	body := `{"hostname":"new-host","agent_version":"v0.4.4","os":"darwin","arch":"arm64","ssh_user":"user","ssh_port":22,"addresses":["192.168.1.42","100.100.1.1"]}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/devices/dev-facts/facts", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer device-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT facts status %d: %s", rec.Code, rec.Body.String())
	}

	got, err := r.Get("dev-facts")
	if err != nil {
		t.Fatal(err)
	}
	if got.Hostname != "new-host" || got.AgentVersion != "v0.4.4" {
		t.Fatalf("facts not updated: %+v", got)
	}
	if len(got.Addresses) != 1 || got.Addresses[0] != "192.168.1.42" {
		t.Fatalf("addresses not safely normalized: %v", got.Addresses)
	}
	if got.MAC != original.MAC {
		t.Fatalf("missing reported MAC must preserve existing MAC: got %q", got.MAC)
	}

	unauthorized := httptest.NewRequest(http.MethodPut, "/api/v1/devices/dev-facts/facts", strings.NewReader(body))
	unauthorizedRec := httptest.NewRecorder()
	h.ServeHTTP(unauthorizedRec, unauthorized)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized facts update status = %d, want 401", unauthorizedRec.Code)
	}
}

func TestPutDeviceFactsSSHUserProtection(t *testing.T) {
	r, err := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	original := device.Device{
		ID: "dev-ssh-protect", Hostname: "win-host", MAC: "02:00:00:00:00:02",
		AgentVersion: "v0.6.8", OS: "windows", Arch: "amd64", SSHUser: "Administrator",
		SSHPort: 22, PublicKey: "ssh-ed25519 AAAA", Addresses: []string{"192.168.1.50"},
	}
	if _, err := r.Save(original); err != nil {
		t.Fatal(err)
	}
	s := &Server{Registry: r, Token: "device-token", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.Handler()

	// 1. Negative test: reporting a Windows machine account ending with $ must NOT overwrite existing SSHUser
	machinePayload := `{"hostname":"win-host","os":"windows","arch":"amd64","ssh_user":"ROKILAI$","ssh_port":22,"addresses":["192.168.1.50"]}`
	req1 := httptest.NewRequest(http.MethodPut, "/api/v1/devices/dev-ssh-protect/facts", strings.NewReader(machinePayload))
	req1.Header.Set("Authorization", "Bearer device-token")
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("PUT facts with machine account status %d: %s", rec1.Code, rec1.Body.String())
	}
	got1, _ := r.Get("dev-ssh-protect")
	if got1.SSHUser != "Administrator" {
		t.Fatalf("machine account ROKILAI$ must be rejected, got %q, want Administrator", got1.SSHUser)
	}

	// 2. Negative test: reporting an empty ssh_user must NOT clear existing SSHUser
	emptyPayload := `{"hostname":"win-host","os":"windows","arch":"amd64","ssh_user":"","ssh_port":22,"addresses":["192.168.1.50"]}`
	req2 := httptest.NewRequest(http.MethodPut, "/api/v1/devices/dev-ssh-protect/facts", strings.NewReader(emptyPayload))
	req2.Header.Set("Authorization", "Bearer device-token")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("PUT facts with empty ssh_user status %d: %s", rec2.Code, rec2.Body.String())
	}
	got2, _ := r.Get("dev-ssh-protect")
	if got2.SSHUser != "Administrator" {
		t.Fatalf("empty ssh_user must preserve existing Administrator, got %q", got2.SSHUser)
	}

	// 3. Positive test: reporting a valid explicit ssh_user updates the record
	validPayload := `{"hostname":"win-host","os":"windows","arch":"amd64","ssh_user":"customadmin","ssh_port":2222,"addresses":["192.168.1.50"]}`
	req3 := httptest.NewRequest(http.MethodPut, "/api/v1/devices/dev-ssh-protect/facts", strings.NewReader(validPayload))
	req3.Header.Set("Authorization", "Bearer device-token")
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("PUT facts with valid ssh_user status %d: %s", rec3.Code, rec3.Body.String())
	}
	got3, _ := r.Get("dev-ssh-protect")
	if got3.SSHUser != "customadmin" || got3.SSHPort != 2222 {
		t.Fatalf("expected customadmin:2222, got %s:%d", got3.SSHUser, got3.SSHPort)
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
	if len(payload.Keys) != 2 || payload.Keys[0].DeviceID != "homeagent-admin" || payload.Keys[1].DeviceID != "dev-a" {
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
			if len(updatedPayload.Keys) != 3 || updatedPayload.Keys[0].DeviceID != "homeagent-admin" || updatedPayload.Keys[1].DeviceID != "dev-a" || updatedPayload.Keys[2].DeviceID != "dev-b" {
				t.Fatalf("expected admin, self, and dev-b keys after registration, got %+v", updatedPayload.Keys)
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
	if len(payload.Keys) != 2 || payload.Keys[0].DeviceID != "homeagent-admin" || payload.Keys[1].DeviceID != "node-1" {
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

	// Test that the per-device sync control is shipped in the dashboard scripts.
	req = httptest.NewRequest("GET", "/static/js/devices/render.js", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /static/js/devices/render.js returned %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "btn-sync-device") {
		t.Fatalf("render script missing btn-sync-device")
	}

	req = httptest.NewRequest("GET", "/static/js/devices/actions.js", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /static/js/devices/actions.js returned %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "/api/v1/devices/${encodeURIComponent(id)}/sync") {
		t.Fatalf("actions script missing sync API endpoint")
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

	_, _ = r.Save(device.Device{
		ID:        "dev-1",
		Hostname:  "dev-1",
		OS:        "linux",
		Arch:      "amd64",
		SSHUser:   "root",
		SSHPort:   22,
		PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleValidKey1",
	})
	_, _ = r.Save(device.Device{
		ID:        "router-1",
		Hostname:  "router-1",
		OS:        "linux",
		Arch:      "arm64",
		SSHUser:   "root",
		SSHPort:   22,
		PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleValidKey2",
	})

	// 1. PUT network-state for dev-1 (with MAC)
	body := `{
		"network_id": "home",
		"revision": 1,
		"observed_at": "2026-08-17T12:00:00Z",
		"ipv6_addresses": [
			{"address": "2001:db8:10::1", "interface": "en0"}
		],
		"mac": "02:00:00:00:00:01"
	}`
	req := httptest.NewRequest("PUT", "/api/v1/devices/dev-1/network-state", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT network-state returned %d: %s", w.Code, w.Body.String())
	}

	d1, err := r.Get("dev-1")
	if err != nil || d1.MAC != "02:00:00:00:00:01" {
		t.Fatalf("expected MAC 02:00:00:00:00:01, got %q (err: %v)", d1.MAC, err)
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

	// 4. PUT router network-prefixes (with MAC)
	prefBody := `{
		"network_id": "home",
		"revision": 1,
		"observed_at": "2026-08-17T12:00:00Z",
		"prefixes": [
			{"prefix": "2001:db8:10::/64"}
		],
		"mac": "02:00:00:00:00:02"
	}`
	req = httptest.NewRequest("PUT", "/api/v1/devices/router-1/network-prefixes", strings.NewReader(prefBody))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT network-prefixes returned %d: %s", w.Code, w.Body.String())
	}

	r1, err := r.Get("router-1")
	if err != nil || r1.MAC != "02:00:00:00:00:02" {
		t.Fatalf("expected MAC 02:00:00:00:00:02, got %q (err: %v)", r1.MAC, err)
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
	if gotIP != "2001:db8:10::1" {
		t.Fatalf("expected 2001:db8:10::1, got %q", gotIP)
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

func TestPatchDeviceMAC(t *testing.T) {
	r, _ := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	b := broker.New()
	s := &Server{Registry: r, Broker: b, Token: "secret", AdminPublicKey: "ssh-ed25519 ADMIN", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.Handler()

	d := device.Device{ID: "dev-mac", Hostname: "win-pc", OS: "windows", Arch: "amd64", SSHUser: "user", SSHPort: 22, PublicKey: "ssh-ed25519 KEY1", Addresses: []string{"192.168.1.50"}}
	_, _ = r.Save(d)

	// 1. Valid PATCH with MAC
	req := httptest.NewRequest("PATCH", "/api/v1/devices/dev-mac", strings.NewReader(`{"alias":"主力主机","mac":"02-00-00-11-22-33"}`))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resDev device.Device
	_ = json.Unmarshal(w.Body.Bytes(), &resDev)
	if resDev.Alias != "主力主机" || resDev.MAC != "02:00:00:11:22:33" {
		t.Fatalf("unexpected updated dev: %+v", resDev)
	}

	// 2. Invalid MAC -> 400
	req = httptest.NewRequest("PATCH", "/api/v1/devices/dev-mac", strings.NewReader(`{"mac":"invalid-mac"}`))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestWakeDevice(t *testing.T) {
	r, _ := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	b := broker.New()
	s := &Server{Registry: r, Broker: b, Token: "secret", AdminPublicKey: "ssh-ed25519 ADMIN", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.Handler()

	devWithMAC := device.Device{
		ID:        "dev-wol",
		Hostname:  "win-box",
		MAC:       "02:00:00:11:22:33",
		OS:        "windows",
		Arch:      "amd64",
		SSHUser:   "user",
		SSHPort:   22,
		PublicKey: "ssh-ed25519 KEY1",
		Addresses: []string{"192.168.1.100"},
	}
	devNoMAC := device.Device{
		ID:        "dev-nomac",
		Hostname:  "nomac-box",
		OS:        "linux",
		Arch:      "amd64",
		SSHUser:   "user",
		SSHPort:   22,
		PublicKey: "ssh-ed25519 KEY2",
		Addresses: []string{"192.168.1.101"},
	}
	_, _ = r.Save(devWithMAC)
	_, _ = r.Save(devNoMAC)

	// 1. Unauthorized
	req := httptest.NewRequest("POST", "/api/v1/devices/dev-wol/wake", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}

	// 2. Non-existent device -> 404
	req = httptest.NewRequest("POST", "/api/v1/devices/nonexistent/wake", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}

	// 3. Device without MAC -> 400
	req = httptest.NewRequest("POST", "/api/v1/devices/dev-nomac/wake", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Invalid broadcast address -> 400
	req = httptest.NewRequest("POST", "/api/v1/devices/dev-wol/wake", strings.NewReader(`{"broadcast":"invalid-ip"}`))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}

	// 5. Valid wake request -> 200 OK
	req = httptest.NewRequest("POST", "/api/v1/devices/dev-wol/wake", strings.NewReader(`{"broadcast":"127.0.0.1"}`))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var respMap map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &respMap); err != nil {
		t.Fatal(err)
	}
	if respMap["status"] != "ok" || respMap["mac"] != "02:00:00:11:22:33" {
		t.Fatalf("unexpected wake response: %+v", respMap)
	}

	// 6. Rate limit (subsequent request within 5s) -> 429 Too Many Requests
	req2 := httptest.NewRequest("POST", "/api/v1/devices/dev-wol/wake", nil)
	req2.Header.Set("Authorization", "Bearer secret")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 rate limited, got %d", w2.Code)
	}
}

func TestUpgradeEndpoints(t *testing.T) {
	r, _ := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	b := broker.New()
	s := &Server{
		Registry:       r,
		Broker:         b,
		Token:          "secret",
		AdminPublicKey: "ssh-ed25519 ADMIN",
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h := s.Handler()

	dev := device.Device{
		ID:        "node-upgrade",
		Hostname:  "node-upgrade",
		OS:        "linux",
		Arch:      "amd64",
		SSHUser:   "root",
		SSHPort:   22,
		PublicKey: "ssh-ed25519 AAAA",
		Addresses: []string{"192.168.1.50"},
	}
	if _, err := r.Save(dev); err != nil {
		t.Fatal(err)
	}

	// 1. Unauthorized -> 401
	req := httptest.NewRequest("POST", "/api/v1/devices/node-upgrade/upgrade", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}

	// 2. Nonexistent device -> 404
	req = httptest.NewRequest("POST", "/api/v1/devices/nonexistent/upgrade", strings.NewReader(`{"url":"http://example.com/bin"}`))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}

	// 3. Subscribe device to SSE broker and trigger upgrade
	ch, unsubscribe := b.Subscribe("node-upgrade")
	defer unsubscribe()

	upgradeBody := `{"version":"v1.5.0","url":"http://192.168.1.10:8080/downloads/homeagent-agent-linux-amd64","sha256":"abcdef123456","force":true}`
	req = httptest.NewRequest("POST", "/api/v1/devices/node-upgrade/upgrade", strings.NewReader(upgradeBody))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res["status"] != "ok" || res["target_version"] != "v1.5.0" || res["active_listeners"] != float64(1) {
		t.Fatalf("unexpected upgrade response: %+v", res)
	}

	// Check broker received event
	select {
	case ev := <-ch:
		if ev.Type != "upgrade" {
			t.Fatalf("expected event type 'upgrade', got %q", ev.Type)
		}
		if !strings.Contains(ev.Data, "v1.5.0") || !strings.Contains(ev.Data, "abcdef123456") {
			t.Fatalf("unexpected event data: %s", ev.Data)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for upgrade event in broker channel")
	}

	// 4. Test Upgrade-All
	req = httptest.NewRequest("POST", "/api/v1/devices/upgrade-all", strings.NewReader(upgradeBody))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var allRes map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &allRes); err != nil {
		t.Fatal(err)
	}
	if allRes["status"] != "ok" || allRes["total"] != float64(1) || allRes["dispatched_count"] != float64(1) {
		t.Fatalf("unexpected upgrade-all response: %+v", allRes)
	}

	// 5. Upgrade ACK preserves runtime AgentVersion; a subsequent facts report owns that projection.
	ackBody := `{"module":"upgrade","status":"upgraded","applied_version":0,"applied_hash":"","agent_version":"v1.5.0","error_message":""}`
	ackReq := httptest.NewRequest("POST", "/api/v1/devices/node-upgrade/ack", strings.NewReader(ackBody))
	ackReq.Header.Set("Authorization", "Bearer secret")
	wAck := httptest.NewRecorder()
	h.ServeHTTP(wAck, ackReq)
	if wAck.Code != http.StatusOK {
		t.Fatalf("expected 200 on ack, got %d: %s", wAck.Code, wAck.Body.String())
	}

	updatedDev, err := r.Get("node-upgrade")
	if err != nil {
		t.Fatal(err)
	}
	if updatedDev.AgentVersion != "" {
		t.Fatalf("upgrade ACK must not infer runtime AgentVersion, got %q", updatedDev.AgentVersion)
	}
}

func TestDeviceAck_ModuleIsolation(t *testing.T) {
	tempDir := t.TempDir()
	r, _ := registry.Open(filepath.Join(tempDir, "devices.json"))
	b := broker.New()
	s := &Server{
		Registry:       r,
		Broker:         b,
		Token:          "secret",
		AdminPublicKey: "ssh-ed25519 ADMIN",
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h := s.Handler()

	dev := device.Device{
		ID:             "dev-isolation",
		Hostname:       "dev-isolation",
		OS:             "darwin",
		Arch:           "arm64",
		SSHUser:        "exampleuser",
		SSHPort:        22,
		PublicKey:      "ssh-ed25519 AAAA",
		Addresses:      []string{"192.168.1.80"},
		SyncStatus:     "synced",
		AppliedVersion: 3,
		AppliedHash:    "hash-v3",
		AgentVersion:   "v0.4.0",
	}
	if _, err := r.Save(dev); err != nil {
		t.Fatal(err)
	}

	// 1. Upgrade module ACK with 0 version and empty hash should NOT overwrite AppliedVersion/AppliedHash
	upgradeAck := `{"module":"upgrade","status":"synced","applied_version":0,"applied_hash":"","agent_version":"v0.4.3","error_message":"already up to date (v0.4.3)"}`
	req := httptest.NewRequest("POST", "/api/v1/devices/dev-isolation/ack", strings.NewReader(upgradeAck))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	d, err := r.Get("dev-isolation")
	if err != nil {
		t.Fatal(err)
	}
	if d.AgentVersion != "v0.4.0" {
		t.Fatalf("ACK must preserve facts-owned AgentVersion v0.4.0, got %q", d.AgentVersion)
	}
	if d.AppliedVersion != 3 || d.AppliedHash != "hash-v3" || d.SyncStatus != "synced" {
		t.Fatalf("upgrade ACK corrupted SSH sync status: AppliedVersion=%d, AppliedHash=%q, SyncStatus=%q", d.AppliedVersion, d.AppliedHash, d.SyncStatus)
	}

	// 2. Shutdown module ACK should also NOT overwrite AppliedVersion/AppliedHash
	shutdownAck := `{"module":"shutdown","status":"shutting_down","applied_version":0,"applied_hash":"","agent_version":"v0.4.3","error_message":"reboot"}`
	req = httptest.NewRequest("POST", "/api/v1/devices/dev-isolation/ack", strings.NewReader(shutdownAck))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	d, err = r.Get("dev-isolation")
	if err != nil {
		t.Fatal(err)
	}
	if d.AppliedVersion != 3 || d.AppliedHash != "hash-v3" {
		t.Fatalf("shutdown ACK corrupted SSH sync status: AppliedVersion=%d, AppliedHash=%q", d.AppliedVersion, d.AppliedHash)
	}

	// 3. ssh_keys module ACK SHOULD update AppliedVersion/AppliedHash
	sshKeysAck := `{"module":"ssh_keys","status":"synced","applied_version":4,"applied_hash":"hash-v4","agent_version":"v0.4.3","error_message":""}`
	req = httptest.NewRequest("POST", "/api/v1/devices/dev-isolation/ack", strings.NewReader(sshKeysAck))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	d, err = r.Get("dev-isolation")
	if err != nil {
		t.Fatal(err)
	}
	if d.AppliedVersion != 4 || d.AppliedHash != "hash-v4" {
		t.Fatalf("expected AppliedVersion=4, AppliedHash=hash-v4, got %d, %q", d.AppliedVersion, d.AppliedHash)
	}
}

func TestDeviceAck_SucceededNormalizedToSynced(t *testing.T) {
	tempDir := t.TempDir()
	r, err := registry.Open(filepath.Join(tempDir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Registry: r,
		Token:    "secret",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h := s.Handler()

	dev := device.Device{
		ID:         "dev-succeeded-test",
		Hostname:   "dev-succeeded-test",
		OS:         "darwin",
		Arch:       "arm64",
		SSHUser:    "exampleuser",
		SSHPort:    22,
		PublicKey:  "ssh-ed25519 AAAA",
		Addresses:  []string{"192.168.1.90"},
		SyncStatus: "pending",
	}
	if _, err := r.Save(dev); err != nil {
		t.Fatal(err)
	}

	// 1. Two-phase Command ACK with status: "succeeded" for ssh_keys
	sshKeysAck := `{"module":"ssh_keys","status":"succeeded","applied_version":5,"applied_hash":"hash-v5","error_message":""}`
	req := httptest.NewRequest("POST", "/api/v1/devices/dev-succeeded-test/ack", strings.NewReader(sshKeysAck))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	d, err := r.Get("dev-succeeded-test")
	if err != nil {
		t.Fatal(err)
	}
	if d.SyncStatus != "synced" {
		t.Fatalf("expected SyncStatus to be normalized to 'synced', got %q", d.SyncStatus)
	}
	if d.AppliedVersion != 5 || d.AppliedHash != "hash-v5" {
		t.Fatalf("expected version=5, hash=hash-v5, got version=%d, hash=%q", d.AppliedVersion, d.AppliedHash)
	}

	// 2. Two-phase Command ACK with status: "succeeded" for github_credentials
	ghAck := `{"module":"github_credentials","status":"succeeded","ssh_fingerprint":"SHA256:abc","error_message":""}`
	req = httptest.NewRequest("POST", "/api/v1/devices/dev-succeeded-test/ack", strings.NewReader(ghAck))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	d, err = r.Get("dev-succeeded-test")
	if err != nil {
		t.Fatal(err)
	}
	if d.GitHubStatus != "synced" {
		t.Fatalf("expected GitHubStatus to be normalized to 'synced', got %q", d.GitHubStatus)
	}
}

func TestGitHubCredentialSync_EndToEndAPI(t *testing.T) {
	tempDir := t.TempDir()
	r, _ := registry.Open(filepath.Join(tempDir, "devices.json"))
	b := broker.New()

	// Fake GitHub Server
	fakeGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/login/device/code":
			_ = json.NewEncoder(w).Encode(githubsync.DeviceCodeResponse{
				DeviceCode:      "dc-100",
				UserCode:        "USER-100",
				VerificationURI: "https://github.com/login/device",
				ExpiresIn:       10,
				Interval:        1,
			})
		case req.URL.Path == "/login/oauth/access_token":
			_ = json.NewEncoder(w).Encode(githubsync.TokenResponse{
				AccessToken: "gho_test_mock_token_12345",
				TokenType:   "bearer",
				Scope:       "repo",
			})
		case req.URL.Path == "/user":
			_ = json.NewEncoder(w).Encode(githubsync.GitHubUser{
				Login: "tester",
				ID:    1122,
			})
		case req.URL.Path == "/user/keys" && req.Method == "POST":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    556677,
				"title": "homeagent-dev-mac",
				"key":   "ssh-ed25519 AAAAC3Nza...",
			})
		case strings.HasPrefix(req.URL.Path, "/user/keys/") && req.Method == "DELETE":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer fakeGitHub.Close()

	ghClient := githubsync.NewClient(fakeGitHub.Client())
	ghClient.OAuthBase = fakeGitHub.URL
	ghClient.APIBase = fakeGitHub.URL

	ghSvc, err := githubsync.NewService(tempDir, ghClient, nil)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	s := &Server{
		Registry:          r,
		Broker:            b,
		Token:             "secret",
		GitHubSyncService: ghSvc,
		Log:               slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h := s.Handler()

	// 1. Initial status -> disconnected
	req := httptest.NewRequest("GET", "/api/v1/github/status", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /api/v1/github/status: %d", w.Code)
	}
	var statusRes githubsync.StatusResponse
	_ = json.Unmarshal(w.Body.Bytes(), &statusRes)
	if statusRes.Connected {
		t.Fatalf("expected disconnected")
	}

	// 2. Start device-code flow
	req = httptest.NewRequest("POST", "/api/v1/github/auth/device-code", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("POST /api/v1/github/auth/device-code: %d", w.Code)
	}
	var devCode githubsync.DeviceCodeResponse
	_ = json.Unmarshal(w.Body.Bytes(), &devCode)
	if devCode.UserCode != "USER-100" {
		t.Fatalf("expected USER-100, got: %s", devCode.UserCode)
	}

	// Wait briefly for background poller to finish
	time.Sleep(1200 * time.Millisecond)

	if !ghSvc.IsConnected() {
		// Manually trigger if background poller didn't hit in time
		_, _ = ghSvc.PollAndSaveDeviceFlow(context.Background())
	}
	if !ghSvc.IsConnected() {
		t.Fatalf("expected GitHub service to be connected")
	}

	// Status should now be connected with redacted token
	req = httptest.NewRequest("GET", "/api/v1/github/status", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	_ = json.Unmarshal(w.Body.Bytes(), &statusRes)
	if !statusRes.Connected || statusRes.User.Login != "tester" {
		t.Fatalf("unexpected status: %+v", statusRes)
	}
	if !strings.HasPrefix(statusRes.RedactedToken, "gho_****") {
		t.Fatalf("expected redacted token, got: %s", statusRes.RedactedToken)
	}

	// 3. Register a device with GitHubSyncEnabled = false
	d := device.Device{ID: "dev-mac", Hostname: "dev-mac", OS: "darwin", Arch: "arm64", SSHUser: "u", SSHPort: 22, PublicKey: "ssh-ed25519 AAAA", Addresses: []string{"192.168.1.10"}}
	_, _ = r.Save(d)

	ch, unsub := b.Subscribe("dev-mac")
	defer unsub()

	// 4. Try to register SSH key before enabling sync -> should be 403 Forbidden
	sshKeyBody := `{"public_key":"ssh-ed25519 AAAAC3Nza... test","fingerprint":"SHA256:abcd"}`
	req = httptest.NewRequest("POST", "/api/v1/devices/dev-mac/github/ssh-key", strings.NewReader(sshKeyBody))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden, got: %d", w.Code)
	}

	// 5. Enable GitHub Sync via PATCH /api/v1/devices/{id}
	patchBody := `{"github_sync_enabled":true}`
	req = httptest.NewRequest("PATCH", "/api/v1/devices/dev-mac", strings.NewReader(patchBody))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("PATCH status: %d (%s)", w.Code, w.Body.String())
	}

	// Verify broker received github_credentials_sync event
	select {
	case ev := <-ch:
		if ev.Type != "github_credentials_sync" {
			t.Fatalf("expected event github_credentials_sync, got: %s", ev.Type)
		}
		if !strings.Contains(ev.Data, "gho_test_mock_token_12345") {
			t.Fatalf("unexpected sync event data: %s", ev.Data)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for github_credentials_sync event")
	}

	// 6. Register SSH key after enabling sync -> 200 OK
	req = httptest.NewRequest("POST", "/api/v1/devices/dev-mac/github/ssh-key", strings.NewReader(sshKeyBody))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("POST ssh-key status: %d (%s)", w.Code, w.Body.String())
	}
	var keyResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &keyResp)
	if keyResp["status"] != "ok" || keyResp["github_key_id"] != float64(556677) {
		t.Fatalf("unexpected ssh-key response: %+v", keyResp)
	}

	// 7. ACK with status "synced"
	ackBody := `{"module":"github_credentials","status":"synced","applied_version":1,"applied_hash":"h1","ssh_fingerprint":"SHA256:abcd"}`
	req = httptest.NewRequest("POST", "/api/v1/devices/dev-mac/ack", strings.NewReader(ackBody))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("ACK status: %d", w.Code)
	}

	devRecord, _ := r.Get("dev-mac")
	if devRecord.GitHubStatus != "synced" || devRecord.GitHubFingerprint != "SHA256:abcd" {
		t.Fatalf("unexpected device github status in registry: %+v", devRecord)
	}

	// 8. Disable GitHub Sync via PATCH -> receives github_credentials_revoke
	// Drain any pending sync events in channel first
	for len(ch) > 0 {
		<-ch
	}

	patchBody = `{"github_sync_enabled":false}`
	req = httptest.NewRequest("PATCH", "/api/v1/devices/dev-mac", strings.NewReader(patchBody))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("PATCH status: %d", w.Code)
	}

	select {
	case ev := <-ch:
		if ev.Type != "github_credentials_revoke" {
			t.Fatalf("expected event github_credentials_revoke, got: %s", ev.Type)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for github_credentials_revoke event")
	}

	// 9. Re-enable sync and test Disconnect
	for len(ch) > 0 {
		<-ch
	}
	patchBody = `{"github_sync_enabled":true}`
	req = httptest.NewRequest("PATCH", "/api/v1/devices/dev-mac", strings.NewReader(patchBody))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	// Consume sync event
	select {
	case ev := <-ch:
		if ev.Type != "github_credentials_sync" {
			t.Fatalf("expected github_credentials_sync, got: %s", ev.Type)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for sync on re-enable")
	}

	for len(ch) > 0 {
		<-ch
	}

	// Disconnect GitHub account
	req = httptest.NewRequest("POST", "/api/v1/github/disconnect", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("POST /api/v1/github/disconnect status: %d (%s)", w.Code, w.Body.String())
	}

	select {
	case ev := <-ch:
		if ev.Type != "github_credentials_revoke" {
			t.Fatalf("expected revoke event on disconnect, got: %s", ev.Type)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for revoke on disconnect")
	}
}

func TestShutdownEndpoint(t *testing.T) {
	r, _ := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	b := broker.New()
	s := &Server{
		Registry:       r,
		Broker:         b,
		Token:          "secret",
		AdminPublicKey: "ssh-ed25519 ADMIN",
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h := s.Handler()

	dev := device.Device{
		ID:        "dev-shutdown",
		Hostname:  "host-shutdown",
		OS:        "linux",
		Arch:      "amd64",
		SSHUser:   "root",
		SSHPort:   22,
		PublicKey: "ssh-ed25519 AAAA",
		Addresses: []string{"192.168.1.100"},
	}
	_, _ = r.Save(dev)

	// 1. Unauthorized -> 401
	req := httptest.NewRequest("POST", "/api/v1/devices/dev-shutdown/shutdown", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}

	// 2. Non-existent device -> 404
	req = httptest.NewRequest("POST", "/api/v1/devices/nonexistent/shutdown", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}

	// 3. Device offline (no active SSE subscriber) -> 400 Bad Request
	req = httptest.NewRequest("POST", "/api/v1/devices/dev-shutdown/shutdown", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for offline device, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Online device -> 200 OK and receives SSE event
	ch, unsub := b.Subscribe("dev-shutdown")
	defer unsub()

	reqBody := `{"reason":"test_shutdown_api","delay_seconds":2,"force":true}`
	req = httptest.NewRequest("POST", "/api/v1/devices/dev-shutdown/shutdown", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["status"] != "shutting_down" || resp["device_id"] != "dev-shutdown" {
		t.Fatalf("unexpected shutdown response: %+v", resp)
	}

	select {
	case ev := <-ch:
		if ev.Type != "shutdown" {
			t.Fatalf("expected shutdown event, got: %s", ev.Type)
		}
		var payload daemon.ShutdownPayload
		if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
			t.Fatalf("failed to parse event data: %v", err)
		}
		if payload.Reason != "test_shutdown_api" || payload.DelaySeconds != 2 || !payload.Force {
			t.Fatalf("unexpected payload in event: %+v", payload)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for shutdown event")
	}
}

func TestResolveUpgradePayload_LoopbackHostRewrite(t *testing.T) {
	tempDir := t.TempDir()
	binName := "homeagent-agent-linux-amd64"
	binPath := filepath.Join(tempDir, binName)
	if err := os.WriteFile(binPath, []byte("dummy-binary-content"), 0755); err != nil {
		t.Fatal(err)
	}

	s := &Server{
		DownloadsDir: tempDir,
	}

	dev := device.Device{
		ID:   "dev-remote-1",
		OS:   "linux",
		Arch: "amd64",
	}

	// 1. Request with 127.0.0.1:8888
	req := httptest.NewRequest("POST", "/api/v1/devices/upgrade-all", nil)
	req.Host = "127.0.0.1:8888"

	payload, err := s.ResolveUpgradePayload(dev, UpgradeRequest{}, req)
	if err != nil {
		t.Fatalf("ResolveUpgradePayload failed: %v", err)
	}

	// Host should NOT be 127.0.0.1 if LAN IP is detected
	if strings.Contains(payload.URL, "127.0.0.1") && detectPrimaryLANIP() != "" {
		t.Fatalf("expected loopback 127.0.0.1 to be rewritten to LAN IP, got: %s", payload.URL)
	}

	// 2. Request with explicit non-loopback host header
	req2 := httptest.NewRequest("POST", "/api/v1/devices/upgrade-all", nil)
	req2.Host = "192.168.1.100:9999"

	payload2, err := s.ResolveUpgradePayload(dev, UpgradeRequest{}, req2)
	if err != nil {
		t.Fatalf("ResolveUpgradePayload failed: %v", err)
	}
	if !strings.HasPrefix(payload2.URL, "http://192.168.1.100:9999/downloads/") {
		t.Fatalf("expected explicit host to be preserved, got: %s", payload2.URL)
	}
}

func TestUpgradeAllReportsPerDeviceDispatchResults(t *testing.T) {
	tempDir := t.TempDir()
	downloadsDir := filepath.Join(tempDir, "downloads")
	if err := os.MkdirAll(downloadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(downloadsDir, "homeagent-agent-linux-amd64"), []byte("candidate"), 0755); err != nil {
		t.Fatal(err)
	}

	r, err := registry.Open(filepath.Join(tempDir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []device.Device{
		{ID: "online-ready", Hostname: "online-ready", OS: "linux", Arch: "amd64", SSHUser: "root", SSHPort: 22, PublicKey: "ssh-ed25519 AAAA"},
		{ID: "offline-ready", Hostname: "offline-ready", OS: "linux", Arch: "amd64", SSHUser: "root", SSHPort: 22, PublicKey: "ssh-ed25519 AAAA"},
		{ID: "online-missing", Hostname: "online-missing", OS: "darwin", Arch: "arm64", SSHUser: "root", SSHPort: 22, PublicKey: "ssh-ed25519 AAAA"},
	} {
		if _, err := r.Save(d); err != nil {
			t.Fatal(err)
		}
	}

	b := broker.New()
	_, unsubscribeReady := b.Subscribe("online-ready")
	defer unsubscribeReady()
	_, unsubscribeMissing := b.Subscribe("online-missing")
	defer unsubscribeMissing()

	s := &Server{
		Registry: r, Broker: b, Token: "secret", DownloadsDir: downloadsDir, UpgradeSource: "local",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/upgrade-all", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Host = "192.168.50.20:8888"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upgrade-all status %d: %s", rec.Code, rec.Body.String())
	}

	var response struct {
		DispatchedCount int `json:"dispatched_count"`
		SkippedCount    int `json:"skipped_count"`
		FailedCount     int `json:"failed_count"`
		DeviceResults   []struct {
			DeviceID string `json:"device_id"`
			Status   string `json:"status"`
			Reason   string `json:"reason"`
		} `json:"device_results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.DispatchedCount != 1 || response.SkippedCount != 1 || response.FailedCount != 1 {
		t.Fatalf("unexpected counts: %+v", response)
	}
	results := map[string]struct{ status, reason string }{}
	for _, result := range response.DeviceResults {
		results[result.DeviceID] = struct{ status, reason string }{result.Status, result.Reason}
	}
	if got := results["online-ready"]; got.status != "dispatched" {
		t.Fatalf("online-ready result = %+v", got)
	}
	if got := results["offline-ready"]; got.status != "skipped" || got.reason != "device_offline" {
		t.Fatalf("offline-ready result = %+v", got)
	}
	if got := results["online-missing"]; got.status != "failed" || got.reason != "artifact_unavailable" {
		t.Fatalf("online-missing result = %+v", got)
	}
}

func TestHealthAndAlertingAPI(t *testing.T) {
	tempDir := t.TempDir()
	r, err := registry.Open(filepath.Join(tempDir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}

	d := device.Device{
		ID:           "dev-test-1",
		Hostname:     "test-host",
		AgentVersion: "v0.6.0",
		OS:           "linux",
		Arch:         "amd64",
		SSHUser:      "root",
		SSHPort:      22,
		PublicKey:    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMockKeyForDeviceHealthTests1234567890123",
		LastSeenAt:   time.Now().UTC(),
	}
	if _, err := r.Save(d); err != nil {
		t.Fatal(err)
	}

	healthRepo, err := health.NewFileRepository(filepath.Join(tempDir, "health"))
	if err != nil {
		t.Fatal(err)
	}
	healthSvc := health.NewService(health.ServiceConfig{
		Config: health.DefaultEvaluatorConfig(),
		Repo:   healthRepo,
	})

	alertRepo, err := alerting.NewFileRepository(filepath.Join(tempDir, "alerting"))
	if err != nil {
		t.Fatal(err)
	}
	alertSvc := alerting.NewService(alerting.ServiceConfig{
		Repo: alertRepo,
	})

	server := &Server{
		Registry: r,
		Token:    "admin-token",
		Health:   healthSvc,
		Alerting: alertSvc,
	}
	handler := server.Handler()

	// 1. GET /api/v1/health/summary
	req := httptest.NewRequest("GET", "/api/v1/health/summary", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/health/summary status %d: %s", w.Code, w.Body.String())
	}

	// 2. GET /api/v1/devices/dev-test-1/health
	req = httptest.NewRequest("GET", "/api/v1/devices/dev-test-1/health", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/devices/dev-test-1/health status %d: %s", w.Code, w.Body.String())
	}

	// 3. GET /api/v1/devices/dev-test-1/health/events
	req = httptest.NewRequest("GET", "/api/v1/devices/dev-test-1/health/events", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/devices/dev-test-1/health/events status %d: %s", w.Code, w.Body.String())
	}

	// 4. POST /api/v1/alerts/silences
	silBody := `{"device_id":"dev-test-1","reason_code":"device_offline","starts_at":"2026-08-24T00:00:00Z","ends_at":"2026-08-25T00:00:00Z","comment":"maintenance"}`
	req = httptest.NewRequest("POST", "/api/v1/alerts/silences", strings.NewReader(silBody))
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/alerts/silences status %d: %s", w.Code, w.Body.String())
	}
	var createdSil struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &createdSil)

	// 5. GET /api/v1/alerts/silences
	req = httptest.NewRequest("GET", "/api/v1/alerts/silences", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/alerts/silences status %d: %s", w.Code, w.Body.String())
	}

	// 6. DELETE /api/v1/alerts/silences/{id}
	req = httptest.NewRequest("DELETE", "/api/v1/alerts/silences/"+createdSil.ID, nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE /api/v1/alerts/silences status %d: %s", w.Code, w.Body.String())
	}

	// 7. GET /api/v1/alerts
	req = httptest.NewRequest("GET", "/api/v1/alerts", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/alerts status %d: %s", w.Code, w.Body.String())
	}

	// 8. GET /api/v1/alert-deliveries
	req = httptest.NewRequest("GET", "/api/v1/alert-deliveries", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/alert-deliveries status %d: %s", w.Code, w.Body.String())
	}
}

func TestPutDeviceFacts_RuntimeValidationAndClearance(t *testing.T) {
	tempDir := t.TempDir()
	r, _ := registry.Open(filepath.Join(tempDir, "devices.json"))
	devToken := "dev-token-val"
	d := device.Device{
		ID:              "dev-val-1",
		Hostname:        "val-node",
		SSHUser:         "root",
		SSHPort:         22,
		PublicKey:       "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIValKey12345678901234567890",
		DeviceTokenHash: auth.HashToken(devToken),
		RuntimeFacts: &device.RuntimeFacts{
			DiskTotalBytes:     100,
			DiskAvailableBytes: 50,
		},
	}
	if _, err := r.Save(d); err != nil {
		t.Fatal(err)
	}

	s := &Server{
		Registry: r,
		Token:    "admin-token",
	}
	handler := s.Handler()

	// 1. 反例: available > total (内存超标) -> 400
	badPayload1, _ := json.Marshal(map[string]any{
		"hostname": "val-node",
		"runtime": map[string]any{
			"memory_total_bytes":     100,
			"memory_available_bytes": 200,
		},
	})
	req := httptest.NewRequest("PUT", "/api/v1/devices/dev-val-1/facts", bytes.NewReader(badPayload1))
	req.Header.Set("Authorization", "Bearer "+devToken)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for memory available > total, got %d", w.Code)
	}

	// 2. 反例: 负数 uptime -> 400
	badPayload2, _ := json.Marshal(map[string]any{
		"hostname": "val-node",
		"runtime": map[string]any{
			"uptime_seconds": -10,
		},
	})
	req = httptest.NewRequest("PUT", "/api/v1/devices/dev-val-1/facts", bytes.NewReader(badPayload2))
	req.Header.Set("Authorization", "Bearer "+devToken)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for negative uptime, got %d", w.Code)
	}

	// 3. 当新上报缺少 runtime 时，旧 runtime 必须被清空而不是继续沿用
	noRuntimePayload, _ := json.Marshal(map[string]any{
		"hostname":  "val-node",
		"ssh_user":  "root",
		"ssh_port":  22,
		"addresses": []string{"192.168.1.50"},
	})
	req = httptest.NewRequest("PUT", "/api/v1/devices/dev-val-1/facts", bytes.NewReader(noRuntimePayload))
	req.Header.Set("Authorization", "Bearer "+devToken)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}

	saved, _ := r.Get("dev-val-1")
	if saved.RuntimeFacts != nil {
		t.Fatalf("expected runtime facts to be cleared when payload omits runtime, got %+v", saved.RuntimeFacts)
	}
}

func TestServerConfigPublicURL(t *testing.T) {
	// 1. 默认情况 (PublicURL 未显式配置 -> 默认返回 https://homeagent.rokilai.online)
	sDefault := &Server{}
	handlerDefault := sDefault.Handler()

	req := httptest.NewRequest("GET", "/api/v1/config", nil)
	w := httptest.NewRecorder()
	handlerDefault.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
	var respDefault map[string]any
	if err := json.NewDecoder(w.Body).Decode(&respDefault); err != nil {
		t.Fatal(err)
	}
	if respDefault["public_url"] != "https://homeagent.rokilai.online" {
		t.Fatalf("expected default public_url https://homeagent.rokilai.online, got %v", respDefault["public_url"])
	}

	// 2. 自定义配置 PublicURL
	sCustom := &Server{PublicURL: "https://custom.myagent.org"}
	handlerCustom := sCustom.Handler()

	req = httptest.NewRequest("GET", "/api/v1/config", nil)
	w = httptest.NewRecorder()
	handlerCustom.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
	var respCustom map[string]any
	if err := json.NewDecoder(w.Body).Decode(&respCustom); err != nil {
		t.Fatal(err)
	}
	if respCustom["public_url"] != "https://custom.myagent.org" {
		t.Fatalf("expected custom public_url https://custom.myagent.org, got %v", respCustom["public_url"])
	}
}

func TestAuthStatusPublicURL(t *testing.T) {
	dir := t.TempDir()
	sm, err := auth.NewSessionManager(filepath.Join(dir, "auth_status.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = sm.InitAdminBootstrap("admin", "AdminPass123!")
	if err != nil {
		t.Fatal(err)
	}
	rawToken, _, err := sm.CreateSession("admin", "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: auth.SessionCookieName, Value: rawToken}

	// 1. 默认 PublicURL -> https://homeagent.rokilai.online
	sDefault := &Server{SessionManager: sm}
	handlerDefault := sDefault.Handler()

	req := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	handlerDefault.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}
	var respDefault map[string]any
	if err := json.NewDecoder(w.Body).Decode(&respDefault); err != nil {
		t.Fatal(err)
	}
	if respDefault["public_url"] != "https://homeagent.rokilai.online" {
		t.Fatalf("expected default public_url https://homeagent.rokilai.online, got %v", respDefault["public_url"])
	}

	// 2. 自定义 PublicURL -> https://custom.selfhost.net
	sCustom := &Server{SessionManager: sm, PublicURL: "https://custom.selfhost.net"}
	handlerCustom := sCustom.Handler()

	req = httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	handlerCustom.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}
	var respCustom map[string]any
	if err := json.NewDecoder(w.Body).Decode(&respCustom); err != nil {
		t.Fatal(err)
	}
	if respCustom["public_url"] != "https://custom.selfhost.net" {
		t.Fatalf("expected custom public_url https://custom.selfhost.net, got %v", respCustom["public_url"])
	}
}

func TestUpgradePlan_OrchestrationAndIdempotency(t *testing.T) {
	tempDir := t.TempDir()
	r, err := registry.Open(filepath.Join(tempDir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	cmdRepo, err := commandfile.Open(filepath.Join(tempDir, "commands.json"))
	if err != nil {
		t.Fatal(err)
	}
	cmdSvc := command.NewService(cmdRepo, nil)
	b := broker.New()

	d := device.Device{
		ID:        "dev-plan-1",
		Hostname:  "mac-mini",
		OS:        "darwin",
		Arch:      "arm64",
		SSHUser:   "roki",
		SSHPort:   22,
		PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleValidKey",
	}
	if _, err := r.Save(d); err != nil {
		t.Fatal(err)
	}

	s := &Server{
		Registry: r,
		Commands: cmdSvc,
		Broker:   b,
		Token:    "secret",
	}
	handler := s.Handler()

	// 1. First upgrade request with Idempotency-Key
	upgradeBody := `{"version":"v0.7.0","url":"https://example.com/bin","sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`
	req := httptest.NewRequest("POST", "/api/v1/devices/dev-plan-1/upgrade", strings.NewReader(upgradeBody))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Idempotency-Key", "idem-plan-1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}
	var res map[string]any
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	planID, ok := res["plan_id"].(string)
	if !ok || planID == "" {
		t.Fatalf("expected plan_id in response, got %+v", res)
	}
	if res["plan_stage"] != "bridge_pending" && res["plan_stage"] != "target_pending" {
		t.Fatalf("unexpected plan_stage: %v", res["plan_stage"])
	}

	// 2. Same Idempotency-Key -> returns same plan
	req2 := httptest.NewRequest("POST", "/api/v1/devices/dev-plan-1/upgrade", strings.NewReader(upgradeBody))
	req2.Header.Set("Authorization", "Bearer secret")
	req2.Header.Set("Idempotency-Key", "idem-plan-1")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on idempotent retry, got %d", w2.Code)
	}
	var res2 map[string]any
	_ = json.NewDecoder(w2.Body).Decode(&res2)
	if res2["plan_id"] != planID {
		t.Fatalf("expected same plan_id %s, got %s", planID, res2["plan_id"])
	}

	// 3. GET /api/v1/upgrade-plans
	getPlansReq := httptest.NewRequest("GET", "/api/v1/upgrade-plans?device_id=dev-plan-1", nil)
	getPlansReq.Header.Set("Authorization", "Bearer secret")
	wPlans := httptest.NewRecorder()
	handler.ServeHTTP(wPlans, getPlansReq)
	if wPlans.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for list upgrade plans, got %d", wPlans.Code)
	}
	var plansList []upgradeplan.UpgradePlan
	if err := json.NewDecoder(wPlans.Body).Decode(&plansList); err != nil {
		t.Fatal(err)
	}
	if len(plansList) != 1 || plansList[0].PlanID != planID {
		t.Fatalf("unexpected plans list: %+v", plansList)
	}

	// 4. GET /api/v1/upgrade-plans/{id}
	getPlanReq := httptest.NewRequest("GET", "/api/v1/upgrade-plans/"+planID, nil)
	getPlanReq.Header.Set("Authorization", "Bearer secret")
	wPlan := httptest.NewRecorder()
	handler.ServeHTTP(wPlan, getPlanReq)
	if wPlan.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for get upgrade plan, got %d", wPlan.Code)
	}
	var singlePlan upgradeplan.UpgradePlan
	if err := json.NewDecoder(wPlan.Body).Decode(&singlePlan); err != nil {
		t.Fatal(err)
	}
	if singlePlan.PlanID != planID {
		t.Fatalf("expected plan_id %s, got %s", planID, singlePlan.PlanID)
	}
}

func TestUpgrade_V2LockedDisabledSafety(t *testing.T) {
	tempDir := t.TempDir()
	r, err := registry.Open(filepath.Join(tempDir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}

	d := device.Device{
		ID:                  "dev-locked",
		Hostname:            "mac-locked",
		OS:                  "darwin",
		Arch:                "arm64",
		SSHUser:             "roki",
		SSHPort:             22,
		PublicKey:           "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleValidKey",
		UpgradeSecurityMode: "v2_locked",
	}
	if _, err := r.Save(d); err != nil {
		t.Fatal(err)
	}

	s := &Server{
		Registry:                 r,
		Token:                    "secret",
		MacOSAppUpgradeV2Enabled: false, // Switch is disabled
	}
	handler := s.Handler()

	upgradeBody := `{"version":"v0.7.0","url":"https://example.com/bin","sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`
	req := httptest.NewRequest("POST", "/api/v1/devices/dev-locked/upgrade", strings.NewReader(upgradeBody))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for v2_locked with v2 disabled, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "v2_upgrade_temporarily_unavailable") {
		t.Fatalf("expected v2_upgrade_temporarily_unavailable message, got %s", w.Body.String())
	}
}

func TestUpgradeAck_ProgressAndFencedConfirmation(t *testing.T) {
	tempDir := t.TempDir()
	r, err := registry.Open(filepath.Join(tempDir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	cmdRepo, err := commandfile.Open(filepath.Join(tempDir, "commands.json"))
	if err != nil {
		t.Fatal(err)
	}
	cmdSvc := command.NewService(cmdRepo, nil)
	b := broker.New()

	devToken := "dev-token-secret-123"
	d := device.Device{
		ID:              "dev-fenced",
		Hostname:        "mac-fenced",
		OS:              "darwin",
		Arch:            "arm64",
		SSHUser:         "roki",
		SSHPort:         22,
		PublicKey:       "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleValidKey",
		DeviceTokenHash: auth.HashToken(devToken),
	}
	if _, err := r.Save(d); err != nil {
		t.Fatal(err)
	}

	s := &Server{
		Registry: r,
		Commands: cmdSvc,
		Broker:   b,
		Token:    "secret",
	}
	handler := s.Handler()

	// 1. Create upgrade command
	cmd, _, err := cmdSvc.Create(command.CreateRequest{
		Kind:        command.KindUpgrade,
		DeviceID:    "dev-fenced",
		RequestedBy: command.Actor{Type: "user", ID: "admin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = cmdSvc.StartDispatch(cmd.ID)
	_, _ = cmdSvc.DispatchResult(cmd.ID, true)

	// 2. Send progress ACK
	seq := uint64(1)
	occ := time.Now().UnixMilli()
	ackBody, _ := json.Marshal(AckRequest{
		CommandID:    cmd.ID,
		Module:       "upgrade",
		Status:       "progress",
		Phase:        "downloading",
		Sequence:     &seq,
		OccurredAt:   &occ,
		ErrorMessage: "",
	})
	ackReq := httptest.NewRequest("POST", "/api/v1/devices/dev-fenced/ack", bytes.NewReader(ackBody))
	ackReq.Header.Set("Authorization", "Bearer "+devToken)
	wAck := httptest.NewRecorder()
	handler.ServeHTTP(wAck, ackReq)
	if wAck.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on progress ACK, got %d: %s", wAck.Code, wAck.Body.String())
	}

	// Verify command progress updated but command not terminal
	updatedCmd, _ := cmdSvc.Get(cmd.ID)
	if updatedCmd.Terminal() {
		t.Fatal("command should not be terminal after progress ACK")
	}
	if updatedCmd.Progress == nil || updatedCmd.Progress.Phase != "downloading" {
		t.Fatalf("unexpected command progress: %+v", updatedCmd.Progress)
	}

	// 3. PUT facts with Fenced token -> receives prepared confirmation
	tokenStr, _, err := upgrade.GenerateFenceToken()
	if err != nil {
		t.Fatal(err)
	}
	fenceRev := uint64(10)
	factsBody, _ := json.Marshal(map[string]any{
		"hostname":                  "mac-fenced",
		"agent_version":             "v0.7.0",
		"os":                        "darwin",
		"arch":                      "arm64",
		"ssh_user":                  "roki",
		"ssh_port":                  22,
		"addresses":                 []string{"192.168.1.100"},
		"control_protocols":         []int{1, 2},
		"command_id":                string(cmd.ID),
		"upgrade_transaction_id":    "tx-999",
		"upgrade_fence_revision":    fenceRev,
		"upgrade_fence_token":       tokenStr,
		"confirmed_manifest_digest": strings.Repeat("a", 64),
		"running_bundle_digest":     strings.Repeat("b", 64),
		"upgrade_security_mode":     "v2_locked",
	})
	factsReq := httptest.NewRequest("PUT", "/api/v1/devices/dev-fenced/facts", bytes.NewReader(factsBody))
	factsReq.Header.Set("Authorization", "Bearer "+devToken)
	wFacts := httptest.NewRecorder()
	handler.ServeHTTP(wFacts, factsReq)
	if wFacts.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on facts PUT, got %d: %s", wFacts.Code, wFacts.Body.String())
	}
	var factsResp map[string]any
	if err := json.NewDecoder(wFacts.Body).Decode(&factsResp); err != nil {
		t.Fatal(err)
	}
	confMap, ok := factsResp["upgrade_confirmation"].(map[string]any)
	if !ok || confMap["state"] != "prepared" {
		t.Fatalf("expected prepared confirmation, got %+v", factsResp)
	}
	serverNonce, _ := confMap["server_nonce"].(string)
	fenceDigest, _ := confMap["fence_digest"].(string)
	factsDigest, _ := confMap["facts_digest"].(string)

	// 4. Send commit_ready progress ACK -> finishes command as succeeded and returns committed confirmation
	commitResult, _ := json.Marshal(map[string]string{
		"fence_digest": fenceDigest,
		"facts_digest": factsDigest,
		"server_nonce": serverNonce,
	})
	seq2 := uint64(2)
	commitAckBody, _ := json.Marshal(AckRequest{
		CommandID:   cmd.ID,
		Module:      "upgrade",
		Status:      "progress",
		Phase:       "commit_ready",
		Sequence:    &seq2,
		PhaseResult: commitResult,
	})
	commitReq := httptest.NewRequest("POST", "/api/v1/devices/dev-fenced/ack", bytes.NewReader(commitAckBody))
	commitReq.Header.Set("Authorization", "Bearer "+devToken)
	wCommit := httptest.NewRecorder()
	handler.ServeHTTP(wCommit, commitReq)
	if wCommit.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on commit_ready ACK, got %d: %s", wCommit.Code, wCommit.Body.String())
	}
	var commitResp map[string]any
	if err := json.NewDecoder(wCommit.Body).Decode(&commitResp); err != nil {
		t.Fatal(err)
	}
	commitConf, ok := commitResp["upgrade_confirmation"].(map[string]any)
	if !ok || commitConf["state"] != "committed" {
		t.Fatalf("expected committed confirmation, got %+v", commitResp)
	}

	// Verify command is now succeeded
	finalCmd, _ := cmdSvc.Get(cmd.ID)
	if finalCmd.Status != command.StatusSucceeded {
		t.Fatalf("expected command StatusSucceeded, got %s", finalCmd.Status)
	}
}

func TestUpgradeDevice_GitHubSourceAndSHA256Resolution(t *testing.T) {
	rawHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/RokiLai/home-agent/releases/download/v0.7.0/homeagent-agent-linux-amd64.sha256":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "%s  homeagent-agent-linux-amd64\n", rawHash)
		case "/RokiLai/home-agent/releases/download/v0.7.0/homeagent-agent-darwin-arm64.sha256":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "%s\n", rawHash)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockGitHub.Close()

	reg, _ := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	d := device.Device{
		ID:               "dev-gh",
		Hostname:         "host-gh",
		MAC:              "12:22:33:44:55:66",
		OS:               "linux",
		Arch:             "amd64",
		SSHUser:          "root",
		SSHPort:          22,
		PublicKey:        "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleValidKey",
		ControlProtocols: []int{1},
	}
	if _, err := reg.Save(d); err != nil {
		t.Fatal(err)
	}

	b := broker.New()
	cmdRepo, _ := commandfile.Open(filepath.Join(t.TempDir(), "commands.json"))
	cmdSvc := command.NewService(cmdRepo, nil)

	ghClient := githubrelease.NewClient(githubrelease.Config{
		Repo:            "RokiLai/home-agent",
		DownloadBaseURL: mockGitHub.URL,
	})

	s := &Server{
		Registry:            reg,
		Broker:              b,
		Commands:            cmdSvc,
		UpgradePlans:        upgradeplan.NewService(),
		Token:               "admin-tok",
		UpgradeSource:       "github",
		GitHubReleaseClient: ghClient,
		Log:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	handler := s.Handler()

	// Connect SSE listener
	eventsReq := httptest.NewRequest("GET", "/api/v1/devices/dev-gh/events", nil)
	eventsReq.Header.Set("Authorization", "Bearer admin-tok")
	wEvents := httptest.NewRecorder()
	go handler.ServeHTTP(wEvents, eventsReq)
	time.Sleep(50 * time.Millisecond)

	// 1. Trigger upgrade without custom url/sha -> should resolve GitHub release URL and fetch sha256
	upgradeReq := httptest.NewRequest("POST", "/api/v1/devices/dev-gh/upgrade", strings.NewReader(`{"target_version":"v0.7.0"}`))
	upgradeReq.Header.Set("Authorization", "Bearer admin-tok")
	upgradeReq.Header.Set("Content-Type", "application/json")
	wUpgrade := httptest.NewRecorder()
	handler.ServeHTTP(wUpgrade, upgradeReq)

	if wUpgrade.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from upgrade, got %d: %s", wUpgrade.Code, wUpgrade.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(wUpgrade.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	expectedURL := mockGitHub.URL + "/RokiLai/home-agent/releases/download/v0.7.0/homeagent-agent-linux-amd64"
	if resp["url"] != expectedURL {
		t.Fatalf("expected URL %s, got %v", expectedURL, resp["url"])
	}
	if resp["sha256"] != rawHash {
		t.Fatalf("expected SHA256 %s, got %v", rawHash, resp["sha256"])
	}

	// 2. Negative test: target version whose sha256 is missing on GitHub -> should fail 400
	failReq := httptest.NewRequest("POST", "/api/v1/devices/dev-gh/upgrade", strings.NewReader(`{"target_version":"v9.9.9"}`))
	failReq.Header.Set("Authorization", "Bearer admin-tok")
	failReq.Header.Set("Content-Type", "application/json")
	wFail := httptest.NewRecorder()
	handler.ServeHTTP(wFail, failReq)

	if wFail.Code == http.StatusOK {
		t.Fatalf("expected failure for non-existent release, got 200: %s", wFail.Body.String())
	}
}

func TestSystemVersionCheck_Endpoint(t *testing.T) {
	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/RokiLai/home-agent/releases/latest" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{
				"tag_name": "v99.0.0",
				"name": "Release v99.0.0",
				"body": "Breaking update",
				"published_at": "2026-09-01T00:00:00Z",
				"html_url": "https://github.com/RokiLai/home-agent/releases/tag/v99.0.0"
			}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer mockGitHub.Close()

	ghClient := githubrelease.NewClient(githubrelease.Config{
		Repo:    "RokiLai/home-agent",
		APIBase: mockGitHub.URL,
	})

	s := &Server{
		Token:               "admin-token",
		GitHubReleaseClient: ghClient,
		Log:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	handler := s.Handler()

	req := httptest.NewRequest("GET", "/api/v1/system/version-check", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}
	var res map[string]any
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res["has_update"] != true || res["latest_version"] != "v99.0.0" {
		t.Fatalf("unexpected version-check response: %+v", res)
	}
}

func TestGetConfig_UpgradeSource(t *testing.T) {
	s := &Server{
		Token:              "admin-token",
		PublicURL:          "https://custom.domain",
		GitHubRepo:         "custom/repo",
		UpgradeSource:      "github",
		GitHubMirrorPrefix: "https://ghproxy.net/",
	}
	handler := s.Handler()

	req := httptest.NewRequest("GET", "/api/v1/config", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
	var res map[string]any
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res["github_repo"] != "custom/repo" || res["upgrade_source"] != "github" || res["github_mirror_prefix"] != "https://ghproxy.net/" {
		t.Fatalf("unexpected config output: %+v", res)
	}
}
