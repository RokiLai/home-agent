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

func TestUpdateSyncStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Save(sample("dev1", "AAAA")); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateSyncStatus("dev1", "synced", 5, "hash123", ""); err != nil {
		t.Fatal(err)
	}
	d, err := r.Get("dev1")
	if err != nil {
		t.Fatal(err)
	}
	if d.SyncStatus != "synced" || d.AppliedVersion != 5 || d.AppliedHash != "hash123" || d.SyncError != "" {
		t.Fatalf("unexpected sync status: %+v", d)
	}
	if d.SyncUpdatedAt.IsZero() {
		t.Fatal("SyncUpdatedAt should not be zero")
	}
	if err := r.UpdateSyncStatus("nonexistent", "synced", 1, "h", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateAliasAndPreserveOnSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d1, err := r.Save(sample("dev1", "AAAA"))
	if err != nil {
		t.Fatal(err)
	}
	if d1.Alias != "" {
		t.Fatalf("expected empty alias, got %q", d1.Alias)
	}

	// 1. Update Alias
	updated, err := r.UpdateAlias("dev1", "客厅软路由")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Alias != "客厅软路由" {
		t.Fatalf("expected alias '客厅软路由', got %q", updated.Alias)
	}

	// Reopen from disk to verify persistence
	r2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r2.Get("dev1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Alias != "客厅软路由" {
		t.Fatalf("expected persisted alias '客厅软路由', got %q", got.Alias)
	}

	// 2. Client re-registers with empty Alias -> Server should preserve existing alias
	reRegistered, err := r2.Save(sample("dev1", "AAAA_NEW"))
	if err != nil {
		t.Fatal(err)
	}
	if reRegistered.Alias != "客厅软路由" {
		t.Fatalf("expected preserved alias '客厅软路由', got %q", reRegistered.Alias)
	}
	if reRegistered.PublicKey != "ssh-ed25519 AAAA_NEW" {
		t.Fatalf("expected updated public key, got %q", reRegistered.PublicKey)
	}

	// 3. Updating non-existent device returns ErrNotFound
	if _, err := r2.UpdateAlias("dev-none", "test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateMACAndPreserveOnSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	dev := sample("dev1", "AAAA")
	dev.MAC = "02-00-00-11-22-33" // format with hyphens

	d1, err := r.Save(dev)
	if err != nil {
		t.Fatal(err)
	}
	if d1.MAC != "02:00:00:11:22:33" {
		t.Fatalf("expected normalized MAC, got %q", d1.MAC)
	}

	// 1. Update MAC directly
	updated, err := r.UpdateMAC("dev1", "02:11:22:33:44:55")
	if err != nil {
		t.Fatal(err)
	}
	if updated.MAC != "02:11:22:33:44:55" {
		t.Fatalf("expected updated MAC, got %q", updated.MAC)
	}

	// 2. Client re-registers with empty MAC -> Server must preserve existing MAC
	devNew := sample("dev1", "AAAA_NEW")
	devNew.MAC = ""
	saved, err := r.Save(devNew)
	if err != nil {
		t.Fatal(err)
	}
	if saved.MAC != "02:11:22:33:44:55" {
		t.Fatalf("expected preserved MAC '02:11:22:33:44:55', got %q", saved.MAC)
	}

	// 3. UpdateDevice both alias and MAC
	alias := "新工作站"
	mac := "02:22:33:44:55:66"
	updatedDev, err := r.UpdateDevice("dev1", &alias, &mac, nil)
	if err != nil {
		t.Fatal(err)
	}
	if updatedDev.Alias != "新工作站" || updatedDev.MAC != "02:22:33:44:55:66" {
		t.Fatalf("unexpected updated dev: %+v", updatedDev)
	}

	// 4. Update non-existent device returns ErrNotFound
	if _, err := r.UpdateMAC("nonexistent", "02:11:22:33:44:55"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := r.UpdateDevice("nonexistent", &alias, &mac, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateGitHubSyncAndPreserveOnSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	dev := sample("dev1", "AAAA")
	d1, err := r.Save(dev)
	if err != nil {
		t.Fatal(err)
	}
	if d1.GitHubSyncEnabled {
		t.Fatalf("expected GitHubSyncEnabled to be false initially")
	}

	// 1. Enable GitHub Sync
	updated, err := r.UpdateGitHubSyncEnabled("dev1", true)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.GitHubSyncEnabled {
		t.Fatalf("expected GitHubSyncEnabled to be true")
	}

	// 2. Update GitHub Status
	if err := r.UpdateGitHubStatus("dev1", "synced", 9988, "SHA256:abcd"); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Get("dev1")
	if got.GitHubStatus != "synced" || got.GitHubKeyID != 9988 || got.GitHubFingerprint != "SHA256:abcd" {
		t.Fatalf("unexpected github status: %+v", got)
	}

	// 3. Re-save should preserve github settings
	devNew := sample("dev1", "AAAA_NEW")
	devNew.GitHubSyncEnabled = false
	saved, err := r.Save(devNew)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.GitHubSyncEnabled || saved.GitHubKeyID != 9988 {
		t.Fatalf("expected preserved github settings, got: %+v", saved)
	}
}

func TestTouchLastSeen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Save(sample("dev-seen", "AAAA")); err != nil {
		t.Fatal(err)
	}
	initial, _ := r.Get("dev-seen")

	if err := r.TouchLastSeen("dev-seen"); err != nil {
		t.Fatalf("TouchLastSeen failed: %v", err)
	}
	after, err := r.Get("dev-seen")
	if err != nil {
		t.Fatal(err)
	}
	if after.LastSeenAt.Before(initial.LastSeenAt) {
		t.Fatalf("expected LastSeenAt >= initial, got %v vs %v", after.LastSeenAt, initial.LastSeenAt)
	}

	if err := r.TouchLastSeen("non-existent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
