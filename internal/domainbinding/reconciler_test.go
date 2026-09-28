package domainbinding

import (
	"context"
	"testing"
)

type recordingProvider struct{ writes int }

func (provider *recordingProvider) Observe(context.Context, ObserveRequest) (RecordObservation, error) {
	return RecordObservation{}, nil
}

func (provider *recordingProvider) Apply(context.Context, ApplyRequest) (RecordObservation, error) {
	provider.writes++
	return RecordObservation{}, nil
}

func TestReconcilerNeverWritesObservingOrDisabledBindings(t *testing.T) {
	provider := &recordingProvider{}
	reconciler := NewReconciler(provider)
	for _, state := range []BindingState{ConfigObserving, ConfigDisabled} {
		if _, err := reconciler.Reconcile(context.Background(), Binding{ConfigState: state}); err != nil {
			t.Fatal(err)
		}
	}
	if provider.writes != 0 {
		t.Fatalf("provider writes = %d", provider.writes)
	}
}

func TestReconcilerAppliesOnlyEnabledBindingWithDesiredAddress(t *testing.T) {
	provider := &recordingProvider{}
	reconciler := NewReconciler(provider)
	if _, err := reconciler.Reconcile(context.Background(), Binding{ConfigState: ConfigEnabled}); err != nil {
		t.Fatal(err)
	}
	if provider.writes != 0 {
		t.Fatalf("empty desired address writes=%d", provider.writes)
	}
	if _, err := reconciler.Reconcile(context.Background(), Binding{ConfigState: ConfigEnabled, FQDN: "host.rokilai.online", DesiredIPv6: "2001:db8::1"}); err != nil {
		t.Fatal(err)
	}
	if provider.writes != 1 {
		t.Fatalf("enabled writes=%d", provider.writes)
	}
}
