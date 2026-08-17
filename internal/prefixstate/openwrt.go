package prefixstate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
	"time"
)

type ubusPrefixEntry struct {
	Address   string `json:"address"`
	Mask      int    `json:"mask"`
	Preferred int64  `json:"preferred"`
	Valid     int64  `json:"valid"`
}

type ubusInterfaceStatus struct {
	Up                   bool              `json:"up"`
	IPv6Prefix           []ubusPrefixEntry `json:"ipv6-prefix"`
	IPv6PrefixAssignment []ubusPrefixEntry `json:"ipv6-prefix-assignment"`
}

// ParseUbusInterfaceStatus parses JSON from `ubus call network.interface.<iface> status`.
func ParseUbusInterfaceStatus(data []byte, now time.Time) ([]ReportedIPv6Prefix, error) {
	var status ubusInterfaceStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, fmt.Errorf("failed to parse ubus json: %w", err)
	}

	entries := status.IPv6PrefixAssignment
	if len(entries) == 0 {
		entries = status.IPv6Prefix
	}

	var results []ReportedIPv6Prefix
	for _, entry := range entries {
		if strings.TrimSpace(entry.Address) == "" {
			continue
		}
		mask := entry.Mask
		if mask <= 0 || mask > 128 {
			mask = 64
		}

		rawPref := fmt.Sprintf("%s/%d", entry.Address, mask)
		prefix, err := netip.ParsePrefix(rawPref)
		if err != nil {
			continue
		}

		var prefUntil *time.Time
		if entry.Preferred > 0 {
			t := now.Add(time.Duration(entry.Preferred) * time.Second)
			prefUntil = &t
		}

		var validUntil *time.Time
		if entry.Valid > 0 {
			t := now.Add(time.Duration(entry.Valid) * time.Second)
			validUntil = &t
		}

		results = append(results, ReportedIPv6Prefix{
			Prefix:         prefix.Masked().String(),
			PreferredUntil: prefUntil,
			ValidUntil:     validUntil,
		})
	}

	return NormalizePrefixes(results, now), nil
}

// OpenWrtUbusProvider fetches LAN prefixes using `ubus call network.interface.<iface> status`.
type OpenWrtUbusProvider struct {
	InterfaceName string
}

func NewOpenWrtUbusProvider(ifaceName string) *OpenWrtUbusProvider {
	if ifaceName == "" {
		ifaceName = "lan"
	}
	return &OpenWrtUbusProvider{InterfaceName: ifaceName}
}

func (p *OpenWrtUbusProvider) GetPrefixes(ctx context.Context) ([]ReportedIPv6Prefix, error) {
	cmd := exec.CommandContext(ctx, "ubus", "call", "network.interface."+p.InterfaceName, "status")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ubus command failed: %w", err)
	}
	return ParseUbusInterfaceStatus(out, time.Now().UTC())
}
