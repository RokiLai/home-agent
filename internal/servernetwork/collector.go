// Package servernetwork 提供服务端物理网卡稳定公网 IPv6 地址采集、DDNS 自更新状态机与自举调和协调器。
package servernetwork

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"homeagent/internal/networkaddr"
)

var (
	// ErrNoValidAddress 表示在指定接口上未探测到任何可用的全局单播 IPv6 地址。
	ErrNoValidAddress = errors.New("no valid global unicast ipv6 address found on interface")
)

// AddressProvider 定义物理网卡地址探测接口。
type AddressProvider interface {
	GetAddresses(ctx context.Context, iface string) ([]networkaddr.ReportedIPv6Address, error)
}

// Collector 封装服务端物理网卡 IPv6 地址采集与候选筛选逻辑。
type Collector struct {
	iface    string
	provider AddressProvider
	nowFunc  func() time.Time
}

// NewCollector 创建指定网络接口的采集器实例。
func NewCollector(iface string, provider AddressProvider) *Collector {
	return NewCollectorWithTime(iface, provider, func() time.Time {
		return time.Now().UTC()
	})
}

// NewCollectorWithTime 允许注入自定义时间源，用于测试验证生命周期过期逻辑。
func NewCollectorWithTime(iface string, provider AddressProvider, nowFunc func() time.Time) *Collector {
	if provider == nil {
		provider = networkaddr.NewGenericProvider()
	}
	if nowFunc == nil {
		nowFunc = func() time.Time { return time.Now().UTC() }
	}
	return &Collector{
		iface:    iface,
		provider: provider,
		nowFunc:  nowFunc,
	}
}

// Collect 探测物理接口上的地址，过滤并返回最合适的稳定 IPv6 地址。
// 若当前已确认的地址 currentConfirmed 依然在有效候选列表中，则优先返回该地址以防止不必要的 DNS 切换。
func (c *Collector) Collect(ctx context.Context, currentConfirmed netip.Addr) (netip.Addr, error) {
	raw, err := c.provider.GetAddresses(ctx, c.iface)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("provider get addresses for %s: %w", c.iface, err)
	}

	valid := networkaddr.NormalizeAndFilterCandidates(raw, c.nowFunc())
	if len(valid) == 0 {
		return netip.Addr{}, ErrNoValidAddress
	}

	// 检查当前确认的地址是否依然有效
	if currentConfirmed.IsValid() && currentConfirmed.Is6() && !currentConfirmed.Is4In6() {
		confirmedStr := currentConfirmed.String()
		for _, cand := range valid {
			if strings.EqualFold(cand.Address, confirmedStr) {
				return currentConfirmed, nil
			}
		}
	}

	// 排序并返回数值最小的首选地址
	networkaddr.SortAddresses(valid)
	bestIP, err := netip.ParseAddr(strings.TrimSpace(valid[0].Address))
	if err != nil || !bestIP.IsValid() {
		return netip.Addr{}, fmt.Errorf("invalid candidate address format %q: %w", valid[0].Address, err)
	}

	return bestIP, nil
}
