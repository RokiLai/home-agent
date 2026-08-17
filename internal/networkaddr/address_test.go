package networkaddr

import (
	"testing"
	"time"
)

func TestIsValidCandidate(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	future := now.Add(1 * time.Hour)
	past := now.Add(-1 * time.Hour)

	tests := []struct {
		name     string
		addr     ReportedIPv6Address
		expected bool
	}{
		{
			name: "Valid global unicast",
			addr: ReportedIPv6Address{
				Address:   "240e:1234:5678:10::20",
				Interface: "en0",
			},
			expected: true,
		},
		{
			name: "Valid global unicast with valid lifetime",
			addr: ReportedIPv6Address{
				Address:        "240e:1234:5678:10::20",
				Interface:      "en0",
				PreferredUntil: &future,
				ValidUntil:     &future,
			},
			expected: true,
		},
		{
			name: "Expired preferred lifetime",
			addr: ReportedIPv6Address{
				Address:        "240e:1234:5678:10::20",
				Interface:      "en0",
				PreferredUntil: &past,
			},
			expected: false,
		},
		{
			name: "Temporary address rejected",
			addr: ReportedIPv6Address{
				Address:   "240e:1234:5678:10::20",
				Interface: "en0",
				Temporary: true,
			},
			expected: false,
		},
		{
			name: "Deprecated address rejected",
			addr: ReportedIPv6Address{
				Address:    "240e:1234:5678:10::20",
				Interface:  "en0",
				Deprecated: true,
			},
			expected: false,
		},
		{
			name: "IPv4 rejected",
			addr: ReportedIPv6Address{
				Address:   "192.168.1.100",
				Interface: "en0",
			},
			expected: false,
		},
		{
			name: "Link-local IPv6 rejected",
			addr: ReportedIPv6Address{
				Address:   "fe80::1",
				Interface: "en0",
			},
			expected: false,
		},
		{
			name: "Loopback rejected",
			addr: ReportedIPv6Address{
				Address:   "::1",
				Interface: "lo0",
			},
			expected: false,
		},
		{
			name: "Unspecified rejected",
			addr: ReportedIPv6Address{
				Address:   "::",
				Interface: "en0",
			},
			expected: false,
		},
		{
			name: "Multicast rejected",
			addr: ReportedIPv6Address{
				Address:   "ff02::1",
				Interface: "en0",
			},
			expected: false,
		},
		{
			name: "ULA rejected (fc00::/7)",
			addr: ReportedIPv6Address{
				Address:   "fd12:3456:789a::1",
				Interface: "en0",
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsValidCandidate(tc.addr, now)
			if got != tc.expected {
				t.Fatalf("expected IsValidCandidate=%v, got=%v for %s", tc.expected, got, tc.addr.Address)
			}
		})
	}
}

func TestNormalizeAndFilterCandidates(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	input := []ReportedIPv6Address{
		{Address: "fe80::1234", Interface: "en0"},                       // link-local, reject
		{Address: "240e:9999::2", Interface: "en0"},                      // valid 2nd
		{Address: "240e:1111::1", Interface: "en0"},                      // valid 1st
		{Address: "240e:1111:0000:0000:0000:0000:0000:0001", Interface: "en0"}, // duplicate of 1st, normalize & dedup
		{Address: "240e:1111::1", Interface: "en0", Temporary: true},     // temp, reject
	}

	result := NormalizeAndFilterCandidates(input, now)
	if len(result) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(result))
	}
	if result[0].Address != "240e:1111::1" {
		t.Errorf("expected first to be 240e:1111::1, got %s", result[0].Address)
	}
	if result[1].Address != "240e:9999::2" {
		t.Errorf("expected second to be 240e:9999::2, got %s", result[1].Address)
	}
}

func TestAddressesEqual(t *testing.T) {
	t1 := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 8, 17, 13, 0, 0, 0, time.UTC)

	a := []ReportedIPv6Address{
		{Address: "240e:1::1", Interface: "en0", PreferredUntil: &t1},
	}
	b := []ReportedIPv6Address{
		{Address: "240e:1::1", Interface: "en0", PreferredUntil: &t1},
	}
	c := []ReportedIPv6Address{
		{Address: "240e:1::1", Interface: "en0", PreferredUntil: &t2},
	}

	if !AddressesEqual(a, b) {
		t.Errorf("expected a and b to be equal")
	}
	if AddressesEqual(a, c) {
		t.Errorf("expected a and c to be unequal")
	}
}
