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
		"go test -race ./...",
		"CGO_ENABLED=0",
		"homeagent/internal/version.${version_var}=${VERSION}",
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

func TestReleaseWorkflowVersionExtraction(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	workflow := string(raw)

	// 断言工作流包含版本提取与防空检查
	requiredFragments := []string{
		"Read bridge component versions",
		"defaultServerVersion",
		"defaultAgentVersion",
		"Bridge release requires equal non-empty Server and Agent versions",
	}
	for _, frag := range requiredFragments {
		if !strings.Contains(workflow, frag) {
			t.Errorf("release workflow missing required version extraction fragment %q", frag)
		}
	}

	// 验证在当前 version.go 文件上的实际提取结果
	versionGoPath := filepath.Join("..", "..", "internal", "version", "version.go")
	versionGoContent, err := os.ReadFile(versionGoPath)
	if err != nil {
		t.Fatalf("read version.go: %v", err)
	}

	extracted := extractVersionFromContent(string(versionGoContent))
	if extracted == "" || !strings.HasPrefix(extracted, "v") {
		t.Fatalf("expected valid semantic version starting with 'v', got %q", extracted)
	}

	// 负例断言：损坏或缺失版本号时提取必须失败并返回空
	corrupted := "package version\nvar other = 123\n"
	if bad := extractVersionFromContent(corrupted); bad != "" {
		t.Fatalf("expected empty version for corrupted content, got %q", bad)
	}
}

func extractVersionFromContent(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "const defaultAgentVersion = \"") {
			parts := strings.Split(line, "\"")
			if len(parts) >= 2 {
				return parts[1]
			}
		}
	}
	return ""
}
