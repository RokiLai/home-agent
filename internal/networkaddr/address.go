package networkaddr

import (
	"cmp"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// ReportedIPv6Address represents an IPv6 address discovered on a local network interface.
type ReportedIPv6Address struct {
	Address        string     `json:"address"`
	Interface      string     `json:"interface"`
	PrefixLength   int        `json:"prefix_length,omitempty"`
	Temporary      bool       `json:"temporary"`
	Deprecated     bool       `json:"deprecated"`
	PreferredUntil *time.Time `json:"preferred_until,omitempty"`
	ValidUntil     *time.Time `json:"valid_until,omitempty"`
}

// IsValidCandidate checks whether an address is suitable for public IPv6 DDNS publishing.
// It excludes IPv4, loopback, link-local, multicast, unspecified, ULA (fc00::/7),
// temporary, deprecated, or expired addresses.
func IsValidCandidate(addr ReportedIPv6Address, now time.Time) bool {
	if addr.Temporary || addr.Deprecated {
		return false
	}
	if addr.PreferredUntil != nil && !addr.PreferredUntil.IsZero() && !addr.PreferredUntil.After(now) {
		return false
	}
	if addr.ValidUntil != nil && !addr.ValidUntil.IsZero() && !addr.ValidUntil.After(now) {
		return false
	}

	ip, err := netip.ParseAddr(strings.TrimSpace(addr.Address))
	if err != nil || !ip.IsValid() || !ip.Is6() || ip.Is4In6() {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if isULA(ip) {
		return false
	}
	return true
}

// isULA checks if the address is a Unique Local Address (fc00::/7).
func isULA(ip netip.Addr) bool {
	b := ip.As16()
	return (b[0] & 0xfe) == 0xfc
}

// NormalizeAndFilterCandidates filters a list of reported addresses to keep only valid candidates,
// normalizes their text representations, deduplicates them, and sorts them deterministically.
func NormalizeAndFilterCandidates(addrs []ReportedIPv6Address, now time.Time) []ReportedIPv6Address {
	type entry struct {
		ip   netip.Addr
		orig ReportedIPv6Address
	}

	seen := make(map[netip.Addr]bool)
	var entries []entry

	for _, a := range addrs {
		if !IsValidCandidate(a, now) {
			continue
		}
		ip, err := netip.ParseAddr(strings.TrimSpace(a.Address))
		if err != nil {
			continue
		}
		if seen[ip] {
			continue
		}
		seen[ip] = true

		normalized := a
		normalized.Address = ip.String()
		entries = append(entries, entry{
			ip:   ip,
			orig: normalized,
		})
	}

	slices.SortFunc(entries, func(a, b entry) int {
		return a.ip.Compare(b.ip)
	})

	result := make([]ReportedIPv6Address, len(entries))
	for i, e := range entries {
		result[i] = e.orig
	}
	return result
}

// AddressesEqual checks whether two slices of reported addresses are identical.
func AddressesEqual(a, b []ReportedIPv6Address) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Address != b[i].Address ||
			a[i].Interface != b[i].Interface ||
			a[i].PrefixLength != b[i].PrefixLength ||
			a[i].Temporary != b[i].Temporary ||
			a[i].Deprecated != b[i].Deprecated {
			return false
		}
		if (a[i].PreferredUntil == nil) != (b[i].PreferredUntil == nil) {
			return false
		}
		if a[i].PreferredUntil != nil && !a[i].PreferredUntil.Equal(*b[i].PreferredUntil) {
			return false
		}
	}
	return true
}

// SortAddresses sorts ReportedIPv6Address slice by IP value deterministically.
func SortAddresses(addrs []ReportedIPv6Address) {
	slices.SortFunc(addrs, func(a, b ReportedIPv6Address) int {
		ipA, errA := netip.ParseAddr(a.Address)
		ipB, errB := netip.ParseAddr(b.Address)
		if errA == nil && errB == nil {
			return ipA.Compare(ipB)
		}
		return cmp.Compare(a.Address, b.Address)
	})
}
