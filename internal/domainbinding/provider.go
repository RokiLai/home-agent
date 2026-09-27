package domainbinding

import "context"

type ObserveRequest struct {
	FQDN             string
	ProviderRecordID string
}

type ApplyRequest struct {
	FQDN             string
	ProviderRecordID string
	DesiredIPv6      string
	TTL              int
	Proxied          bool
}

type RecordObservation struct {
	RecordID string
	IPv6     string
	TTL      int
	Proxied  bool
}

type Provider interface {
	Observe(context.Context, ObserveRequest) (RecordObservation, error)
	Apply(context.Context, ApplyRequest) (RecordObservation, error)
}
