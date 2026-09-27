package domainbinding

import "context"

type Reconciler struct{ provider Provider }

func NewReconciler(provider Provider) *Reconciler { return &Reconciler{provider: provider} }

func (reconciler *Reconciler) Reconcile(ctx context.Context, binding Binding) (RecordObservation, error) {
	if reconciler.provider == nil {
		return RecordObservation{}, nil
	}
	if binding.ConfigState == ConfigObserving {
		return reconciler.provider.Observe(ctx, ObserveRequest{FQDN: binding.FQDN, ProviderRecordID: binding.ProviderRecordID})
	}
	if binding.ConfigState != ConfigEnabled || binding.DesiredIPv6 == "" {
		return RecordObservation{}, nil
	}
	return reconciler.provider.Apply(ctx, ApplyRequest{FQDN: binding.FQDN, ProviderRecordID: binding.ProviderRecordID, DesiredIPv6: binding.DesiredIPv6, TTL: binding.TTL, Proxied: binding.Proxied})
}
