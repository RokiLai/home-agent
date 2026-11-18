package routermonitor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCollectFixedReadOnlyCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX SSH fixture")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	fixture := `#!/bin/sh
case "$*" in
 *"StrictHostKeyChecking=yes"*"/bin/ash /data/router-guard/router-guard.sh status")
 printf 'pause_until_uptime=0 current_uptime=100\nwan\thealthy\tok\nchecked_uptime\t90\n';;
 *) exit 1;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(fixture), 0700); err != nil {
		t.Fatal(err)
	}
	if r := Collect(context.Background(), "router-fixture"); r.Failed || r.Raw == "" {
		t.Fatalf("unexpected report: %+v", r)
	}
	if r := Collect(context.Background(), "-bad-option"); !r.Failed {
		t.Fatal("accepted a command option")
	}
	if err := Publish(context.Background(), "http://127.0.0.1", "agent", "router", "test", Report{Failed: true}); err == nil {
		t.Fatal("accepted plaintext upload")
	}
}
