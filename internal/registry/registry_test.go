package registry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"homeagent/internal/auth"
	"homeagent/internal/device"
	"homeagent/internal/store"
	"homeagent/internal/store/filestore"
)

func TestRegistryListsReturnStableNewestFirstOrder(t *testing.T) {
	older := time.Unix(1, 0).UTC()
	newer := time.Unix(2, 0).UTC()
	r := &Registry{
		devices: map[string]device.Device{
			"dev-a": {ID: "dev-a", OwnerUserID: "usr-owner", CreatedAt: older},
			"dev-b": {ID: "dev-b", OwnerUserID: "usr-owner", CreatedAt: newer},
			"dev-c": {ID: "dev-c", OwnerUserID: "usr-owner", CreatedAt: newer},
		},
		grants: map[string]map[string]*device.DeviceGrant{
			"dev-a": {
				"usr-a": {DeviceID: "dev-a", UserID: "usr-a", CreatedAt: older},
				"usr-b": {DeviceID: "dev-a", UserID: "usr-b", CreatedAt: newer},
				"usr-c": {DeviceID: "dev-a", UserID: "usr-c", CreatedAt: newer},
			},
		},
		userGrants: map[string]map[string]*device.DeviceGrant{
			"usr-target": {
				"dev-a": {DeviceID: "dev-a", UserID: "usr-target", CreatedAt: older},
				"dev-b": {DeviceID: "dev-b", UserID: "usr-target", CreatedAt: newer},
				"dev-c": {DeviceID: "dev-c", UserID: "usr-target", CreatedAt: newer},
			},
		},
	}

	for attempt := 0; attempt < 3; attempt++ {
		devices := r.List()
		visible := r.FilterDevicesForUser("usr-owner", true)
		grants := r.ListGrants("dev-a")
		userGrants := r.GetUserGrants("usr-target")
		for i, want := range []string{"dev-c", "dev-b", "dev-a"} {
			if devices[i].ID != want || visible[i].ID != want {
				t.Fatalf("device order at %d = %q/%q, want %q", i, devices[i].ID, visible[i].ID, want)
			}
		}
		for i, want := range []string{"usr-c", "usr-b", "usr-a"} {
			if grants[i].UserID != want {
				t.Fatalf("grants[%d] = %q, want %q", i, grants[i].UserID, want)
			}
		}
		for i, want := range []string{"dev-c", "dev-b", "dev-a"} {
			if userGrants[i].DeviceID != want {
				t.Fatalf("user grants[%d] = %q, want %q", i, userGrants[i].DeviceID, want)
			}
		}
	}
}

func TestAttachedControlPlaneIsAuthoritativeForRegistryWrites(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "devices.json")
	registry, err := Open(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := filestore.OpenControlPlane(filepath.Join(dir, "control-plane.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := store.NewControlPlaneService(repository)
	registry.AttachControlPlane(service, nil, nil)
	if _, err := registry.Save(sample("device-1", "AAAA")); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.SetGrant("device-1", "user-2", device.GrantLevelRead, "owner"); err != nil {
		t.Fatal(err)
	}
	devices, grants, err := service.RegistryState(context.Background())
	if err != nil || len(devices) != 1 || len(grants) != 1 {
		t.Fatalf("devices=%+v grants=%+v err=%v", devices, grants, err)
	}
	if _, err := Open(legacyPath); err != nil {
		t.Fatal(err)
	}
	if err := registry.Delete("device-1"); err != nil {
		t.Fatal(err)
	}
	devices, grants, err = service.RegistryState(context.Background())
	if err != nil || len(devices) != 0 || len(grants) != 0 {
		t.Fatalf("after delete devices=%+v grants=%+v err=%v", devices, grants, err)
	}
}

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

func TestSSHPortOverrideSurvivesStaleFactsAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	old := sample("ssh-port", "AAAA")
	if _, err = r.Save(old); err != nil {
		t.Fatal(err)
	}
	for _, port := range []int{1, 22, 65535, 2222} {
		got, err := r.UpdateDeviceWithSSHPort(old.ID, nil, nil, nil, &port)
		if err != nil || got.SSHPort != port || got.SSHPortReported != 22 {
			t.Fatalf("set: %+v %v", got, err)
		}
	}
	stale, _ := r.Get(old.ID)
	reported := 2022
	stale.SSHPort = reported
	got, err := r.SaveWithSSHPortReport(stale, &reported)
	if err != nil || got.SSHPort != 2222 || got.SSHPortReported != 2022 {
		t.Fatalf("facts: %+v %v", got, err)
	}
	zero := 0
	got, err = r.UpdateDeviceWithSSHPort(old.ID, nil, nil, nil, &zero)
	if err != nil || got.SSHPort != 2022 {
		t.Fatalf("restore: %+v %v", got, err)
	}
	// A request read before restore must not resurrect the prior override.
	got, err = r.SaveWithSSHPortReport(stale, nil)
	if err != nil || got.SSHPortOverride != 0 || got.SSHPort != 2022 {
		t.Fatalf("stale: %+v %v", got, err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = reopened.Get(old.ID)
	if got.SSHPort != 2022 || got.SSHPortReported != 2022 || got.SSHPortOverride != 0 {
		t.Fatalf("restart: %+v", got)
	}
	bad := -1
	alias := "must not persist"
	if _, err = r.UpdateDeviceWithSSHPort(old.ID, &alias, nil, nil, &bad); err == nil {
		t.Fatal("bad port accepted")
	}
	after, _ := r.Get(old.ID)
	if after.Alias != got.Alias || !after.UpdatedAt.Equal(got.UpdatedAt) {
		t.Fatal("partial invalid write")
	}
}

func TestSSHPortConcurrentSettingsAndFailureRollback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devices.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d := sample("parallel-port", "AAAA")
	if _, err = r.Save(d); err != nil {
		t.Fatal(err)
	}
	other := sample("other-port", "BBBB")
	if _, err = r.Save(other); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			value := 2200 + i
			if i%2 == 0 {
				if _, err := r.UpdateDeviceWithSSHPort(d.ID, nil, nil, nil, &value); err != nil {
					t.Error(err)
				}
			} else {
				if _, err := r.SaveWithSSHPortReport(d, &value); err != nil {
					t.Error(err)
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()
	got, _ := r.Get(d.ID)
	if got.SSHPort != got.SSHPortOverride || got.SSHPortReported < 2200 || got.SSHPortReported > 2219 {
		t.Fatalf("concurrency: %+v", got)
	}
	second, _ := r.Get(other.ID)
	if second.SSHPort != 22 || second.SSHPortOverride != 0 {
		t.Fatal("cross-device write")
	}
	// Force a real filesystem persistence failure and verify all properties roll back.
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	port := 3333
	alias := "must rollback"
	if _, err = r.UpdateDeviceWithSSHPort(d.ID, &alias, nil, nil, &port); err == nil {
		t.Fatal("persistence failure hidden")
	}
	after, _ := r.Get(d.ID)
	if after.SSHPort != got.SSHPort || after.SSHPortReported != got.SSHPortReported || after.SSHPortOverride != got.SSHPortOverride || after.Alias != got.Alias || after.UpdatedAt != got.UpdatedAt {
		t.Fatal("rollback failed")
	}
}

func TestSSHPortLegacyRegistryMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.json")
	data := []byte(`{"devices":[{"id":"legacy-port","hostname":"host","ssh_user":"admin","ssh_port":2222,"public_key":"ssh-ed25519 AAAA"}]}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Get("legacy-port")
	if err != nil || got.SSHPort != 2222 || got.SSHPortReported != 2222 || got.SSHPortOverride != 0 {
		t.Fatalf("legacy: %+v %v", got, err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := reopened.Get("legacy-port")
	if again.SSHPortReported != 2222 || again.SSHPortOverride != 0 {
		t.Fatal("legacy migration did not survive restart")
	}
}
