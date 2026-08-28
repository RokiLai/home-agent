// Package ddns 提供局域网 IPv6 动态域名解析（DDNS）对齐调和、前缀宽限期状态机以及 DNS 服务商驱动适配器。
package ddns

import (
	"context"
	"net/netip"
)

// DNSPublisher 定义用于查询、创建/更新（Upsert）以及删除 DNS AAAA 记录的服务提供者接口。
type DNSPublisher interface {
	GetAAAA(ctx context.Context, record string) ([]netip.Addr, error)
	UpsertAAAA(ctx context.Context, record string, address netip.Addr, ttl int) error
	DeleteAAAA(ctx context.Context, record string) error
}
