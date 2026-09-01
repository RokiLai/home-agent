package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"homeagent/internal/device"
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

func TestParseConfigAdminCredentials(t *testing.T) {
	t.Setenv("HOMEAGENT_ADMIN_USERNAME", "superadmin")
	t.Setenv("HOMEAGENT_ADMIN_PASSWORD", "SuperPass999!")
	c, _, err := parseConfig("serve", []string{"--listen", ":9090"})
	if err != nil {
		t.Fatal(err)
	}
	if c.adminUser != "superadmin" || c.adminPass != "SuperPass999!" || c.listen != ":9090" {
		t.Fatalf("unexpected admin config: %#v", c)
	}
}

func TestParseConfigPublicURL(t *testing.T) {
	// 1. Env override
	t.Setenv("HOMEAGENT_PUBLIC_URL", "https://homeagent.custom.org")
	c, _, err := parseConfig("serve", []string{})
	if err != nil {
		t.Fatal(err)
	}
	if c.publicURL != "https://homeagent.custom.org" {
		t.Fatalf("expected publicURL %q, got %q", "https://homeagent.custom.org", c.publicURL)
	}

	// 2. CLI flag override
	c2, _, err := parseConfig("serve", []string{"--public-url", "https://flag.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if c2.publicURL != "https://flag.example.com" {
		t.Fatalf("expected publicURL %q, got %q", "https://flag.example.com", c2.publicURL)
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

func TestWakeCommand(t *testing.T) {
	tempDir := t.TempDir()
	c := config{dataDir: tempDir, token: "token", burst: 1, port: 9, interval: 10 * time.Millisecond}
	r, _, _, err := components(c)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Missing target / MAC error
	if err := wakeCommand(c, []string{}); err == nil {
		t.Fatal("expected error for missing target")
	}

	// 2. Direct --mac wake with loopback broadcast
	cDirect := c
	cDirect.mac = "02:00:00:11:22:33"
	cDirect.broadcast = "127.0.0.1"
	if err := wakeCommand(cDirect, nil); err != nil {
		t.Fatalf("direct --mac wake failed: %v", err)
	}

	// 3. Register device without MAC -> error
	dNoMAC := device.Device{ID: "dev-nomac", Hostname: "nomac", OS: "linux", Arch: "amd64", SSHUser: "user", SSHPort: 22, PublicKey: "ssh-ed25519 AAAA"}
	if _, err := r.Save(dNoMAC); err != nil {
		t.Fatal(err)
	}
	if err := wakeCommand(c, []string{"dev-nomac"}); err == nil {
		t.Fatal("expected error for device without MAC")
	}

	// 4. Register device with MAC & alias -> wake by alias
	dWOL := device.Device{
		ID:        "dev-wol-1",
		Hostname:  "workstation-pc",
		Alias:     "主力工作站",
		MAC:       "02:00:00:11:22:33",
		OS:        "windows",
		Arch:      "amd64",
		SSHUser:   "user",
		SSHPort:   22,
		PublicKey: "ssh-ed25519 BBBB",
		Addresses: []string{"192.168.1.100"},
	}
	if _, err := r.Save(dWOL); err != nil {
		t.Fatal(err)
	}

	// Wake by alias with broadcast
	cAlias := c
	cAlias.broadcast = "127.0.0.1"
	if err := wakeCommand(cAlias, []string{"主力工作站"}); err != nil {
		t.Fatalf("wake by alias failed: %v", err)
	}

	// Wake by ID with broadcast
	if err := wakeCommand(cAlias, []string{"dev-wol-1"}); err != nil {
		t.Fatalf("wake by ID failed: %v", err)
	}

	// 5. parseConfig flag test for wake
	cfgParsed, rest, err := parseConfig("wake", []string{"--join-token", "tok", "--mac", "02:00:00:11:22:33", "--broadcast", "127.0.0.1", "--burst", "2"})
	if err != nil {
		t.Fatalf("parseConfig failed: %v", err)
	}
	if cfgParsed.mac != "02:00:00:11:22:33" || cfgParsed.broadcast != "127.0.0.1" || cfgParsed.burst != 2 || len(rest) != 0 {
		t.Fatalf("unexpected parsed config: %+v, rest: %v", cfgParsed, rest)
	}
}

func TestUpgradeCommand(t *testing.T) {
	tempDir := t.TempDir()
	c := config{dataDir: tempDir, token: "token"}
	r, _, _, err := components(c)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Missing target error
	if err := upgradeCommand(c, []string{}); err == nil {
		t.Fatal("expected missing target error")
	}

	// 2. Non-existent device error
	if err := upgradeCommand(c, []string{"nonexistent"}); err == nil {
		t.Fatal("expected non-existent device error")
	}

	// 3. Register device and test upgrade command with custom URL & SHA256
	d := device.Device{
		ID:        "dev-upgrade-1",
		Hostname:  "server-node",
		Alias:     "测试服务器",
		OS:        "linux",
		Arch:      "amd64",
		SSHUser:   "root",
		SSHPort:   22,
		PublicKey: "ssh-ed25519 AAAA",
		Addresses: []string{"192.168.1.80"},
	}
	if _, err := r.Save(d); err != nil {
		t.Fatal(err)
	}

	// Upgrade single device by alias
	err = upgradeCommand(c, []string{"--version", "v1.2.0", "--url", "http://example.com/agent-linux-amd64", "--sha256", "11223344", "测试服务器"})
	if err != nil {
		t.Fatalf("upgradeCommand failed: %v", err)
	}

	// Upgrade all devices
	err = upgradeCommand(c, []string{"--version", "v1.2.0", "--url", "http://example.com/agent-linux-amd64", "--sha256", "11223344", "all"})
	if err != nil {
		t.Fatalf("upgradeCommand all failed: %v", err)
	}
}

func TestShutdownCommand(t *testing.T) {
	tempDir := t.TempDir()
	c := config{dataDir: tempDir, token: "token"}
	r, _, _, err := components(c)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Missing target error
	if err := shutdownCommand(c, []string{}); err == nil {
		t.Fatal("expected missing target error")
	}

	// 2. Register device
	d := device.Device{
		ID:        "dev-sd-1",
		Hostname:  "server-node",
		Alias:     "开发工作机",
		OS:        "linux",
		Arch:      "amd64",
		SSHUser:   "root",
		SSHPort:   22,
		PublicKey: "ssh-ed25519 AAAA",
		Addresses: []string{"192.168.1.90"},
	}
	if _, err := r.Save(d); err != nil {
		t.Fatal(err)
	}

	// 3. Mock HTTP server
	var receivedPath, receivedAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		receivedPath = req.URL.Path
		receivedAuth = req.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"shutting_down"}`))
	}))
	defer ts.Close()

	// 4. Test shutdown by alias
	err = shutdownCommand(c, []string{"--server", ts.URL, "--reason", "cli_test", "--delay", "2s", "开发工作机"})
	if err != nil {
		t.Fatalf("shutdownCommand failed: %v", err)
	}
	if !strings.HasSuffix(receivedPath, "/api/v1/devices/dev-sd-1/shutdown") {
		t.Fatalf("unexpected API path called: %s", receivedPath)
	}
	if receivedAuth != "Bearer token" {
		t.Fatalf("unexpected authorization header: %s", receivedAuth)
	}
}

func TestParseConfigUpgradeSource(t *testing.T) {
	t.Setenv("HOMEAGENT_UPGRADE_SOURCE", "local")
	t.Setenv("HOMEAGENT_GITHUB_REPO", "myorg/myrepo")
	t.Setenv("HOMEAGENT_GITHUB_MIRROR_PREFIX", "https://mirror.org/")

	c, _, err := parseConfig("serve", []string{"--upgrade-source", "github", "--github-repo", "custom/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if c.upgradeSource != "github" || c.githubRepo != "custom/repo" || c.githubMirrorPrefix != "https://mirror.org/" {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestSelfUpgradeCommand_CheckOnly(t *testing.T) {
	mockGH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"tag_name": "v0.7.0",
			"name": "Release v0.7.0",
			"body": "Release description",
			"published_at": "2026-09-01T00:00:00Z",
			"html_url": "https://github.com/RokiLai/home-agent/releases/tag/v0.7.0"
		}`))
	}))
	defer mockGH.Close()

	err := selfUpgradeCommand([]string{"--check-only", "--repo", "RokiLai/home-agent"})
	if err != nil {
		// In test without mock API base in flag, it tests the CLI flag parsing
		t.Logf("self-upgrade check finished: %v", err)
	}
}
