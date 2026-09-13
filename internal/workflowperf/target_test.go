package workflowperf

import (
	"strings"
	"testing"
)

func TestDecodeTargetsValidatesCanonicalManifest(t *testing.T) {
	targets, err := DecodeTargets(strings.NewReader(`[
		{"component":"server","goos":"linux","goarch":"amd64","output":"homeagent-server-linux-amd64"},
		{"component":"agent","goos":"windows","goarch":"arm64","output":"homeagent-agent-windows-arm64.exe"}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].ID() != "server-linux-amd64" || targets[1].ID() != "agent-windows-arm64" {
		t.Fatalf("unexpected targets: %#v", targets)
	}
}

func TestDecodeTargetsRejectsUnsafeOrDuplicateEntries(t *testing.T) {
	for _, manifest := range []string{
		`[{"component":"unknown","goos":"linux","goarch":"amd64","output":"x"}]`,
		`[{"component":"server","goos":"linux","goarch":"amd64","output":"../escape"}]`,
		`[{"component":"server","goos":"linux","goarch":"amd64","output":"homeagent-server-linux-amd64"},{"component":"server","goos":"linux","goarch":"amd64","output":"homeagent-server-linux-amd64"}]`,
		`[{"component":"server","goos":"","goarch":"amd64","output":"homeagent-server-linux-amd64"}]`,
		`not-json`,
	} {
		if _, err := DecodeTargets(strings.NewReader(manifest)); err == nil {
			t.Fatalf("DecodeTargets accepted invalid manifest %q", manifest)
		}
	}
}
