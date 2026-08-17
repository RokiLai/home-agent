package main

import (
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

