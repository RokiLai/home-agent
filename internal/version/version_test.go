package version

import "testing"

func TestGet(t *testing.T) {
	orig := Version
	defer func() { Version = orig }()

	Version = "v0.6.10"
	if got := Get(); got != "v0.6.10" {
		t.Fatalf("Get() = %q, want v0.6.10", got)
	}

	Version = ""
	if got := Get(); got != "v0.6.10" {
		t.Fatalf("Get() with empty = %q, want v0.6.10", got)
	}

	Version = "   "
	if got := Get(); got != "v0.6.10" {
		t.Fatalf("Get() with whitespace = %q, want v0.6.10", got)
	}
}
