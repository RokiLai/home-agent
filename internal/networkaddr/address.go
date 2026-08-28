// Package networkaddr 提供本地网络接口 IPv6 地址探测、规范化过滤、去重排序及地址变化监听触发器。
package networkaddr

import (
	"cmp"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// ReportedIPv6Address 表示在本地网络接口上探测到的 IPv6 地址详细元数据。
type ReportedIPv6Address struct {
	Address        string     `json:"address"`
	Interface      string     `json:"interface"`
	PrefixLength   int        `json:"prefix_length,omitempty"`
	Temporary      bool       `json:"temporary"`
	Deprecated     bool       `json:"deprecated"`
	PreferredUntil *time.Time `json:"preferred_until,omitempty"`
	ValidUntil     *time.Time `json:"valid_until,omitempty"`
}

// IsValidCandidate 校验该地址是否适合用于公网 IPv6 DDNS 动态域名发布。
// 过滤 IPv4、回环、链路本地、组播、未指定、ULA 唯一本地地址 (fc00::/7)、临时隐私扩展地址、废弃或已过期地址。
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

// isULA 检查 IP 地址是否属于 IPv6 ULA 唯一本地地址范围 (fc00::/7)。
func isULA(ip netip.Addr) bool {
	b := ip.As16()
	return (b[0] & 0xfe) == 0xfc
}

// NormalizeAndFilterCandidates 过滤上报地址列表，仅保留有效候选，规范化文本表达，去重并确定性排序。
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

// AddressesEqual 判断两个上报地址切片是否内容完全相同。
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

// SortAddresses 按 IP 二进制数值大小对 ReportedIPv6Address 切片进行确定性升序排序。
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
