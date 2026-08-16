package api

import (
	"bytes"
	"encoding/json"
	"homeagent/internal/device"
	"homeagent/internal/registry"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestRegisterListDelete(t *testing.T) {
	r, _ := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	s := &Server{Registry: r, Token: "secret", AdminPublicKey: "ssh-ed25519 ADMIN", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.Handler()
	d := device.Device{ID: "a", Hostname: "a", OS: "linux", Arch: "amd64", SSHUser: "u", SSHPort: 22, PublicKey: "ssh-ed25519 AAAA", Addresses: []string{"192.168.1.2"}}
	b, _ := json.Marshal(d)
	req := httptest.NewRequest("POST", "/api/v1/devices/register", bytes.NewReader(b))
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
