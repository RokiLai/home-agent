package sshsync

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"homeagent/internal/acl"
	"homeagent/internal/device"
	"homeagent/internal/registry"
)

func TestThreeDeviceAllowAllManagedBlocks(t *testing.T) {
	all := []device.Device{{ID: "a", PublicKey: "ssh-ed25519 AAAA"}, {ID: "b", PublicKey: "ssh-ed25519 BBBB"}, {ID: "c", PublicKey: "ssh-ed25519 CCCC"}}
	policy := acl.Policy{DefaultAllow: true, Devices: map[string][]string{}}
	for _, target := range all {
		keys := []Key{{DeviceID: "homeagent-admin", PublicKey: "ssh-ed25519 ADMIN"}}
		for _, allowed := range policy.Resolve(target.ID, all) {
			keys = append(keys, Key{DeviceID: allowed.ID, PublicKey: allowed.PublicKey})
		}
		got, err := UpdateManagedBlock([]byte("ssh-ed25519 PERSONAL user\n"), keys)
		if err != nil {
			t.Fatal(err)
		}
		text := string(got)
		if !strings.Contains(text, "PERSONAL user") || !strings.Contains(text, "ADMIN homeagent-admin") {
			t.Fatalf("target %s: %s", target.ID, text)
		}
		if strings.Contains(text, target.PublicKey+" "+target.ID) {
			t.Fatalf("target %s contains its own key", target.ID)
		}
		for _, allowed := range policy.Resolve(target.ID, all) {
			if !strings.Contains(text, allowed.PublicKey+" "+allowed.ID) {
				t.Fatalf("target %s missing %s", target.ID, allowed.ID)
			}
		}
	}
}

func TestOfflineDeviceFailsSafely(t *testing.T) {
	r, err := registry.Open(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Save(device.Device{ID: "offline", Hostname: "offline", OS: "linux", Arch: "amd64", SSHUser: "user", SSHPort: 22, PublicKey: "ssh-ed25519 AAAA"})
	if err != nil {
		t.Fatal(err)
	}
	c := &Controller{Registry: r, ACLPath: filepath.Join(t.TempDir(), "acl.yaml")}
	result := c.SyncDevice(context.Background(), "offline")
	if result.Error == "" || !strings.Contains(result.Error, "no reachable SSH address") {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestKnownHostName(t *testing.T) {
	if got := knownHostName("192.168.1.2", 22); got != "192.168.1.2" {
		t.Fatal(got)
	}
	if got := knownHostName("2001:db8::1", 2222); got != "[2001:db8::1]:2222" {
		t.Fatal(got)
	}
}

func TestSSHPathOptionQuotesSpacesAndSpecialCharacters(t *testing.T) {
	got, err := sshPathOption("UserKnownHostsFile", `/Users/roki/Library/Application Support/HomeAgent/ssh/known"hosts`)
	if err != nil {
		t.Fatal(err)
	}
	want := `UserKnownHostsFile="/Users/roki/Library/Application Support/HomeAgent/ssh/known\"hosts"`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if _, err := sshPathOption("UserKnownHostsFile", "bad\npath"); err == nil {
		t.Fatal("expected newline path to be rejected")
	}
}
