package domainbinding

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"homeagent/internal/device"
	"homeagent/internal/store"
	"homeagent/internal/store/filestore"
)

func newTestService(t *testing.T) (*Service, *store.ControlPlaneService) {
	t.Helper()
	repository, err := filestore.OpenControlPlane(filepath.Join(t.TempDir(), "control-plane.json"))
	if err != nil {
		t.Fatal(err)
	}
	control := store.NewControlPlaneService(repository)
	return NewService(control, nil), control
}

func TestCreateObservingBindingIsAtomicAndUnique(t *testing.T) {
	service, control := newTestService(t)
	if err := control.ReplaceRegistryState(context.Background(), []device.Device{{ID: "device-1", OwnerUserID: "owner-1"}}, nil); err != nil {
		t.Fatal(err)
	}
	binding, err := service.Create(context.Background(), CreateCommand{SourceType: SourceDevice, SourceID: "device-1", OwnerUserID: "user-1", FQDN: "MBP.Rokilai.Online.", ExpectedRevision: 1, ExistingRecord: true})
	if err != nil {
		t.Fatal(err)
	}
	if binding.OwnerUserID != "user-1" {
		t.Fatalf("owner_user_id=%q", binding.OwnerUserID)
	}
	if binding.ConfigState != ConfigObserving || binding.RuntimeState != RuntimeWaitingReport || binding.FQDN != "mbp.rokilai.online" {
		t.Fatalf("binding = %+v", binding)
	}
	_, err = service.Create(context.Background(), CreateCommand{SourceType: SourceServer, SourceID: LocalServerSourceID, FQDN: "mbp.rokilai.online", ExpectedRevision: binding.ControlPlaneRevision})
	if !errors.Is(err, ErrFQDNConflict) {
		t.Fatalf("duplicate err = %v", err)
	}
}

func TestEnableRequiresExplicitPublisherExclusionAndCurrentRevision(t *testing.T) {
	service, _ := newTestService(t)
	binding, err := service.Create(context.Background(), CreateCommand{SourceType: SourceServer, SourceID: LocalServerSourceID, FQDN: "srv.rokilai.online", ExpectedRevision: 0, ExistingRecord: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Enable(context.Background(), EnableCommand{BindingID: binding.BindingID, ExpectedRevision: binding.Revision, PublisherDisabledConfirmed: false})
	if !errors.Is(err, ErrPublisherConfirmationRequired) {
		t.Fatalf("missing confirmation err = %v", err)
	}
	enabled, err := service.Enable(context.Background(), EnableCommand{BindingID: binding.BindingID, ExpectedRevision: binding.Revision, PublisherDisabledConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if enabled.ConfigState != ConfigEnabled || enabled.RuntimeState != RuntimeWaitingReport {
		t.Fatalf("enabled = %+v", enabled)
	}
	_, err = service.Disable(context.Background(), enabled.BindingID, binding.Revision)
	if !errors.Is(err, ErrBindingRevisionConflict) {
		t.Fatalf("stale revision err = %v", err)
	}
}

func TestCreateMapsControlPlaneRevisionConflict(t *testing.T) {
	service, _ := newTestService(t)
	if _, err := service.Create(context.Background(), CreateCommand{SourceType: SourceServer, SourceID: LocalServerSourceID, FQDN: "first.rokilai.online", ExpectedRevision: 0, ExistingRecord: true}); err != nil {
		t.Fatal(err)
	}
	_, err := service.Create(context.Background(), CreateCommand{SourceType: SourceServer, SourceID: LocalServerSourceID, FQDN: "second.rokilai.online", ExpectedRevision: 0, ExistingRecord: true})
	if !errors.Is(err, ErrBindingRevisionConflict) {
		t.Fatalf("create error=%v, want ErrBindingRevisionConflict", err)
	}
}

func TestRecoveryQueueDropsStaleRevisionAndRestoresCurrentPendingTask(t *testing.T) {
	service, _ := newTestService(t)
	binding, err := service.Create(context.Background(), CreateCommand{SourceType: SourceServer, SourceID: LocalServerSourceID, FQDN: "queue.rokilai.online", ExpectedRevision: 0})
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.QueueDesiredAddress(context.Background(), binding.BindingID, binding.Revision, "2001:db8::10")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "pending" {
		t.Fatalf("task=%+v", task)
	}
	recovered, err := service.Recover(context.Background())
	if err != nil || len(recovered) != 1 || recovered[0].DesiredIPv6 != "2001:db8::10" {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	if _, err := service.Disable(context.Background(), binding.BindingID, binding.Revision); err != nil {
		t.Fatal(err)
	}
	recovered, err = service.Recover(context.Background())
	if err != nil || len(recovered) != 0 {
		t.Fatalf("stale recovered=%+v err=%v", recovered, err)
	}
}

type existingRecordProvider struct{}

func (existingRecordProvider) Observe(context.Context, ObserveRequest) (RecordObservation, error) {
	return RecordObservation{Exists: true, RecordID: "record-1", IPv6: "2001:db8::1", TTL: 300, Proxied: true}, nil
}
func (existingRecordProvider) Apply(context.Context, ApplyRequest) (RecordObservation, error) {
	panic("unexpected write")
}

func TestCreateRechecksProviderAndPersistsObservedRecordIdentity(t *testing.T) {
	service, _ := newTestService(t)
	service.AttachProvider(existingRecordProvider{})
	binding, err := service.Create(context.Background(), CreateCommand{SourceType: SourceServer, SourceID: LocalServerSourceID, FQDN: "existing.rokilai.online", ExpectedRevision: 0})
	if err != nil {
		t.Fatal(err)
	}
	if binding.ConfigState != ConfigObserving || binding.ProviderRecordID != "record-1" || binding.TTL != 300 || !binding.Proxied || binding.ProviderIPv6 != "2001:db8::1" {
		t.Fatalf("binding=%+v", binding)
	}
}
