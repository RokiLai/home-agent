package domainbinding

import "testing"

func TestNormalizeFQDNEnforcesManagedSuffixLabelBoundary(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{"MBP.Rokilai.Online.", "mbp.rokilai.online", true},
		{"rokilai.online", "rokilai.online", true},
		{"fake-rokilai.online", "", false},
		{"rokilai.online.evil", "", false},
		{"example.com.evil", "", false},
	}
	for _, test := range tests {
		got, err := NormalizeFQDN(test.input, ManagedSuffix)
		if test.ok && (err != nil || got != test.want) {
			t.Fatalf("NormalizeFQDN(%q) = %q, %v", test.input, got, err)
		}
		if !test.ok && err == nil {
			t.Fatalf("NormalizeFQDN(%q) unexpectedly accepted", test.input)
		}
	}
}
