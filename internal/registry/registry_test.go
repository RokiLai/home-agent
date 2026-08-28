package registry

import (
	"errors"
	"path/filepath"
	"testing"

	"homeagent/internal/auth"
	"homeagent/internal/device"
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

func TestRegistry_OwnerAndGrants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices_grants.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	// 1. 设置 DefaultOwnerID，新建设备自动绑定
	r.SetDefaultOwnerID("usr_owner_default")
	d1, err := r.Save(sample("dev1", "AAAA"))
	if err != nil {
		t.Fatal(err)
	}
	if d1.OwnerUserID != "usr_owner_default" {
		t.Fatalf("Expected OwnerUserID 'usr_owner_default', got %q", d1.OwnerUserID)
	}

	// 2. 为设备所有者添加 Grant 必须失败 (ErrGrantToOwner)
	if _, err := r.SetGrant("dev1", "usr_owner_default", device.GrantLevelOperate, "admin"); err != device.ErrGrantToOwner {
		t.Fatalf("Expected ErrGrantToOwner, got: %v", err)
	}

	// 3. 为用户 Bob 添加 operate 级别授权
	g1, err := r.SetGrant("dev1", "usr_bob", device.GrantLevelOperate, "usr_owner_default")
	if err != nil {
		t.Fatalf("SetGrant failed: %v", err)
	}
	if g1.Level != device.GrantLevelOperate || g1.UserID != "usr_bob" {
		t.Fatalf("Unexpected grant data: %+v", g1)
	}

	// 4. 重载验证持久化
	r2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	grants := r2.ListGrants("dev1")
	if len(grants) != 1 || grants[0].Level != device.GrantLevelOperate {
		t.Fatalf("Expected 1 operate grant after reload, got: %+v", grants)
	}

	// 5. 验证 DeviceScopeResolver 接口实现
	// a. Bob 可见但非 Owner
	visible, exists := r2.IsDeviceVisible("usr_bob", "dev1")
	if !visible || !exists {
		t.Fatalf("Bob should see dev1: visible=%v, exists=%v", visible, exists)
	}
	if r2.IsDeviceOwner("usr_bob", "dev1") {
		t.Fatal("Bob is not device owner")
	}
	// b. Bob 有 operate 权限，但无 manage 权限
	if !r2.HasDevicePermission("usr_bob", "dev1", auth.PermDevicesShutdown) {
		t.Fatal("Bob should have shutdown permission")
	}
	if r2.HasDevicePermission("usr_bob", "dev1", auth.PermDevicesUpdate) {
		t.Fatal("Bob should NOT have update permission with operate grant")
	}

	// 6. 所有权转移：将 dev1 转移给 Bob，并为原 Owner 保留 read 权限
	retainRead := device.GrantLevelRead
	if err := r2.TransferOwnership("dev1", "usr_bob", "usr_owner_default", &retainRead); err != nil {
		t.Fatalf("TransferOwnership failed: %v", err)
	}

	dev1After, _ := r2.Get("dev1")
	if dev1After.OwnerUserID != "usr_bob" {
		t.Fatalf("Expected new owner 'usr_bob', got %q", dev1After.OwnerUserID)
	}
	if !r2.IsDeviceOwner("usr_bob", "dev1") {
		t.Fatal("Bob should now be device owner")
	}
	// 原 Owner 变为 read 授权
	if r2.IsDeviceOwner("usr_owner_default", "dev1") {
		t.Fatal("Old owner should no longer be owner")
	}
	if !r2.HasDevicePermission("usr_owner_default", "dev1", auth.PermDevicesRead) {
		t.Fatal("Old owner should have read permission")
	}
	if r2.HasDevicePermission("usr_owner_default", "dev1", auth.PermDevicesShutdown) {
		t.Fatal("Old owner should NOT have shutdown permission after downgrade to read")
	}

	// 7. 级联物理删除用户 Bob 名下的所有设备
	deletedIDs, err := r2.DeleteDevicesByOwner("usr_bob")
	if err != nil {
		t.Fatalf("DeleteDevicesByOwner failed: %v", err)
	}
	if len(deletedIDs) != 1 || deletedIDs[0] != "dev1" {
		t.Fatalf("Expected deleted ['dev1'], got: %v", deletedIDs)
	}
	if _, err := r2.Get("dev1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("dev1 should be purged after DeleteDevicesByOwner")
	}
	if len(r2.ListGrants("dev1")) != 0 {
		t.Fatal("Grants for dev1 should be purged")
	}
}
