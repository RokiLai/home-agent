// Package wol 实现用于远程唤醒网络主机的网络唤醒（Wake-on-LAN）协议。
// 提供 MAC 地址合法性校验、Magic Packet（魔术包）构造、局域网广播地址自动发现与 UDP 冗余重发能力。
package wol

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// MagicPacket 表示 102 字节的标准 WOL 魔术包（由 6 字节的 0xFF 与目标 MAC 地址连续重复 16 次构成）。
type MagicPacket [102]byte

// Options 定义发送 WOL 魔术包的配置参数。
type Options struct {
	BroadcastAddrs []string      // 自定义广播目标地址（例如 "192.168.1.255:9", "255.255.255.255:9"）
	TargetIPs      []string      // 目标设备的 IPv4 地址列表，用于自动推导 /24 子网广播地址
	BurstCount     int           // 突发发送数据包的次数（默认 3 次）
	BurstInterval  time.Duration // 每次突发发送之间的间隔时间（默认 50ms）
	Port           int           // 目标 UDP 端口（默认 9）
}

// ParseAndValidateMAC 解析并校验 IEEE 802 48位 MAC 地址。
// 验证 MAC 地址为严格 6 字节，且过滤全零、全 F（广播）及组播地址。
// 返回规范化的 HardwareAddr 及标准小写冒号分隔字符串。
func ParseAndValidateMAC(macStr string) (net.HardwareAddr, string, error) {
	clean := strings.TrimSpace(macStr)
	if clean == "" {
		return nil, "", errors.New("empty MAC address")
	}

	hw, err := net.ParseMAC(clean)
	if err != nil {
		return nil, "", fmt.Errorf("invalid MAC address format %q: %w", macStr, err)
	}

	if len(hw) != 6 {
		return nil, "", fmt.Errorf("MAC address must be 6 bytes (got %d bytes for %q)", len(hw), macStr)
	}

	// 拒绝全零 MAC: 00:00:00:00:00:00
	isZero := true
	for _, b := range hw {
		if b != 0x00 {
			isZero = false
			break
		}
	}
	if isZero {
		return nil, "", fmt.Errorf("MAC address cannot be all zeros (%s)", hw.String())
	}

	// 拒绝广播 MAC: ff:ff:ff:ff:ff:ff
	isBroadcast := true
	for _, b := range hw {
		if b != 0xff {
			isBroadcast = false
			break
		}
	}
	if isBroadcast {
		return nil, "", fmt.Errorf("MAC address cannot be broadcast address (%s)", hw.String())
	}

	// 拒绝组播 MAC: 首字节最低有效位为 1
	if (hw[0] & 0x01) != 0 {
		return nil, "", fmt.Errorf("MAC address cannot be multicast address (%s)", hw.String())
	}

	normalized := strings.ToLower(hw.String())
	return hw, normalized, nil
}

// BuildMagicPacket 针对指定 MAC 地址构造 102 字节的 Magic Packet 唤醒包。
func BuildMagicPacket(macStr string) (MagicPacket, error) {
	hw, _, err := ParseAndValidateMAC(macStr)
	if err != nil {
		return MagicPacket{}, err
	}

	var packet MagicPacket
	// 前 6 字节为 0xFF
	for i := 0; i < 6; i++ {
		packet[i] = 0xFF
	}

	// 后续重复 16 次目标 MAC 地址
	for i := 0; i < 16; i++ {
		copy(packet[6+i*6:6+(i+1)*6], hw)
	}

	return packet, nil
}

// DefaultBroadcastAddresses 自动探测本地所有活动网卡的 IPv4 子网广播地址，并附带有限广播地址 255.255.255.255。
func DefaultBroadcastAddresses(defaultPort int) []string {
	if defaultPort <= 0 {
		defaultPort = 9
	}

	seen := map[string]bool{}
	var result []string

	addAddr := func(ipStr string) {
		addr := net.JoinHostPort(ipStr, fmt.Sprintf("%d", defaultPort))
		if !seen[addr] {
			seen[addr] = true
			result = append(result, addr)
		}
	}

	// 始终添加受限广播地址
	addAddr("255.255.255.255")

	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			// 跳过未启用或回环接口
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}

			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}

			for _, a := range addrs {
				ipNet, ok := a.(*net.IPNet)
				if !ok || ipNet.IP == nil {
					continue
				}
				v4 := ipNet.IP.To4()
				if v4 == nil || v4.IsLoopback() {
					continue
				}

				mask := ipNet.Mask
				if len(mask) == 4 {
					bcast := net.IPv4(
						v4[0]|^mask[0],
						v4[1]|^mask[1],
						v4[2]|^mask[2],
						v4[3]|^mask[3],
					)
					addAddr(bcast.String())
				}
			}
		}
	}

	return result
}

// InferSubnetBroadcast 根据目标设备的 IPv4 地址推导可能的 /24 子网广播地址（如 x.y.z.255）。
func InferSubnetBroadcast(targetIPs []string, defaultPort int) []string {
	if defaultPort <= 0 {
		defaultPort = 9
	}
	var addrs []string
	seen := map[string]bool{}

	for _, raw := range targetIPs {
		ip := net.ParseIP(strings.Trim(strings.TrimSpace(raw), "[]"))
		if ip == nil {
			continue
		}
		v4 := ip.To4()
		if v4 == nil || v4.IsLoopback() {
			continue
		}
		// 默认推导 /24 广播: x.y.z.255
		bcast := net.IPv4(v4[0], v4[1], v4[2], 255)
		addr := net.JoinHostPort(bcast.String(), fmt.Sprintf("%d", defaultPort))
		if !seen[addr] {
			seen[addr] = true
			addrs = append(addrs, addr)
		}
	}
	return addrs
}

// Wake 构建并向指定 MAC 地址发送 WOL 网络唤醒魔术包。
func Wake(macStr string, opts *Options) error {

	packet, err := BuildMagicPacket(macStr)
	if err != nil {
		return err
	}

	port := 9
	burstCount := 3
	burstInterval := 50 * time.Millisecond

	var targets []string

	if opts != nil {
		if opts.Port > 0 {
			port = opts.Port
		}
		if opts.BurstCount > 0 {
			burstCount = opts.BurstCount
		}
		if opts.BurstInterval > 0 {
			burstInterval = opts.BurstInterval
		}
		if len(opts.BroadcastAddrs) > 0 {
			for _, b := range opts.BroadcastAddrs {
				b = strings.TrimSpace(b)
				if b == "" {
					continue
				}
				if !strings.Contains(b, ":") {
					b = net.JoinHostPort(b, fmt.Sprintf("%d", port))
				}
				targets = append(targets, b)
			}
		}
		if len(opts.TargetIPs) > 0 {
			inferred := InferSubnetBroadcast(opts.TargetIPs, port)
			targets = append(targets, inferred...)
		}
	}

	if len(targets) == 0 {
		targets = DefaultBroadcastAddresses(port)
	}

	// Deduplicate targets
	seen := map[string]bool{}
	var uniqueTargets []string
	for _, t := range targets {
		if !seen[t] {
			seen[t] = true
			uniqueTargets = append(uniqueTargets, t)
		}
	}

	if len(uniqueTargets) == 0 {
		uniqueTargets = []string{fmt.Sprintf("255.255.255.255:%d", port)}
	}

	var lastErr error
	sentSuccess := 0

	for i := 0; i < burstCount; i++ {
		for _, addrStr := range uniqueTargets {
			dst, err := net.ResolveUDPAddr("udp4", addrStr)
			if err != nil {
				lastErr = err
				continue
			}

			conn, err := net.DialUDP("udp4", nil, dst)
			if err != nil {
				lastErr = err
				continue
			}

			_, err = conn.Write(packet[:])
			_ = conn.Close()
			if err != nil {
				lastErr = err
			} else {
				sentSuccess++
			}
		}

		if i < burstCount-1 && burstInterval > 0 {
			time.Sleep(burstInterval)
		}
	}

	if sentSuccess == 0 && lastErr != nil {
		return fmt.Errorf("failed to send WOL packet: %w", lastErr)
	}

	return nil
}
