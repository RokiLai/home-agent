package device

import (
	"crypto/sha256"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

type Device struct {
	ID         string    `json:"id"`
	Hostname   string    `json:"hostname"`
	OS         string    `json:"os"`
	Arch       string    `json:"arch"`
	SSHUser    string    `json:"ssh_user"`
	SSHPort    int       `json:"ssh_port"`
	PublicKey  string    `json:"public_key"`
	Addresses  []string  `json:"addresses"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	LastSeenAt    time.Time `json:"last_seen_at"`
	SyncStatus    string    `json:"sync_status,omitempty"`
	AppliedVersion int64     `json:"applied_version,omitempty"`
	AppliedHash   string    `json:"applied_hash,omitempty"`
	SyncError     string    `json:"sync_error,omitempty"`
	SyncUpdatedAt time.Time `json:"sync_updated_at,omitempty"`
}

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
	return nil
}

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

func isVirtualOrProxyIP(ip net.IP) bool {
	v4 := ip.To4()
	if v4 != nil {
		// Filter network and broadcast addresses (e.g. *.0 or *.255)
		if v4[3] == 0 || v4[3] == 255 {
			return true
		}
		// APIPA Link-Local: 169.254.0.0/16
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
		// Common VM / Bridge adapters (VirtualBox, VMware, macOS bridge100-108, Parallels)
		if v4[0] == 192 && v4[1] == 168 {
			third := v4[2]
			if third == 56 || third == 65 || third == 97 || third == 107 || third == 117 ||
				third == 139 || third == 147 || third == 148 || third == 156 || third == 158 || third == 215 {
				return true
			}
		}
		return false
	}

	// IPv6 filtering:
	// Filter ULA (Unique Local Address fc00::/7, including fd00::/8) used by Tailscale / virtual bridges
	if len(ip) == 16 && (ip[0]&0xfe == 0xfc) {
		return true
	}
	return false
}
