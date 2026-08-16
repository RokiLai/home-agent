package acl

import (
	"homeagent/internal/device"
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acl.yaml")
	if err := os.WriteFile(path, []byte("default_policy: deny\ndevices:\n  b:\n    allow:\n      - a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	all := []device.Device{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got := p.Resolve("b", all)
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("got %#v", got)
	}
	if got := p.Resolve("c", all); len(got) != 0 {
		t.Fatalf("deny got %#v", got)
	}
}

func TestAllowAllExcludesSelf(t *testing.T) {
	p := Policy{DefaultAllow: true, Devices: map[string][]string{}}
	got := p.Resolve("a", []device.Device{{ID: "a"}, {ID: "b"}})
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("got %#v", got)
	}
}
