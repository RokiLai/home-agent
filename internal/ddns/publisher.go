package ddns

import (
	"context"
	"net/netip"
)

// DNSPublisher defines the contract for querying, updating, and deleting AAAA records.
type DNSPublisher interface {
	GetAAAA(ctx context.Context, record string) ([]netip.Addr, error)
	UpsertAAAA(ctx context.Context, record string, address netip.Addr, ttl int) error
	DeleteAAAA(ctx context.Context, record string) error
}
