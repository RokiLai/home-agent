//go:build !darwin

package networkaddr

import (
	"context"
)

type DarwinProvider struct{}

func NewDarwinProvider() AddressProvider {
	return NewGenericProvider()
}

func (p *DarwinProvider) GetAddresses(ctx context.Context, ifaceName string) ([]ReportedIPv6Address, error) {
	return NewGenericProvider().GetAddresses(ctx, ifaceName)
}
