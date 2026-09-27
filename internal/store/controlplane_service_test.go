package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"homeagent/internal/auth"
	"homeagent/internal/device"
)

type memoryControlPlaneRepository struct {
	mu       sync.Mutex
	snapshot *ControlPlaneSnapshot
}

func newMemoryControlPlaneRepository() *memoryControlPlaneRepository {
	return &memoryControlPlaneRepository{snapshot: NewControlPlaneSnapshot()}
}

func (repository *memoryControlPlaneRepository) Load(context.Context) (*ControlPlaneSnapshot, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return cloneControlPlaneSnapshot(repository.snapshot)
}

func (repository *memoryControlPlaneRepository) Commit(_ context.Context, expected uint64, next *ControlPlaneSnapshot) (*ControlPlaneSnapshot, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.snapshot.Revision != expected {
		return nil, ErrRevisionConflict
	}
	copy, err := cloneControlPlaneSnapshot(next)
	if err != nil {
		return nil, err
	}
	copy.Revision++
	if err := ValidateControlPlaneSnapshot(copy); err != nil {
		return nil, err
	}
	repository.snapshot = copy
	return cloneControlPlaneSnapshot(copy)
}

func cloneControlPlaneSnapshot(snapshot *ControlPlaneSnapshot) (*ControlPlaneSnapshot, error) {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	var copy ControlPlaneSnapshot
	err = json.Unmarshal(data, &copy)
	return &copy, err
}

func TestClaimDeviceCommitsTokenAndDeviceAtomically(t *testing.T) {
	repository := newMemoryControlPlaneRepository()
	service := NewControlPlaneService(repository)
	raw, _, err := service.CreateClaimToken(context.Background(), time.Minute, 1, "test", "owner", "owner")
	if err != nil {
		t.Fatal(err)
	}
	candidate := device.Device{ID: "device-1", Hostname: "host", OS: "linux", Arch: "amd64", SSHUser: "root", SSHPort: 22, PublicKey: "ssh-ed25519 AAAA"}
	saved, err := service.ClaimDevice(context.Background(), raw, "dev_secret", candidate)
	if err != nil || saved.OwnerUserID != "owner" {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	snapshot, _ := repository.Load(context.Background())
	if snapshot.Devices["device-1"] == nil || len(snapshot.ClaimTokens) != 0 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if _, err := service.ClaimDevice(context.Background(), raw, "second", candidate); !errors.Is(err, auth.ErrClaimTokenNotFound) {
		t.Fatalf("reused token error=%v", err)
	}
}

func TestClaimDeviceValidationFailureDoesNotConsumeToken(t *testing.T) {
	repository := newMemoryControlPlaneRepository()
	service := NewControlPlaneService(repository)
	raw, _, err := service.CreateClaimToken(context.Background(), time.Minute, 1, "test", "owner", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ClaimDevice(context.Background(), raw, "secret", device.Device{ID: "invalid"}); err == nil {
		t.Fatal("invalid device accepted")
	}
	tokens, err := service.ListClaimTokens(context.Background())
	if err != nil || len(tokens) != 1 || tokens[0].RemainingUses != 1 {
		t.Fatalf("tokens=%+v err=%v", tokens, err)
	}
}

func TestControlPlaneServiceImportsListsAndRevokesLegacyState(t *testing.T) {
	repository := newMemoryControlPlaneRepository()
	service := NewControlPlaneService(repository)
	now := time.Now().UTC()
	legacyToken := &auth.ClaimToken{ID: "claim_legacy", TokenHash: auth.HashToken("legacy"), CreatedAt: now, ExpiresAt: now.Add(time.Hour), MaxUses: 2, RemainingUses: 2, OwnerUserID: "owner"}
	legacyDevice := device.Device{ID: "legacy-device", Hostname: "legacy"}
	if err := service.ImportLegacy(context.Background(), []device.Device{legacyDevice}, nil, []*auth.ClaimToken{legacyToken}); err != nil {
		t.Fatal(err)
	}
	devices, err := service.Devices(context.Background())
	if err != nil || len(devices) != 1 || devices[0].ID != legacyDevice.ID {
		t.Fatalf("devices=%+v err=%v", devices, err)
	}
	tokens, err := service.ListClaimTokens(context.Background())
	if err != nil || len(tokens) != 1 || tokens[0].TokenHash != "" {
		t.Fatalf("tokens=%+v err=%v", tokens, err)
	}
	if err := service.RevokeClaimToken(context.Background(), "claim_legacy"); err != nil {
		t.Fatal(err)
	}
	tokens, err = service.ListClaimTokens(context.Background())
	if err != nil || len(tokens) != 0 {
		t.Fatalf("tokens after revoke=%+v err=%v", tokens, err)
	}
}

func TestClaimDeviceRecoversOfflineHardwareAndRotatesCredential(t *testing.T) {
	repository := newMemoryControlPlaneRepository()
	service := NewControlPlaneService(repository)
	service.SetRecoveryOfflineAfter(time.Minute)
	fingerprint := &device.HardwareFingerprint{Version: 1, Source: "io_platform_uuid", Fingerprint: "fingerprint-1"}
	existing := device.Device{ID: "stable-device", OwnerUserID: "owner", Hostname: "old", Alias: "Living Room", OS: "darwin", Arch: "arm64", SSHUser: "user", SSHPort: 22, PublicKey: "ssh-ed25519 OLD", DeviceTokenHash: auth.HashToken("old-token"), HardwareIdentity: fingerprint, CreatedAt: time.Now().Add(-time.Hour), LastSeenAt: time.Now().Add(-time.Hour)}
	if err := service.ImportLegacy(context.Background(), []device.Device{existing}, nil, nil); err != nil {
		t.Fatal(err)
	}
	raw, _, err := service.CreateClaimToken(context.Background(), time.Minute, 1, "recover", "owner", "owner")
	if err != nil {
		t.Fatal(err)
	}
	candidate := device.Device{ID: "temporary", Hostname: "new", OS: "darwin", Arch: "arm64", SSHUser: "user", SSHPort: 22, PublicKey: "ssh-ed25519 NEW", HardwareIdentity: fingerprint}
	saved, err := service.ClaimDevice(context.Background(), raw, "new-token", candidate)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID != existing.ID || saved.Alias != existing.Alias || saved.DeviceTokenHash != auth.HashToken("new-token") {
		t.Fatalf("recovered device=%+v", saved)
	}
	snapshot, _ := repository.Load(context.Background())
	if len(snapshot.Devices) != 1 || snapshot.Devices[existing.ID].DeviceTokenHash == existing.DeviceTokenHash {
		t.Fatalf("snapshot=%+v", snapshot.Devices)
	}
}

func TestClaimDeviceRejectsUnsafeHardwareRecoveryWithoutConsumingToken(t *testing.T) {
	for _, test := range []struct {
		name       string
		owner      string
		lastSeenAt time.Time
		want       error
	}{
		{name: "other owner", owner: "other", lastSeenAt: time.Now().Add(-time.Hour), want: ErrHardwareOwnerMismatch},
		{name: "online", owner: "owner", lastSeenAt: time.Now(), want: ErrHardwareDeviceOnline},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newMemoryControlPlaneRepository()
			service := NewControlPlaneService(repository)
			fingerprint := &device.HardwareFingerprint{Version: 1, Source: "io_platform_uuid", Fingerprint: "same"}
			existing := device.Device{ID: "existing", OwnerUserID: test.owner, Hostname: "old", HardwareIdentity: fingerprint, LastSeenAt: test.lastSeenAt}
			if err := service.ImportLegacy(context.Background(), []device.Device{existing}, nil, nil); err != nil {
				t.Fatal(err)
			}
			raw, _, _ := service.CreateClaimToken(context.Background(), time.Minute, 1, "recover", "owner", "owner")
			candidate := device.Device{ID: "new", Hostname: "new", OS: "darwin", Arch: "arm64", SSHUser: "user", SSHPort: 22, PublicKey: "ssh-ed25519 NEW", HardwareIdentity: fingerprint}
			if _, err := service.ClaimDevice(context.Background(), raw, "new-token", candidate); !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			tokens, _ := service.ListClaimTokens(context.Background())
			if len(tokens) != 1 || tokens[0].RemainingUses != 1 {
				t.Fatalf("token consumed: %+v", tokens)
			}
		})
	}
}

func TestDomainBindingReconcileLifecycleAndRecovery(t *testing.T) {
	repository := newMemoryControlPlaneRepository()
	service := NewControlPlaneService(repository)
	now := time.Now().UTC().Add(-time.Minute)
	binding := DomainBinding{BindingID: "binding-1", SourceType: "server", SourceID: "local-server", OwnerUserID: "owner", FQDN: "host.rokilai.online", ConfigState: "enabled", RuntimeState: "waiting_report", Revision: 1, UpdatedAt: now}
	created, revision, err := service.CreateDomainBinding(context.Background(), 0, binding)
	if err != nil || created.OwnerUserID != "owner" || revision != 1 {
		t.Fatalf("created=%+v revision=%d err=%v", created, revision, err)
	}
	if _, _, err := service.CreateDomainBinding(context.Background(), revision, DomainBinding{BindingID: "binding-2", FQDN: binding.FQDN, Revision: 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate error=%v", err)
	}
	bindings, listedRevision, err := service.ListDomainBindings(context.Background())
	if err != nil || len(bindings) != 1 || listedRevision != revision {
		t.Fatalf("bindings=%+v revision=%d err=%v", bindings, listedRevision, err)
	}
	task, err := service.QueueReconcileTask(context.Background(), binding.BindingID, binding.Revision, "2001:db8::1", now)
	if err != nil || task.Status != "pending" {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	same, err := service.QueueReconcileTask(context.Background(), binding.BindingID, binding.Revision, "2001:db8::1", now)
	if err != nil || same.TaskID != task.TaskID {
		t.Fatalf("same=%+v err=%v", same, err)
	}
	syncing, err := service.MarkReconcileTaskSyncing(context.Background(), task.TaskID)
	if err != nil || syncing.Status != "syncing" {
		t.Fatalf("syncing=%+v err=%v", syncing, err)
	}
	if err := service.CompleteReconcileTask(context.Background(), task.TaskID, "record-1", "2001:db8::1", 120, false, "upstream", "temporary", true, now); err != nil {
		t.Fatal(err)
	}
	recovered, err := service.RecoverReconcileTasks(context.Background())
	if err != nil || len(recovered) != 1 || recovered[0].Attempts != 1 || recovered[0].Status != "pending" {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	if _, err := service.MarkReconcileTaskSyncing(context.Background(), task.TaskID); err != nil {
		t.Fatal(err)
	}
	completedAt := time.Now().UTC()
	if err := service.CompleteReconcileTask(context.Background(), task.TaskID, "record-1", "2001:db8::1", 120, false, "", "", false, completedAt); err != nil {
		t.Fatal(err)
	}
	bindings, _, err = service.ListDomainBindings(context.Background())
	if err != nil || len(bindings) != 1 || bindings[0].RuntimeState != "synced" || bindings[0].LastAppliedIPv6 != "2001:db8::1" || bindings[0].LastSyncedAt == nil {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	observed, err := service.UpdateDomainBindingObservation(context.Background(), binding.BindingID, binding.Revision, "record-1", "2001:db8::2", 120, false, "stale", "source_unavailable", completedAt)
	if err != nil || observed.ProviderIPv6 != "2001:db8::2" || observed.RuntimeState != "stale" {
		t.Fatalf("observed=%+v err=%v", observed, err)
	}
	disabled, _, err := service.TransitionDomainBinding(context.Background(), binding.BindingID, binding.Revision, "disabled", "waiting_report", completedAt)
	if err != nil || disabled.ConfigState != "disabled" || disabled.Revision != 2 {
		t.Fatalf("disabled=%+v err=%v", disabled, err)
	}
}
