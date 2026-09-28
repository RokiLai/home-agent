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
	Exists   bool
	RecordID string
	IPv6     string
	TTL      int
	Proxied  bool
	Comment  string
}

type Provider interface {
	Observe(context.Context, ObserveRequest) (RecordObservation, error)
	Apply(context.Context, ApplyRequest) (RecordObservation, error)
}

type ClassifiedError interface {
	error
	Category() string
	CanRetry() bool
}
