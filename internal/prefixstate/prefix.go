// Package prefixstate 管理路由器局域网 IPv6 前缀的生命周期、合法性校验以及与设备地址的交集计算。
package prefixstate

import (
	"cmp"
	"net/netip"
	"slices"
	"strings"
	"time"

	"homeagent/internal/networkaddr"
)

// ReportedIPv6Prefix 表示路由器通告的 IPv6 网络前缀及其首选和有效生存期。
type ReportedIPv6Prefix struct {
	Prefix         string     `json:"prefix"`
	PreferredUntil *time.Time `json:"preferred_until,omitempty"`
	ValidUntil     *time.Time `json:"valid_until,omitempty"`
}

// RouterPrefixState 记录指定路由器 Agent 上报的前缀快照与版本控制状态。
type RouterPrefixState struct {
	RouterDeviceID string               `json:"router_device_id"`
	NetworkID      string               `json:"network_id"`
	Revision       uint64               `json:"revision"`
	ObservedAt     time.Time            `json:"observed_at"`
	LastSeenAt     time.Time            `json:"last_seen_at"`
	Prefixes       []ReportedIPv6Prefix `json:"prefixes"`
}

// IsValidPrefix 校验前缀是否为合法的全球单播 IPv6 前缀，且未过期且非 ULA 地址。
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
	if (b[0] & 0xfe) == 0xfc { // ULA 校验
		return false
	}
	return true
}

// NormalizePrefixes 对前缀列表执行过滤、掩码规范化、去重和确定性排序。
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

// PrefixesEqual 判断两个前缀列表是否内容完全一致。
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

// Intersect 计算候选地址与当前活动局域网前缀的交集（即 reported_candidates ∩ active_preferred_lan_prefixes）。
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

		// 检查 IP 是否落在任一有效前缀范围内
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

// SelectAddress 从合法候选地址集中选出最优地址。
// 若当前已生效地址仍然有效，则优先保持该地址以防止 DNS 记录频繁抖动；否则按确定性规则选择首个地址。
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
