package domainbinding

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"homeagent/internal/store"
	"homeagent/internal/store/filestore"
)

type fixedResolver struct {
	address string
	err     error
}

func (resolver fixedResolver) DesiredIPv6(context.Context, Binding) (string, error) {
	return resolver.address, resolver.err
}

func TestCoordinatorDisablesBindingWhenOwnerLosesSourcePermission(t *testing.T) {
	repository, err := filestore.OpenControlPlane(filepath.Join(t.TempDir(), "control-plane.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store.NewControlPlaneService(repository), nil)
	binding, err := service.Create(context.Background(), CreateCommand{SourceType: SourceDevice, SourceID: "device-1", OwnerUserID: "user-1", FQDN: "denied.rokilai.online", ExpectedRevision: 0})
	if err != nil {
		t.Fatal(err)
	}
	coordinator := NewCoordinator(service, &coordinatorProvider{}, fixedResolver{err: ErrSourceUnauthorized})
	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	bindings, err := service.List(context.Background(), SourceDevice, "device-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || bindings[0].ConfigState != ConfigDisabled || bindings[0].Revision != binding.Revision+1 {
		t.Fatalf("bindings=%+v", bindings)
	}
}

func TestCoordinatorMarksPreviouslySyncedBindingStaleWithoutWritingDNS(t *testing.T) {
	repository, err := filestore.OpenControlPlane(filepath.Join(t.TempDir(), "control-plane.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store.NewControlPlaneService(repository), nil)
	provider := &coordinatorProvider{}
	binding, err := service.Create(context.Background(), CreateCommand{SourceType: SourceServer, SourceID: LocalServerSourceID, FQDN: "stale.rokilai.online", ExpectedRevision: 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.QueueDesiredAddress(context.Background(), binding.BindingID, binding.Revision, "2001:db8::2"); err != nil {
		t.Fatal(err)
	}
	coordinator := NewCoordinator(service, provider, fixedResolver{address: "2001:db8::2"})
	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	coordinator.resolver = fixedResolver{err: ErrNoDesiredAddress}
	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	bindings, err := service.List(context.Background(), SourceServer, LocalServerSourceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || bindings[0].RuntimeState != RuntimeStale || bindings[0].LastAppliedIPv6 != "2001:db8::2" || provider.applies != 1 {
		t.Fatalf("bindings=%+v applies=%d", bindings, provider.applies)
	}
}

func TestCoordinatorReconcilesSameAddressAfterBindingIsReenabled(t *testing.T) {
	repository, err := filestore.OpenControlPlane(filepath.Join(t.TempDir(), "control-plane.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store.NewControlPlaneService(repository), nil)
	provider := &coordinatorProvider{}
	binding, err := service.Create(context.Background(), CreateCommand{SourceType: SourceServer, SourceID: LocalServerSourceID, FQDN: "reenable.rokilai.online", ExpectedRevision: 0})
	if err != nil {
		t.Fatal(err)
	}
	coordinator := NewCoordinator(service, provider, fixedResolver{address: "2001:db8::2"})
	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	bindings, err := service.List(context.Background(), SourceServer, LocalServerSourceID)
	if err != nil || len(bindings) != 1 || bindings[0].RuntimeState != RuntimeSynced {
		t.Fatalf("initial bindings=%+v err=%v", bindings, err)
	}
	disabled, err := service.Disable(context.Background(), binding.BindingID, bindings[0].Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Enable(context.Background(), EnableCommand{BindingID: binding.BindingID, ExpectedRevision: disabled.Revision, PublisherDisabledConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	bindings, err = service.List(context.Background(), SourceServer, LocalServerSourceID)
	if err != nil || len(bindings) != 1 || bindings[0].RuntimeState != RuntimeSynced || bindings[0].LastAppliedIPv6 != "2001:db8::2" || provider.applies != 2 {
		t.Fatalf("reenabled bindings=%+v applies=%d err=%v", bindings, provider.applies, err)
	}
}

type coordinatorProvider struct{ observes, applies int }

func (provider *coordinatorProvider) Observe(context.Context, ObserveRequest) (RecordObservation, error) {
	provider.observes++
	return RecordObservation{Exists: true, RecordID: "record-1", IPv6: "2001:db8::1", TTL: 120}, nil
}
func (provider *coordinatorProvider) Apply(_ context.Context, request ApplyRequest) (RecordObservation, error) {
	provider.applies++
	return RecordObservation{Exists: true, RecordID: "record-1", IPv6: request.DesiredIPv6, TTL: 120}, nil
}

type convergencePendingProvider struct{}

func (convergencePendingProvider) Observe(context.Context, ObserveRequest) (RecordObservation, error) {
	return RecordObservation{}, nil
}

func (convergencePendingProvider) Apply(_ context.Context, request ApplyRequest) (RecordObservation, error) {
	return RecordObservation{Exists: true, RecordID: "record-created", IPv6: request.DesiredIPv6, TTL: 120}, classifiedTestError{}
}

type classifiedTestError struct{}

func (classifiedTestError) Error() string    { return "dns not converged" }
func (classifiedTestError) Category() string { return "dns_not_converged" }
func (classifiedTestError) CanRetry() bool   { return true }

func TestCoordinatorRetainsProviderIdentityWhenDNSConvergenceIsPending(t *testing.T) {
	repository, err := filestore.OpenControlPlane(filepath.Join(t.TempDir(), "control-plane.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store.NewControlPlaneService(repository), nil)
	if _, err := service.Create(context.Background(), CreateCommand{SourceType: SourceServer, SourceID: LocalServerSourceID, FQDN: "pending.rokilai.online", ExpectedRevision: 0}); err != nil {
		t.Fatal(err)
	}
	coordinator := NewCoordinator(service, convergencePendingProvider{}, fixedResolver{address: "2001:db8::2"})
	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	bindings, err := service.List(context.Background(), SourceServer, LocalServerSourceID)
	if err != nil || len(bindings) != 1 || bindings[0].ProviderRecordID != "record-created" || bindings[0].ProviderIPv6 != "2001:db8::2" || bindings[0].LastAppliedIPv6 != "" || bindings[0].RuntimeState != RuntimeFailed {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
}

func TestCoordinatorObservesWithoutWritingAndReconcilesEnabledBinding(t *testing.T) {
	repository, err := filestore.OpenControlPlane(filepath.Join(t.TempDir(), "control-plane.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store.NewControlPlaneService(repository), nil)
	provider := &coordinatorProvider{}
	observing, err := service.Create(context.Background(), CreateCommand{SourceType: SourceServer, SourceID: LocalServerSourceID, FQDN: "observe.rokilai.online", ExpectedRevision: 0, ExistingRecord: true})
	if err != nil {
		t.Fatal(err)
	}
	coordinator := NewCoordinator(service, provider, fixedResolver{address: "2001:db8::2"})
	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.observes != 1 || provider.applies != 0 {
		t.Fatalf("observes=%d applies=%d", provider.observes, provider.applies)
	}
	enabled, err := service.Enable(context.Background(), EnableCommand{BindingID: observing.BindingID, ExpectedRevision: observing.Revision, PublisherDisabledConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.applies != 1 {
		t.Fatalf("applies=%d", provider.applies)
	}
	bindings, err := service.List(context.Background(), SourceServer, LocalServerSourceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || bindings[0].RuntimeState != RuntimeSynced || bindings[0].LastAppliedIPv6 != "2001:db8::2" || enabled.Revision != 2 {
		t.Fatalf("bindings=%+v", bindings)
	}
}

type concurrentProvider struct {
	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	failFQDN    string
	entered     chan struct{}
	release     chan struct{}
}

func (p *concurrentProvider) Observe(context.Context, ObserveRequest) (RecordObservation, error) {
	return RecordObservation{}, nil
}

func (p *concurrentProvider) Apply(ctx context.Context, req ApplyRequest) (RecordObservation, error) {
	p.mu.Lock()
	p.inFlight++
	if p.inFlight > p.maxInFlight {
		p.maxInFlight = p.inFlight
	}
	p.mu.Unlock()
	p.entered <- struct{}{}
	select {
	case <-p.release:
	case <-ctx.Done():
		return RecordObservation{}, ctx.Err()
	}
	p.mu.Lock()
	p.inFlight--
	p.mu.Unlock()
	if req.FQDN == p.failFQDN {
		return RecordObservation{Exists: true, RecordID: "rec-" + req.FQDN, IPv6: req.DesiredIPv6, TTL: 120}, classifiedTestError{}
	}
	return RecordObservation{Exists: true, RecordID: "rec-" + req.FQDN, IPv6: req.DesiredIPv6, TTL: 120}, nil
}

func TestCoordinatorReconcilesMultipleBindingsConcurrentlyAndIsolatesFailures(t *testing.T) {
	repository, err := filestore.OpenControlPlane(filepath.Join(t.TempDir(), "control-plane.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store.NewControlPlaneService(repository), nil)
	fqdns := []string{"a.rokilai.online", "b.rokilai.online", "c.rokilai.online", "d.rokilai.online"}
	for _, fqdn := range fqdns {
		if _, err := service.Create(context.Background(), CreateCommand{SourceType: SourceServer, SourceID: LocalServerSourceID, FQDN: fqdn}); err != nil {
			t.Fatal(err)
		}
	}
	provider := &concurrentProvider{
		failFQDN: "b.rokilai.online",
		entered:  make(chan struct{}, len(fqdns)),
		release:  make(chan struct{}),
	}
	coordinator := NewCoordinator(service, provider, fixedResolver{address: "2001:db8::2"})
	done := make(chan error, 1)
	go func() {
		done <- coordinator.RunOnce(context.Background())
	}()
	for i := 0; i < len(fqdns); i++ {
		select {
		case <-provider.entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("expected all %d tasks to run concurrently, only %d entered", len(fqdns), i)
		}
	}
	close(provider.release)
	if err := <-done; err != nil {
		t.Fatalf("RunOnce err=%v", err)
	}
	if provider.maxInFlight < 2 {
		t.Fatalf("maxInFlight=%d, want >= 2", provider.maxInFlight)
	}
	bindings, err := service.List(context.Background(), SourceServer, LocalServerSourceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bindings {
		if b.FQDN == "b.rokilai.online" {
			if b.RuntimeState != RuntimeFailed {
				t.Fatalf("failing binding %s state=%s, want failed", b.FQDN, b.RuntimeState)
			}
			continue
		}
		if b.RuntimeState != RuntimeSynced || b.LastAppliedIPv6 != "2001:db8::2" {
			t.Fatalf("binding %s state=%s applied=%s, want synced/2001:db8::2", b.FQDN, b.RuntimeState, b.LastAppliedIPv6)
		}
	}
}
