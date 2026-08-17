package prefixstate

import (
	"cmp"
	"net/netip"
	"slices"
	"strings"
	"time"

	"homeagent/internal/networkaddr"
)

type ReportedIPv6Prefix struct {
	Prefix         string     `json:"prefix"`
	PreferredUntil *time.Time `json:"preferred_until,omitempty"`
	ValidUntil     *time.Time `json:"valid_until,omitempty"`
}

type RouterPrefixState struct {
	RouterDeviceID string               `json:"router_device_id"`
	NetworkID      string               `json:"network_id"`
	Revision       uint64               `json:"revision"`
	ObservedAt     time.Time            `json:"observed_at"`
	LastSeenAt     time.Time            `json:"last_seen_at"`
	Prefixes       []ReportedIPv6Prefix `json:"prefixes"`
}

// IsValidPrefix checks if a prefix is valid global unicast IPv6 and not expired.
func IsValidPrefix(p ReportedIPv6Prefix, now time.Time) bool {
	if p.PreferredUntil != nil && !p.PreferredUntil.IsZero() && !p.PreferredUntil.After(now) {
		return false
	}
	if p.ValidUntil != nil && !p.ValidUntil.IsZero() && !p.ValidUntil.After(now) {
		return false
	}

	prefix, err := netip.ParsePrefix(strings.TrimSpace(p.Prefix))
	if err != nil || !prefix.IsValid() {
		return false
	}
	addr := prefix.Addr()
	if !addr.Is6() || addr.Is4In6() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	b := addr.As16()
	if (b[0] & 0xfe) == 0xfc { // ULA check
		return false
	}
	return true
}

// NormalizePrefixes filters, parses, deduplicates, and sorts valid prefixes.
func NormalizePrefixes(raw []ReportedIPv6Prefix, now time.Time) []ReportedIPv6Prefix {
	type entry struct {
		pref netip.Prefix
		orig ReportedIPv6Prefix
	}

	seen := make(map[netip.Prefix]bool)
	var entries []entry

	for _, p := range raw {
		if !IsValidPrefix(p, now) {
			continue
		}
		prefix, err := netip.ParsePrefix(strings.TrimSpace(p.Prefix))
		if err != nil {
			continue
		}
		prefix = prefix.Masked()
		if seen[prefix] {
			continue
		}
		seen[prefix] = true

		norm := p
		norm.Prefix = prefix.String()
		entries = append(entries, entry{
			pref: prefix,
			orig: norm,
		})
	}

	slices.SortFunc(entries, func(a, b entry) int {
		return cmp.Compare(a.pref.String(), b.pref.String())
	})

	res := make([]ReportedIPv6Prefix, len(entries))
	for i, e := range entries {
		res[i] = e.orig
	}
	return res
}

// PrefixesEqual checks if two prefix lists are identical.
func PrefixesEqual(a, b []ReportedIPv6Prefix) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Prefix != b[i].Prefix {
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

// Intersect computes reported_candidates ∩ active_preferred_lan_prefixes.
func Intersect(candidates []networkaddr.ReportedIPv6Address, activePrefixes []netip.Prefix) []netip.Addr {
	var valid []netip.Addr
	seen := make(map[netip.Addr]bool)

	for _, c := range candidates {
		ip, err := netip.ParseAddr(strings.TrimSpace(c.Address))
		if err != nil || !ip.IsValid() {
			continue
		}
		if seen[ip] {
			continue
		}

		// Check if ip is contained in any active prefix
		matched := false
		for _, pref := range activePrefixes {
			if pref.Contains(ip) {
				matched = true
				break
			}
		}

		if matched {
			seen[ip] = true
			valid = append(valid, ip)
		}
	}

	slices.SortFunc(valid, func(a, b netip.Addr) int {
		return a.Compare(b)
	})
	return valid
}

// SelectAddress chooses the optimal address from valid candidates.
// If the currently applied address is still valid in the candidate set, it is preferred to avoid DNS flapping.
// Otherwise, the first deterministic candidate is chosen.
func SelectAddress(validCandidates []netip.Addr, currentApplied string) (netip.Addr, bool) {
	if len(validCandidates) == 0 {
		return netip.Addr{}, false
	}

	if currentApplied != "" {
		appliedIP, err := netip.ParseAddr(strings.TrimSpace(currentApplied))
		if err == nil && appliedIP.IsValid() {
			for _, c := range validCandidates {
				if c == appliedIP {
					return appliedIP, true
				}
			}
		}
	}

	return validCandidates[0], true
}
