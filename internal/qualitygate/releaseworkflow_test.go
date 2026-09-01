package qualitygate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseWorkflowContract(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	workflow := string(raw)

	required := []string{
		"pull_request:",
		"branches: [main]",
		"types: [closed]",
		"github.event.pull_request.merged == true",
		"github.event.pull_request.head.ref == 'dev'",
		"permissions:\n  contents: write",
		"github.event.pull_request.merge_commit_sha",
		"actions/checkout@v4",
		"actions/setup-go@v5",
		"CGO_ENABLED=0",
		"homeagent/internal/version.Version=${VERSION}",
		"sha256sum",
		"gh release create",
		"--generate-notes",
		"dist/*",
	}
	for _, fragment := range required {
		if !strings.Contains(workflow, fragment) {
			t.Errorf("release workflow missing required contract fragment %q", fragment)
		}
	}

	targets := []string{
		"server linux amd64 homeagent-server-linux-amd64",
		"server linux arm64 homeagent-server-linux-arm64",
		"server linux arm homeagent-server-linux-arm",
		"server darwin amd64 homeagent-server-darwin-amd64",
		"server darwin arm64 homeagent-server-darwin-arm64",
		"server windows amd64 homeagent-server-windows-amd64.exe",
		"server windows arm64 homeagent-server-windows-arm64.exe",
		"agent linux amd64 homeagent-agent-linux-amd64",
		"agent linux arm64 homeagent-agent-linux-arm64",
		"agent linux arm homeagent-agent-linux-arm",
		"agent linux mips homeagent-agent-linux-mips",
		"agent linux mipsle homeagent-agent-linux-mipsle",
		"agent darwin amd64 homeagent-agent-darwin-amd64",
		"agent darwin arm64 homeagent-agent-darwin-arm64",
		"agent windows amd64 homeagent-agent-windows-amd64.exe",
		"agent windows arm64 homeagent-agent-windows-arm64.exe",
	}
	for _, target := range targets {
		if strings.Count(workflow, target) != 1 {
			t.Errorf("release target %q must appear exactly once", target)
		}
	}

	forbidden := []string{
		"go test",
		"quality-gate.sh",
		"gh release delete",
		"gh release edit",
		"docker",
		"deploy",
	}
	for _, fragment := range forbidden {
		if strings.Contains(strings.ToLower(workflow), fragment) {
			t.Errorf("release workflow contains forbidden extra action %q", fragment)
		}
	}
}
