//go:build darwin

package networkaddr

import (
	"context"
	"os/exec"
	"time"
)

type DarwinProvider struct{}

func NewDarwinProvider() *DarwinProvider {
	return &DarwinProvider{}
}

func (p *DarwinProvider) GetAddresses(ctx context.Context, ifaceName string) ([]ReportedIPv6Address, error) {
	var args []string
	if ifaceName != "" && ifaceName != "auto" {
		args = []string{ifaceName}
	}
	cmd := exec.CommandContext(ctx, "ifconfig", args...)
	out, err := cmd.Output()
	if err != nil {
		// Fallback to generic provider if ifconfig fails
		return NewGenericProvider().GetAddresses(ctx, ifaceName)
	}

	raw := ParseDarwinIfconfig(string(out), ifaceName)
	return NormalizeAndFilterCandidates(raw, time.Now().UTC()), nil
}
