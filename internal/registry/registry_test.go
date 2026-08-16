package registry

import (
	"errors"
	"homeagent/internal/device"
	"path/filepath"
	"testing"
)

func sample(id, key string) device.Device {
	return device.Device{ID: id, Hostname: id, OS: "linux", Arch: "amd64", SSHUser: "user", SSHPort: 22, PublicKey: "ssh-ed25519 " + key, Addresses: []string{"192.168.1.2"}}
}

func TestSaveUpdateDeleteAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.Save(sample("a", "AAAA"))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := r.Save(sample("a", "BBBB"))
	if err != nil {
		t.Fatal(err)
	}
	if !updated.CreatedAt.Equal(first.CreatedAt) || len(r.List()) != 1 {
		t.Fatal("update did not preserve identity")
	}
	r2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := r2.Get("a")
	if got.PublicKey != "ssh-ed25519 BBBB" {
		t.Fatalf("got %q", got.PublicKey)
	}
	if err := r2.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r2.Get("a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}
