package workflowperf

import (
	"strings"
	"testing"
)

func TestParseTestEventsAggregatesPackagesAndTests(t *testing.T) {
	report, err := ParseTestEvents(strings.NewReader(strings.Join([]string{
		`{"Time":"2026-09-13T00:00:00Z","Action":"run","Package":"homeagent/a","Test":"TestA"}`,
		`{"Time":"2026-09-13T00:00:01Z","Action":"pass","Package":"homeagent/a","Test":"TestA","Elapsed":1.0}`,
		`{"Time":"2026-09-13T00:00:02Z","Action":"pass","Package":"homeagent/a","Elapsed":2.0}`,
		`{"Time":"2026-09-13T00:00:03Z","Action":"fail","Package":"homeagent/b","Elapsed":3.0}`,
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcome != "failed" || len(report.Packages) != 2 || len(report.Tests) != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}
	if report.Packages[0].Package != "homeagent/b" || report.Packages[0].ElapsedSeconds != 3 {
		t.Fatalf("packages are not sorted slowest first: %#v", report.Packages)
	}
}

func TestParseTestEventsRejectsMalformedOrIncompleteStream(t *testing.T) {
	for _, events := range []string{
		"not-json\n",
		`{"Action":"pass","Package":""}` + "\n",
		`{"Action":"run","Package":"homeagent/a"}` + "\n",
	} {
		if _, err := ParseTestEvents(strings.NewReader(events)); err == nil {
			t.Fatalf("ParseTestEvents accepted invalid stream %q", events)
		}
	}
}
