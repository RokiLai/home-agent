package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunTargetsFiltersAndPrintsStableTSV(t *testing.T) {
	manifest := writeTempFile(t, `[
		{"component":"server","goos":"linux","goarch":"amd64","output":"homeagent-server-linux-amd64"},
		{"component":"agent","goos":"darwin","goarch":"arm64","output":"homeagent-agent-darwin-arm64"}
	]`)
	var output bytes.Buffer
	if err := run([]string{"targets", "-manifest", manifest, "-component", "agent"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "agent\tdarwin\tarm64\thomeagent-agent-darwin-arm64\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunTestReportWritesJSONAndRejectsBadCommand(t *testing.T) {
	events := `{"Action":"pass","Package":"homeagent/a","Elapsed":1.5}` + "\n"
	var output bytes.Buffer
	if err := run([]string{"test-report"}, strings.NewReader(events), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"outcome": "passed"`) || !strings.Contains(output.String(), `"elapsed_seconds": 1.5`) {
		t.Fatalf("unexpected report: %s", output.String())
	}
	if err := run([]string{"unknown"}, strings.NewReader(""), &output); err == nil {
		t.Fatal("unknown command must fail")
	}
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
