package device

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFingerprintHardwareIdentityDoesNotPersistRawValue(t *testing.T) {
	raw := "11111111-2222-3333-4444-555555555555"
	got, err := FingerprintHardwareIdentity(bytes.Repeat([]byte{7}, 32), HardwareIdentityReport{Version: 1, Source: "io_platform_uuid", Value: raw})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), raw) || len(got.Fingerprint) != 64 {
		t.Fatalf("raw identity persisted or digest invalid: %s", encoded)
	}
}

func TestFingerprintHardwareIdentityRejectsUnsafeValues(t *testing.T) {
	for _, value := range []string{"", "not-a-uuid", "00000000-0000-0000-0000-000000000000"} {
		if _, err := FingerprintHardwareIdentity(bytes.Repeat([]byte{7}, 32), HardwareIdentityReport{Version: 1, Source: "dmi_product_uuid", Value: value}); err == nil {
			t.Fatalf("value %q accepted", value)
		}
	}
}

func TestGenerateIDStable(t *testing.T) {
	a := GenerateID("Example Laptop", "machine-1")
	b := GenerateID("Example Laptop", "machine-1")
	if a != b || a[:15] != "example-laptop-" {
		t.Fatalf("unexpected IDs %q %q", a, b)
	}
}

func TestFilterAndSortAddresses(t *testing.T) {
	got := FilterAndSortAddresses([]string{
		"127.0.0.1", "fe80::1", "169.254.1.20", "8.8.8.8", "192.168.1.4",
		"172.17.0.1", "192.168.1.4", "2001:db8::1", "100.114.254.60",
		"192.168.139.3", "192.168.215.0", "fd07:b51a:cc66::1",
	})
	want := []string{"192.168.1.4", "8.8.8.8", "2001:db8::1"}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	}
}

func TestValidateDevice(t *testing.T) {
	d := Device{
		ID:        "dev1",
		Hostname:  "host1",
		OS:        "darwin",
		Arch:      "arm64",
		SSHUser:   "root",
		SSHPort:   22,
		PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleValidKey",
	}
	if err := Validate(d); err != nil {
		t.Fatalf("unexpected error on valid device: %v", err)
	}

	// Test MAC validation in Validate
	d.MAC = "invalid-mac"
	if err := Validate(d); err == nil {
		t.Fatal("expected error on invalid MAC")
	}
	d.MAC = "02:00:00:11:22:33"
	if err := Validate(d); err != nil {
		t.Fatalf("unexpected error on valid MAC: %v", err)
	}
}

func TestDeviceUpgradeFactsFields(t *testing.T) {
	d := Device{
		ID:                      "dev-upg",
		Hostname:                "mac-mini",
		OS:                      "darwin",
		Arch:                    "arm64",
		SSHUser:                 "roki",
		SSHPort:                 22,
		PublicKey:               "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleValidKey",
		ControlProtocols:        []int{1, 2},
		UpgradeTransactionID:    "tx-12345",
		UpgradeFenceRevision:    42,
		UpgradeReleaseSequence:  100,
		ConfirmedManifestDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		RunningBundleDigest:     "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		UpgradeSecurityMode:     "v2_locked",
	}
	if err := Validate(d); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if d.UpgradeTransactionID != "tx-12345" || d.UpgradeFenceRevision != 42 || d.UpgradeSecurityMode != "v2_locked" {
		t.Fatalf("unexpected upgrade facts values: %+v", d)
	}
}

func TestSSHPortNormalizationContract(t *testing.T) {
	for _, port := range []int{1, 22, 2222, 65535} {
		d := Device{SSHPort: port}
		if err := NormalizeSSHPorts(&d); err != nil || d.SSHPortReported != port || d.SSHPortOverride != 0 || d.SSHPort != port {
			t.Fatalf("legacy port %d: %+v %v", port, d, err)
		}
		d.SSHPortOverride = 2200
		if err := NormalizeSSHPorts(&d); err != nil || d.SSHPort != 2200 || d.SSHPortReported != port {
			t.Fatalf("override: %+v %v", d, err)
		}
	}
	for _, d := range []Device{{SSHPort: 0}, {SSHPort: 65536}, {SSHPort: 22, SSHPortReported: -1}, {SSHPort: 22, SSHPortOverride: -1}, {SSHPort: 22, SSHPortOverride: 65536}} {
		if NormalizeSSHPorts(&d) == nil {
			t.Fatalf("invalid accepted: %+v", d)
		}
	}
}

func TestSSHPortLegacyAgentPayloadOmitsServerSettings(t *testing.T) {
	d := Device{ID: "legacy-agent", SSHPort: 2222}
	body, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "ssh_port_override") || strings.Contains(string(body), "ssh_port_reported") {
		t.Fatalf("server settings leaked into legacy agent registration: %s", body)
	}
}
