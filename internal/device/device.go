// Package device 提供核心设备数据模型、设备 ID 生成、字段合法性校验以及 IP 地址排序过滤工具。
package device

import (
	"crypto/sha256"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"homeagent/internal/wol"
)

// Device 表示已接入 HomeAgent 管理的主机设备模型，
// 包含其唯一身份标识、网络地址列表、SSH 凭据、状态同步记录以及 GitHub 集成属性。
type Device struct {
	ID                string    `json:"id"`
	Hostname          string    `json:"hostname"`
	Alias             string    `json:"alias,omitempty"`
	MAC               string    `json:"mac,omitempty"`
	AgentVersion      string    `json:"agent_version,omitempty"`
	OS                string    `json:"os"`
	Arch              string    `json:"arch"`
	SSHUser           string    `json:"ssh_user"`
	SSHPort           int       `json:"ssh_port"`
	PublicKey         string    `json:"public_key"`
	Addresses         []string  `json:"addresses"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	LastSeenAt        time.Time `json:"last_seen_at"`
	SyncStatus        string    `json:"sync_status,omitempty"`
	AppliedVersion    int64     `json:"applied_version,omitempty"`
	AppliedHash       string    `json:"applied_hash,omitempty"`
	SyncError         string    `json:"sync_error,omitempty"`
	SyncUpdatedAt     time.Time `json:"sync_updated_at,omitempty"`
	GitHubSyncEnabled bool      `json:"github_sync_enabled"`
	GitHubStatus      string    `json:"github_status,omitempty"`
	GitHubKeyID       int64     `json:"github_key_id,omitempty"`
	GitHubFingerprint string    `json:"github_fingerprint,omitempty"`
	GitHubUpdatedAt   time.Time `json:"github_updated_at,omitempty"`
	DeviceTokenHash   string        `json:"device_token_hash,omitempty"`
	ControlProtocols  []int         `json:"control_protocols,omitempty"`
	RuntimeFacts      *RuntimeFacts `json:"runtime,omitempty"`
}

// RuntimeFacts 包含 Agent 上报的系统运行指标快照。
type RuntimeFacts struct {
	ObservedAt           time.Time `json:"observed_at"`
	UptimeSeconds        int64     `json:"uptime_seconds,omitempty"`
	Load1                *float64  `json:"load_1,omitempty"`
	LogicalCPUCount      int       `json:"logical_cpu_count,omitempty"`
	MemoryTotalBytes     uint64    `json:"memory_total_bytes,omitempty"`
	MemoryAvailableBytes uint64    `json:"memory_available_bytes,omitempty"`
	DiskTotalBytes       uint64    `json:"disk_total_bytes,omitempty"`
	DiskAvailableBytes   uint64    `json:"disk_available_bytes,omitempty"`
	DiskMount            string    `json:"disk_mount,omitempty"`
}

// ShouldReceiveGitHubCredentials 判断该设备是否已启用并允许同步 GitHub OAuth 凭据和 SSH 密钥。
func ShouldReceiveGitHubCredentials(d Device) bool {
	return d.GitHubSyncEnabled
}

// GenerateRandomID 生成高强度全局唯一的服务端设备标识符（基于时间戳与安全随机数）。
func GenerateRandomID() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().Nanosecond())))
	return fmt.Sprintf("dev-%x", sum[:8])
}

// GenerateID 基于主机名与硬件 Machine ID 生成稳定、确定性且 URL 安全的设备唯一标识符。
func GenerateID(hostname, machineID string) string {
	host := strings.ToLower(strings.TrimSpace(hostname))
	host = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '-'
	}, host)
	host = strings.Trim(strings.Join(strings.FieldsFunc(host, func(r rune) bool { return r == '-' }), "-"), "-")
	if host == "" {
		host = "device"
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(hostname) + strings.TrimSpace(machineID)))
	return fmt.Sprintf("%s-%x", host, sum[:4])
}

// Validate 校验设备数据完整性，包括必填 ID、主机名、有效 SSH 端口、支持的公钥格式（ed25519/rsa/ecdsa）及 MAC 地址合法性。
func Validate(d Device) error {
	if strings.TrimSpace(d.ID) == "" || strings.TrimSpace(d.Hostname) == "" {
		return fmt.Errorf("id and hostname are required")
	}
	if strings.TrimSpace(d.SSHUser) == "" || d.SSHPort < 1 || d.SSHPort > 65535 {
		return fmt.Errorf("valid ssh_user and ssh_port are required")
	}
	fields := strings.Fields(d.PublicKey)
	if len(fields) < 2 || (fields[0] != "ssh-ed25519" && fields[0] != "ssh-rsa" && !strings.HasPrefix(fields[0], "ecdsa-sha2-")) {
		return fmt.Errorf("invalid SSH public key")
	}
	if strings.TrimSpace(d.MAC) != "" {
		if _, _, err := wol.ParseAndValidateMAC(d.MAC); err != nil {
			return fmt.Errorf("invalid mac address: %w", err)
		}
	}
	return nil
}

// FilterAndSortAddresses 过滤虚拟网卡、回环、链路本地以及容器网桥 IP，
// 并对有效地址按优先级确定性排序（私有 IPv4 > 公网 IPv4 > IPv6）。
func FilterAndSortAddresses(addresses []string) []string {
	seen := map[string]bool{}
	type ranked struct {
		value string
		rank  int
	}
	var values []ranked
	for _, raw := range addresses {
		ip := net.ParseIP(strings.Trim(strings.TrimSpace(raw), "[]"))
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || isVirtualOrProxyIP(ip) {
			continue
		}
		value := ip.String()
		if seen[value] {
			continue
		}
		seen[value] = true
		rank := 2
		if ip.To4() != nil && ip.IsPrivate() {
			rank = 0
		} else if ip.To4() != nil {
			rank = 1
		}
		values = append(values, ranked{value, rank})
	}
	sort.SliceStable(values, func(i, j int) bool { return values[i].rank < values[j].rank })
	result := make([]string, len(values))
	for i := range values {
		result[i] = values[i].value
	}
	return result
}

// isVirtualOrProxyIP 检查 IP 是否属于已知虚拟网卡、代理隧道或容器桥接段
// （如 Tailscale、Docker、WSL、Hyper-V、VMware、VirtualBox 或 IPv6 ULA 唯一本地地址段）。
func isVirtualOrProxyIP(ip net.IP) bool {
	v4 := ip.To4()
	if v4 != nil {
		// 过滤网络地址与广播地址 (例如 *.0 或 *.255)
		if v4[3] == 0 || v4[3] == 255 {
			return true
		}
		// APIPA 链路本地: 169.254.0.0/16
		if v4[0] == 169 && v4[1] == 254 {
			return true
		}
		// Tailscale / CGNAT: 100.64.0.0/10 (100.64.0.0 - 100.127.255.255)
		if v4[0] == 100 && (v4[1] >= 64 && v4[1] <= 127) {
			return true
		}
		// Docker / WSL / Hyper-V (172.17.x.x ~ 172.31.x.x)
		if v4[0] == 172 && (v4[1] >= 17 && v4[1] <= 31) {
			return true
		}
		// 常见虚拟机/网桥适配器 (VirtualBox, VMware, macOS bridge100-108, Parallels)
		if v4[0] == 192 && v4[1] == 168 {
			third := v4[2]
			if third == 56 || third == 65 || third == 97 || third == 107 || third == 117 ||
				third == 139 || third == 147 || third == 148 || third == 156 || third == 158 || third == 215 {
				return true
			}
		}
		return false
	}

	// IPv6 过滤:
	// 过滤 Tailscale / 虚拟网桥常用的 ULA 唯一本地地址 (fc00::/7，含 fd00::/8)
	if len(ip) == 16 && (ip[0]&0xfe == 0xfc) {
		return true
	}
	return false
}
