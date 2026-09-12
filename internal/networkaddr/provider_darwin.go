//go:build darwin

package networkaddr

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

type platformDefaultIPv6RouteResolver struct{}

func newPlatformDefaultIPv6RouteResolver() DefaultIPv6RouteResolver {
	return platformDefaultIPv6RouteResolver{}
}

func (platformDefaultIPv6RouteResolver) ResolveDefaultIPv6Route(ctx context.Context) (DefaultIPv6Route, error) {
	out, err := exec.CommandContext(ctx, "/sbin/route", "-n", "get", "-inet6", "default").Output()
	if err != nil {
		return DefaultIPv6Route{}, err
	}
	var route DefaultIPv6Route
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "interface":
			route.Interface = strings.TrimSpace(value)
		case "gateway":
			route.Gateway = strings.TrimSpace(value)
		}
	}
	if route.Interface == "" {
		return DefaultIPv6Route{}, errors.New("ipv6 default route has no interface")
	}
	return route, nil
}

// DarwinProvider 通过调用 macOS `ifconfig` 命令采集网络接口上的 IPv6 地址及详细标志。
type DarwinProvider struct {
	generic *GenericProvider
}

// NewDarwinProvider 创建面向 macOS 系统的 DarwinProvider 实例。
func NewDarwinProvider() *DarwinProvider {
	return &DarwinProvider{
		generic: NewGenericProvider(),
	}
}

// GetAddresses 获取活动网络接口的 IPv6 地址列表，并自动过滤临时和废弃地址。
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
