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
	got := FilterAndSortAddresses([]string{"127.0.0.1", "fe80::1", "8.8.8.8", "192.168.1.4", "172.17.0.1", "192.168.1.4", "2001:db8::1"})
	want := []string{"192.168.1.4", "8.8.8.8", "2001:db8::1"}
	if len(got) != len(want) {
		t.Fatalf("got %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v", got)
		}
	}
}
