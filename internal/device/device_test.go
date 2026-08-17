package device

import "testing"

func TestGenerateIDStable(t *testing.T) {
	a := GenerateID("Roki MacBook", "machine-1")
	b := GenerateID("Roki MacBook", "machine-1")
	if a != b || a[:13] != "roki-macbook-" {
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
