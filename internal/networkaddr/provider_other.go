//go:build !darwin

package networkaddr

import (
	"context"
)

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
