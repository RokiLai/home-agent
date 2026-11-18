package routermonitor

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
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

func TestCollectorSelection(t *testing.T) {
	for _, tc := range []struct {
		mode, host, router, wantRouter string
		wantArgs                       []string
		wantErr                        bool
	}{
		{mode: "", host: "", router: "", wantRouter: ""},
		{mode: "local", wantRouter: "device", wantArgs: []string{"/bin/ash", "/data/router-guard/router-guard.sh", "status"}},
		{mode: "local", router: "device", wantRouter: "device", wantArgs: []string{"/bin/ash", "/data/router-guard/router-guard.sh", "status"}},
		{mode: "local", router: "other", wantErr: true},
		{mode: "local", host: "alias", wantErr: true},
		{mode: "invalid", wantErr: true},
		{mode: "ssh", host: "alias", wantErr: true},
		{host: "alias", router: "target", wantRouter: "target"},
	} {
		c, err := NewCollector(tc.mode, tc.host, tc.router, "device")
		if (err != nil) != tc.wantErr || c.RouterID != tc.wantRouter {
			t.Fatalf("mode %q: collector=%+v err=%v", tc.mode, c, err)
		}
		if tc.wantArgs != nil && !reflect.DeepEqual(c.command(context.Background()).Args, tc.wantArgs) {
			t.Fatal("local command must use fixed executable, path and status argument")
		}
	}
}
