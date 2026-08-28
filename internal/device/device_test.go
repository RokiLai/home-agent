package device

import "testing"

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
