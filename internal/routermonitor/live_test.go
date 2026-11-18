package routermonitor

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLatestSurvivesFailureAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	now := time.Unix(1000, 0)
	s := NewLatest(path)
	if err := s.Save(Report{Raw: "pause_until_uptime=0 current_uptime=100\nwan\thealthy\tok\nchecked_uptime\t90\n"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(Report{Failed: true}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	v, err := NewLatest(path).Read(now.Add(time.Minute))
	if err != nil || v.State != "collection_failed" || v.Snapshot == nil || !v.LastSuccess.Equal(now) {
		t.Fatalf("%+v %v", v, err)
	}
	v, err = s.Read(now.Add(5 * time.Minute))
	if err != nil || v.State != "stale" {
		t.Fatalf("%+v %v", v, err)
	}
	if err = s.Save(Report{Raw: "invalid"}, now); err == nil {
		t.Fatal("accepted invalid report")
	}
}
