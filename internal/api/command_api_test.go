package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"homeagent/internal/broker"
	"homeagent/internal/command"
	commandfile "homeagent/internal/command/file"
	"homeagent/internal/device"
	"homeagent/internal/registry"
)

func TestShutdownCommandLifecycle(t *testing.T) {
	dir := t.TempDir()
	reg, err := registry.Open(filepath.Join(dir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = reg.Save(device.Device{ID: "dev-1", Hostname: "host", OS: "linux", Arch: "amd64", SSHUser: "u", SSHPort: 22, PublicKey: "ssh-ed25519 AAAA", ControlProtocols: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := commandfile.Open(filepath.Join(dir, "commands.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := command.NewService(repo, nil)
	b := broker.New()
	ch, unsub := b.Subscribe("dev-1")
	defer unsub()
	s := &Server{Registry: reg, Broker: b, Token: "secret", Commands: svc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.Handler()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/dev-1/shutdown", bytes.NewBufferString(`{"reason":"test"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Idempotency-Key", "shutdown-once")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("trigger: %d %s", w.Code, w.Body.String())
	}
	var response map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	id, ok := response["command_id"].(string)
	if !ok || id == "" {
		t.Fatalf("missing command id: %v", response)
	}
	ev := <-ch
	if ev.ID != id {
		t.Fatalf("event id %q != %q", ev.ID, id)
	}
	var payload map[string]any
	_ = json.Unmarshal([]byte(ev.Data), &payload)
	if payload["command_id"] != id || payload["ack_mode"] != "two_phase" {
		t.Fatalf("wire payload: %v", payload)
	}
	retry := httptest.NewRequest(http.MethodPost, "/api/v1/devices/dev-1/shutdown", bytes.NewBufferString(`{"reason":"test"}`))
	retry.Header.Set("Authorization", "Bearer secret")
	retry.Header.Set("Idempotency-Key", "shutdown-once")
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, retry)
	if rw.Code != 200 {
		t.Fatalf("retry: %d %s", rw.Code, rw.Body.String())
	}
	var retryResponse map[string]any
	_ = json.Unmarshal(rw.Body.Bytes(), &retryResponse)
	if retryResponse["command_id"] != id {
		t.Fatalf("retry created another command: %v", retryResponse)
	}
	select {
	case duplicate := <-ch:
		t.Fatalf("idempotent retry redelivered event: %#v", duplicate)
	default:
	}
	for _, status := range []string{"accepted", "succeeded"} {
		body := bytes.NewBufferString(`{"command_id":"` + id + `","protocol":1,"ack_mode":"two_phase","module":"shutdown","status":"` + status + `"}`)
		ack := httptest.NewRequest(http.MethodPost, "/api/v1/devices/dev-1/ack", body)
		ack.Header.Set("Authorization", "Bearer secret")
		aw := httptest.NewRecorder()
		h.ServeHTTP(aw, ack)
		if aw.Code != 200 {
			t.Fatalf("ack %s: %d %s", status, aw.Code, aw.Body.String())
		}
	}
	got, err := svc.Get(command.ID(id))
	if err != nil || got.Status != command.StatusSucceeded {
		t.Fatalf("final: %#v %v", got, err)
	}
	if got.Projection.Status != "applied" {
		t.Fatalf("projection not applied: %#v", got.Projection)
	}
}
