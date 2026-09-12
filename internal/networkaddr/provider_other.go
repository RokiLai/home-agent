//go:build !darwin

package networkaddr

import (
	"context"
	"errors"
)

type platformDefaultIPv6RouteResolver struct{}

func newPlatformDefaultIPv6RouteResolver() DefaultIPv6RouteResolver {
	return platformDefaultIPv6RouteResolver{}
}

func (platformDefaultIPv6RouteResolver) ResolveDefaultIPv6Route(context.Context) (DefaultIPv6Route, error) {
	return DefaultIPv6Route{}, errors.New("automatic default ipv6 route resolution is only supported on macOS")
}

// DarwinProvider 在非 macOS 平台上的桩实现，直接回退到通用 GenericProvider。
type DarwinProvider struct {
	generic *GenericProvider
}

// NewDarwinProvider 创建非 macOS 平台上的桩 DarwinProvider 实例。
func NewDarwinProvider() *DarwinProvider {
	return &DarwinProvider{
		generic: NewGenericProvider(),
	}
}

// GetAddresses 直接委托给通用 GenericProvider 探测 IPv6 地址。
func (p *DarwinProvider) GetAddresses(ctx context.Context, ifaceName string) ([]ReportedIPv6Address, error) {
	return p.generic.GetAddresses(ctx, ifaceName)
}
