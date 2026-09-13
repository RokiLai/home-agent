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
		"permissions:\n  contents: read",
		"github.event.pull_request.merge_commit_sha",
		"github.event.pull_request.base.sha",
		"actions/checkout@v4",
		"fetch-depth: 0",
		"actions/setup-go@v5",
		"cache-dependency-path: go.sum",
		"./scripts/run-workflow-tests.sh workflow-metrics/test",
		"./scripts/build-release-component.sh server",
		"./scripts/build-release-component.sh agent",
		"actions/upload-artifact@v4",
		"actions/download-artifact@v4",
		"permissions:\n      contents: write",
		"sha256sum",
		"gh release create",
		"--generate-notes",
		"--draft",
		"gh release download",
		"gh release edit",
		"--draft=false",
		"dist/homeagent-server-*",
		"dist/homeagent-agent-*",
		"steps.version.outputs.server_changed == 'true'",
		"steps.version.outputs.agent_changed == 'true'",
		"cancel-in-progress: false",
		"timeout-minutes:",
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
	manifestRaw, err := os.ReadFile(filepath.Join("..", "..", "configs", "release-targets.json"))
	if err != nil {
		t.Fatalf("read release target manifest: %v", err)
	}
	manifest := string(manifestRaw)
	buildScriptRaw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "build-release-component.sh"))
	if err != nil {
		t.Fatalf("read release build script: %v", err)
	}
	buildScript := string(buildScriptRaw)
	for _, fragment := range []string{"CGO_ENABLED=0", "homeagent/internal/version.ServerVersion=${version}", "homeagent/internal/version.AgentVersion=${version}", "go build -trimpath", "sha256sum -c"} {
		if !strings.Contains(buildScript, fragment) {
			t.Errorf("release build script missing contract fragment %q", fragment)
		}
	}
	for _, target := range targets {
		parts := strings.Fields(target)
		fragment := `{"component":"` + parts[0] + `","goos":"` + parts[1] + `","goarch":"` + parts[2] + `","output":"` + parts[3] + `"}`
		if strings.Count(manifest, fragment) != 1 {
			t.Errorf("release target %q must appear exactly once in manifest", target)
		}
	}

	forbidden := []string{
		"quality-gate.sh",
		"gh release delete",
		"docker",
		"deploy",
	}
	for _, fragment := range forbidden {
		if strings.Contains(strings.ToLower(workflow), fragment) {
			t.Errorf("release workflow contains forbidden extra action %q", fragment)
		}
	}
	if strings.Contains(workflow, "go test -race -p 1") || strings.Contains(workflow, "go test -count=1 -race -p 1") {
		t.Error("release workflow must not force package-level test serialization with -p 1")
	}
	if got := strings.Count(workflow, "./scripts/run-workflow-tests.sh workflow-metrics/test"); got != 1 {
		t.Errorf("shared full race regression runner must run exactly once, got %d", got)
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
		"Read component versions",
		"defaultServerVersion",
		"defaultAgentVersion",
		"Server and Agent versions must be non-empty",
		"base_server_version",
		"base_agent_version",
		`echo "server_changed=${server_changed}" >> "$GITHUB_OUTPUT"`,
		`echo "agent_changed=${agent_changed}" >> "$GITHUB_OUTPUT"`,
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

func TestReleaseWorkflowUsesSharedRegressionAndIndependentComponentJobs(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	workflow := string(raw)

	for _, job := range []struct {
		name     string
		required []string
	}{
		{"test:", []string{"needs: prepare", "./scripts/run-workflow-tests.sh workflow-metrics/test"}},
		{"build-server:", []string{"needs: prepare", "needs.prepare.outputs.server_changed == 'true'", "actions/upload-artifact@v4"}},
		{"build-agent:", []string{"needs: prepare", "needs.prepare.outputs.agent_changed == 'true'", "actions/upload-artifact@v4"}},
		{"release-server:", []string{"needs: [prepare, test, build-server]", "needs.test.result == 'success'", "needs.build-server.result == 'success'", "actions/download-artifact@v4"}},
		{"release-agent:", []string{"needs: [prepare, test, build-agent]", "needs.test.result == 'success'", "needs.build-agent.result == 'success'", "actions/download-artifact@v4"}},
	} {
		rest := releaseWorkflowJobBlock(workflow, job.name)
		if rest == "" {
			t.Errorf("release workflow missing job %q", job.name)
			continue
		}
		for _, fragment := range job.required {
			if !strings.Contains(rest, fragment) {
				t.Errorf("job %q missing contract fragment %q", job.name, fragment)
			}
		}
	}
}

func TestWorkflowPerformanceExperimentContract(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "workflows", "workflow-performance.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read performance workflow: %v", err)
	}
	workflow := string(raw)
	for _, fragment := range []string{
		"workflow_dispatch:",
		"group_id:",
		"scenario:",
		"permissions:\n  contents: read",
		"cache: false",
		"./scripts/run-workflow-tests.sh workflow-metrics/test",
		"./scripts/build-release-component.sh server",
		"./scripts/build-release-component.sh agent",
		"actions/upload-artifact@65462800fd760344b1a7b4382951275a0abb4808",
	} {
		if !strings.Contains(workflow, fragment) {
			t.Errorf("performance workflow missing contract fragment %q", fragment)
		}
	}
	for _, forbidden := range []string{"contents: write", "gh release", "pull_request:", "schedule:"} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("performance workflow contains forbidden fragment %q", forbidden)
		}
	}
	for _, action := range []string{"actions/checkout@", "actions/setup-go@", "actions/upload-artifact@"} {
		for _, line := range strings.Split(workflow, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "uses: "+action) {
				ref := strings.TrimPrefix(line, "uses: "+action)
				if len(ref) != 40 {
					t.Errorf("action %q must be pinned to a full commit SHA", line)
				}
			}
		}
	}
}

func TestReleaseWorkflowChecksTagConflictsBeforeExpensiveJobs(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "workflows", "release.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	workflow := string(raw)

	prepare := strings.Index(workflow, "\n  prepare:")
	testJob := strings.Index(workflow, "\n  test:")
	if prepare < 0 || testJob < 0 || prepare >= testJob {
		t.Fatal("prepare job with release preflight must precede the test job")
	}
	prepareBlock := workflow[prepare:testJob]
	for _, fragment := range []string{"Check release tag conflicts", "server-${SERVER_VERSION}", "agent-${AGENT_VERSION}", "gh api -i", "/releases/tags/"} {
		if !strings.Contains(prepareBlock, fragment) {
			t.Errorf("prepare job missing early tag-conflict contract %q", fragment)
		}
	}
}

func releaseWorkflowJobBlock(workflow, jobName string) string {
	lines := strings.Split(workflow, "\n")
	start := -1
	for index, line := range lines {
		if line == "  "+jobName {
			start = index
			continue
		}
		if start >= 0 && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && strings.HasSuffix(line, ":") {
			return strings.Join(lines[start:index], "\n")
		}
	}
	if start >= 0 {
		return strings.Join(lines[start:], "\n")
	}
	return ""
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
