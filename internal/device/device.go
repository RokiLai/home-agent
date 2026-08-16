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
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
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
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || isDockerOrVM(ip) {
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

func isDockerOrVM(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	return v4[0] == 172 && (v4[1] == 17 || v4[1] == 18) || v4[0] == 192 && v4[1] == 168 && (v4[2] == 56 || v4[2] == 65)
}
