package servernetwork

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"homeagent/internal/networkaddr"
)

type mockAddressProvider struct {
	addrs []networkaddr.ReportedIPv6Address
	err   error
}

func (m *mockAddressProvider) GetAddresses(ctx context.Context, iface string) ([]networkaddr.ReportedIPv6Address, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.addrs, nil
}

func TestCollector_CollectAndSelectCandidate(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	future := now.Add(1 * time.Hour)
	past := now.Add(-1 * time.Hour)

	tests := []struct {
		name              string
		providerAddrs     []networkaddr.ReportedIPv6Address
		currentConfirmed  netip.Addr
		expectAddr        netip.Addr
		expectErr         error
		now               time.Time
	}{
		{
			name: "single valid address",
			providerAddrs: []networkaddr.ReportedIPv6Address{
				{Address: "240e:390:1234:5678::1", Interface: "en0", PreferredUntil: &future, ValidUntil: &future},
			},
			currentConfirmed: netip.Addr{},
			expectAddr:       netip.MustParseAddr("240e:390:1234:5678::1"),
			now:              now,
		},
		{
			name: "filter temporary and deprecated",
			providerAddrs: []networkaddr.ReportedIPv6Address{
				{Address: "240e:390:1234:5678::2", Interface: "en0", Temporary: true},
				{Address: "240e:390:1234:5678::3", Interface: "en0", Deprecated: true},
				{Address: "240e:390:1234:5678::4", Interface: "en0", PreferredUntil: &past},
				{Address: "240e:390:1234:5678::5", Interface: "en0", PreferredUntil: &future},
			},
			currentConfirmed: netip.Addr{},
			expectAddr:       netip.MustParseAddr("240e:390:1234:5678::5"),
			now:              now,
		},
		{
			name: "preserve current confirmed address when still valid",
			providerAddrs: []networkaddr.ReportedIPv6Address{
				{Address: "240e:390:1234:5678::10", Interface: "en0"},
				{Address: "240e:390:1234:5678::20", Interface: "en0"},
			},
			currentConfirmed: netip.MustParseAddr("240e:390:1234:5678::20"),
			expectAddr:       netip.MustParseAddr("240e:390:1234:5678::20"),
			now:              now,
		},
		{
			name: "switch to smallest address when current confirmed is invalid",
			providerAddrs: []networkaddr.ReportedIPv6Address{
				{Address: "240e:390:1234:5678::20", Interface: "en0"},
				{Address: "240e:390:1234:5678::10", Interface: "en0"},
			},
			currentConfirmed: netip.MustParseAddr("240e:390:1234:5678::99"),
			expectAddr:       netip.MustParseAddr("240e:390:1234:5678::10"),
			now:              now,
		},
		{
			name:             "no valid address returns ErrNoValidAddress",
			providerAddrs:    []networkaddr.ReportedIPv6Address{},
			currentConfirmed: netip.Addr{},
			expectErr:        ErrNoValidAddress,
			now:              now,
		},
		{
			name: "ula and link local rejected",
			providerAddrs: []networkaddr.ReportedIPv6Address{
				{Address: "fe80::1", Interface: "en0"},
				{Address: "fd00::1", Interface: "en0"},
			},
			currentConfirmed: netip.Addr{},
			expectErr:        ErrNoValidAddress,
			now:              now,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prov := &mockAddressProvider{addrs: tt.providerAddrs}
			collector := NewCollectorWithTime("en0", prov, func() time.Time { return tt.now })

			addr, err := collector.Collect(context.Background(), tt.currentConfirmed)
			if tt.expectErr != nil {
				if err != tt.expectErr {
					t.Fatalf("expected error %v, got %v", tt.expectErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if addr != tt.expectAddr {
				t.Fatalf("expected address %v, got %v", tt.expectAddr, addr)
			}
		})
	}
}

func TestCollector_ProviderErrorAndDefaults(t *testing.T) {
	provErr := &mockAddressProvider{err: context.DeadlineExceeded}
	collector := NewCollector("en0", provErr)

	_, err := collector.Collect(context.Background(), netip.Addr{})
	if err == nil {
		t.Fatalf("expected error from failing provider, got nil")
	}

	cDefault := NewCollector("en0", nil)
	if cDefault.provider == nil {
		t.Fatalf("expected non-nil default provider")
	}
}

