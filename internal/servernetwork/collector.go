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
	// ErrUnsupportedRouteInterface 表示默认 IPv6 路由落在虚拟接口，不能安全推断物理接口。
	ErrUnsupportedRouteInterface = errors.New("default ipv6 route uses an unsupported virtual interface")
)

// AddressProvider 定义物理网卡地址探测接口。
type AddressProvider interface {
	GetAddresses(ctx context.Context, iface string) ([]networkaddr.ReportedIPv6Address, error)
}

// Collector 封装服务端物理网卡 IPv6 地址采集与候选筛选逻辑。
type Collector struct {
	iface    string
	provider AddressProvider
	resolver networkaddr.DefaultIPv6RouteResolver
	nowFunc  func() time.Time
}

// Detection 是一次自动网络解析的确定结果。
type Detection struct {
	Interface string
	Gateway   string
	Address   netip.Addr
}

// NewCollector 创建指定网络接口的采集器实例。
func NewCollector(iface string, provider AddressProvider) *Collector {
	return NewCollectorWithTime(iface, provider, func() time.Time {
		return time.Now().UTC()
	})
}

// NewAutoCollectorWithTime 创建使用内核默认 IPv6 路由自动确定接口的采集器。
func NewAutoCollectorWithTime(provider AddressProvider, resolver networkaddr.DefaultIPv6RouteResolver, nowFunc func() time.Time) *Collector {
	c := NewCollectorWithTime("", provider, nowFunc)
	if resolver == nil {
		resolver = networkaddr.NewDefaultIPv6RouteResolver()
	}
	c.resolver = resolver
	return c
}

// NewAutoCollector 创建自动解析默认路由接口的采集器。
func NewAutoCollector(provider AddressProvider) *Collector {
	return NewAutoCollectorWithTime(provider, nil, nil)
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
	result, err := c.Detect(ctx, currentConfirmed)
	return result.Address, err
}

// Detect 自动解析接口并从该接口选择稳定公网 IPv6。
func (c *Collector) Detect(ctx context.Context, currentConfirmed netip.Addr) (Detection, error) {
	iface := c.iface
	gateway := ""
	if iface == "" {
		if c.resolver == nil {
			return Detection{}, errors.New("default ipv6 route resolver is not configured")
		}
		route, err := c.resolver.ResolveDefaultIPv6Route(ctx)
		if err != nil {
			return Detection{}, fmt.Errorf("resolve default ipv6 route: %w", err)
		}
		iface = strings.TrimSpace(route.Interface)
		gateway = strings.TrimSpace(route.Gateway)
		if networkaddr.IsVirtualInterface(iface) {
			return Detection{}, fmt.Errorf("%w: %s", ErrUnsupportedRouteInterface, iface)
		}
	}

	raw, err := c.provider.GetAddresses(ctx, iface)
	if err != nil {
		return Detection{}, fmt.Errorf("provider get addresses for %s: %w", iface, err)
	}

	valid := networkaddr.NormalizeAndFilterCandidates(raw, c.nowFunc())
	if len(valid) == 0 {
		return Detection{}, ErrNoValidAddress
	}

	// 检查当前确认的地址是否依然有效
	if currentConfirmed.IsValid() && currentConfirmed.Is6() && !currentConfirmed.Is4In6() {
		confirmedStr := currentConfirmed.String()
		for _, cand := range valid {
			if strings.EqualFold(cand.Address, confirmedStr) {
				return Detection{Interface: iface, Gateway: gateway, Address: currentConfirmed}, nil
			}
		}
	}

	// 排序并返回数值最小的首选地址
	networkaddr.SortAddresses(valid)
	bestIP, err := netip.ParseAddr(strings.TrimSpace(valid[0].Address))
	if err != nil || !bestIP.IsValid() {
		return Detection{}, fmt.Errorf("invalid candidate address format %q: %w", valid[0].Address, err)
	}

	return Detection{Interface: iface, Gateway: gateway, Address: bestIP}, nil
}
