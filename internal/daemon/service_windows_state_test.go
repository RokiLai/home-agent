//go:build windows

package daemon

import (
	"testing"

	"golang.org/x/sys/windows/svc"
)

func TestParseWindowsServiceState(t *testing.T) {
	tests := []struct {
		state svc.State
		want  string
	}{
		{svc.Running, "running"},
		{svc.Stopped, "stopped"},
		{svc.StartPending, "start_pending"},
		{svc.StopPending, "stop_pending"},
		{svc.Paused, "paused"},
	}

	for _, tt := range tests {
		got := parseWindowsServiceState(tt.state)
		if got != tt.want {
			t.Errorf("parseWindowsServiceState(%v) = %q, want %q", tt.state, got, tt.want)
		}
	}
}
