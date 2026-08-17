package networkaddr

import (
	"context"
	"net"
	"net/netip"
	"runtime"
	"strings"
	"time"
)

// AddressProvider discovers IPv6 addresses on a local interface.
type AddressProvider interface {
	GetAddresses(ctx context.Context, iface string) ([]ReportedIPv6Address, error)
}

// NewDefaultProvider returns the platform-specific default AddressProvider.
func NewDefaultProvider() AddressProvider {
	if runtime.GOOS == "darwin" {
		return NewDarwinProvider()
	}
	return NewGenericProvider()
}

// GenericProvider is a cross-platform fallback provider using net.Interfaces.
type GenericProvider struct{}

func NewGenericProvider() *GenericProvider {
	return &GenericProvider{}
}

func (p *GenericProvider) GetAddresses(ctx context.Context, ifaceName string) ([]ReportedIPv6Address, error) {
	var ifaces []net.Interface
	if ifaceName != "" && ifaceName != "auto" {
		ifi, err := net.InterfaceByName(ifaceName)
		if err != nil {
			return nil, err
		}
		ifaces = []net.Interface{*ifi}
	} else {
		all, err := net.Interfaces()
		if err != nil {
			return nil, err
		}
		for _, ifi := range all {
			if (ifi.Flags&net.FlagUp) != 0 && (ifi.Flags&net.FlagLoopback) == 0 {
				ifaces = append(ifaces, ifi)
			}
		}
	}

	var results []ReportedIPv6Address
	for _, ifi := range ifaces {
		if (ifi.Flags & net.FlagUp) == 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, raw := range addrs {
			ipNet, ok := raw.(*net.IPNet)
			if !ok {
				continue
			}
			ipStr := ipNet.IP.String()
			ip, err := netip.ParseAddr(ipStr)
			if err != nil || !ip.IsValid() || !ip.Is6() || ip.Is4In6() {
				continue
			}
			ones, _ := ipNet.Mask.Size()
			results = append(results, ReportedIPv6Address{
				Address:      ip.String(),
				Interface:    ifi.Name,
				PrefixLength: ones,
			})
		}
	}

	return NormalizeAndFilterCandidates(results, time.Now().UTC()), nil
}

// ParseDarwinIfconfig parses output from macOS `ifconfig <interface>`.
func ParseDarwinIfconfig(output string, ifaceName string) []ReportedIPv6Address {
	var results []ReportedIPv6Address
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "inet6 ") {
			continue
		}
		// Format example: inet6 240e:1234:5678:10::20 prefixlen 64 [scopeid 0x4] [flags]
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		addrStr := fields[1]
		if idx := strings.Index(addrStr, "%"); idx != -1 {
			addrStr = addrStr[:idx]
		}
		prefixLen := 64
		isTemp := false
		isDeprecated := false

		for i := 2; i < len(fields); i++ {
			if fields[i] == "prefixlen" && i+1 < len(fields) {
				if pLen, err := netip.ParseAddr(fields[i+1]); err == nil {
					_ = pLen
				}
				// parse integer
				var plen int
				for _, c := range fields[i+1] {
					if c >= '0' && c <= '9' {
						plen = plen*10 + int(c-'0')
					} else {
						break
					}
				}
				if plen > 0 {
					prefixLen = plen
				}
			}
			if strings.Contains(fields[i], "temporary") {
				isTemp = true
			}
			if strings.Contains(fields[i], "deprecated") {
				isDeprecated = true
			}
			if strings.Contains(fields[i], "tentative") || strings.Contains(fields[i], "duplicated") {
				isDeprecated = true
			}
		}

		results = append(results, ReportedIPv6Address{
			Address:      addrStr,
			Interface:    ifaceName,
			PrefixLength: prefixLen,
			Temporary:    isTemp,
			Deprecated:   isDeprecated,
		})
	}
	return results
}
