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

// ParseUbusInterfaceStatus 解析 `ubus call network.interface.<iface> status` 命令返回的 JSON 输出并提取 IPv6 前缀。
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

// OpenWrtUbusProvider 通过调用 OpenWrt 系统的 ubus CLI 工具采集指定接口的局域网 IPv6 前缀。
type OpenWrtUbusProvider struct {
	// InterfaceName 指定要监控的 OpenWrt 网络接口名称（默认为 "lan"）。
	InterfaceName string
}

// NewOpenWrtUbusProvider 创建 OpenWrt ubus 前缀采集提供器。
func NewOpenWrtUbusProvider(ifaceName string) *OpenWrtUbusProvider {
	if ifaceName == "" {
		ifaceName = "lan"
	}
	return &OpenWrtUbusProvider{InterfaceName: ifaceName}
}

// GetPrefixes 执行 ubus 调用并返回经过解析和规范化的 IPv6 前缀切片。
func (p *OpenWrtUbusProvider) GetPrefixes(ctx context.Context) ([]ReportedIPv6Prefix, error) {
	cmd := exec.CommandContext(ctx, "ubus", "call", "network.interface."+p.InterfaceName, "status")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ubus command failed: %w", err)
	}
	return ParseUbusInterfaceStatus(out, time.Now().UTC())
}
