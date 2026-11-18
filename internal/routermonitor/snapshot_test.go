package routermonitor

import (
	"strings"
	"testing"
)

func TestParseSnapshot(t *testing.T) {
	raw := "pause_until_uptime=0 current_uptime=22702\nwan\thealthy\tok\nlearned_sets\tfailed\tmissing\nfuture\tnew_state\tdetail\nchecked_uptime\t22650\n"
	s, err := ParseSnapshot(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode != "monitoring" || s.CheckedUptime != 22650 || s.CurrentUptime != 22702 || len(s.Checks) != 3 {
		t.Fatalf("unexpected snapshot: %+v", s)
	}
	if s.Checks[1] != (Check{"learned_sets", "failed", "missing"}) || s.Checks[2].Status != "unknown" {
		t.Fatalf("unexpected checks: %+v", s.Checks)
	}
}

func TestSnapshotModes(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"pause_until_uptime=900 current_uptime=700\n", "paused"},
		{"pause_until_uptime=0 current_uptime=12\nNo completed health cycle yet.\n", "waiting"},
		{"pause_until_uptime=0 current_uptime=700\nchecked_uptime\t200\n", "stale"},
	} {
		s, err := ParseSnapshot(strings.NewReader(tc.input))
		if err != nil || s.Mode != tc.want {
			t.Errorf("got %+v, %v; want %s", s, err, tc.want)
		}
	}
}

func TestRejectInvalidSnapshots(t *testing.T) {
	for _, raw := range []string{
		"", "not a header", "pause_until_uptime=0 current_uptime=-2\n",
		"pause_until_uptime=0 current_uptime=100\nchecked_uptime\t200\n",
		"pause_until_uptime=0 current_uptime=100\nwan\thealthy\tok\n",
		"pause_until_uptime=0 current_uptime=100\nwan\thealthy\tok\nwan\tfailed\terr\nchecked_uptime\t50\n",
	} {
		if _, err := ParseSnapshot(strings.NewReader(raw)); err == nil {
			t.Errorf("accepted malformed snapshot %q", raw)
		}
	}
}
