package main

import (
	"homeagent/internal/device"
	"testing"
	"time"
)

func TestParseConfig(t *testing.T) {
	t.Setenv("HOMEAGENT_JOIN_TOKEN", "secret")
	t.Setenv("HOMEAGENT_DATA_DIR", "/tmp/homeagent-test")
	c, rest, err := parseConfig("sync", []string{"--ssh-timeout", "2s", "device-a"})
	if err != nil {
		t.Fatal(err)
	}
	if c.token != "secret" || c.dataDir != "/tmp/homeagent-test" || c.timeout != 2*time.Second || len(rest) != 1 || rest[0] != "device-a" {
		t.Fatalf("unexpected config: %#v %#v", c, rest)
	}
}

func TestParseConfigRequiresToken(t *testing.T) {
	t.Setenv("HOMEAGENT_JOIN_TOKEN", "")
	if _, _, err := parseConfig("serve", nil); err == nil {
		t.Fatal("expected missing token error")
	}
}

func TestListDevices(t *testing.T) {
	tempDir := t.TempDir()
	c := config{dataDir: tempDir, token: "token"}
	if err := list(c); err != nil {
		t.Fatal(err)
	}
}

func TestRenameCommand(t *testing.T) {
	tempDir := t.TempDir()
	c := config{dataDir: tempDir, token: "token"}
	r, _, _, err := components(c)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Missing args error
	if err := renameCommand(c, []string{"dev1"}); err == nil {
		t.Fatal("expected missing alias error")
	}

	// 2. Non-existent device error
	if err := renameCommand(c, []string{"dev1", "客厅软路由"}); err == nil {
		t.Fatal("expected non-existent device error")
	}

	// 3. Register device and rename
	d := device.Device{ID: "dev1", Hostname: "dev1", OS: "linux", Arch: "amd64", SSHUser: "user", SSHPort: 22, PublicKey: "ssh-ed25519 AAAA"}
	if _, err := r.Save(d); err != nil {
		t.Fatal(err)
	}

	if err := renameCommand(c, []string{"dev1", "客厅软路由"}); err != nil {
		t.Fatalf("rename failed: %v", err)
	}

	r2, _, _, err := components(c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r2.Get("dev1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Alias != "客厅软路由" {
		t.Fatalf("expected alias '客厅软路由', got %q", got.Alias)
	}
}



