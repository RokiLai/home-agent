package qualitygate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPipelineRejectsResultDirAtRepositoryRoot(t *testing.T) {
	repoDir := initTestGitRepo(t)
	_, exitCode, err := RunPipeline(PipelineOptions{RootDir: repoDir, ResultDir: repoDir, BaseRef: "HEAD"})
	if err == nil || exitCode != 2 {
		t.Fatalf("repository root result dir must be rejected, exit=%d err=%v", exitCode, err)
	}
}

func TestCollectChangedPackagesFailsForUnresolvablePackage(t *testing.T) {
	repoDir := initTestGitRepo(t)
	badDir := filepath.Join(repoDir, "broken")
	if err := os.MkdirAll(badDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "broken.go"), []byte("not go\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := collectChangedPackages(repoDir, []string{"broken/broken.go"})
	if err == nil || !strings.Contains(err.Error(), "go list") {
		t.Fatalf("unresolvable changed package must fail, got %v", err)
	}
}

func TestAggregatePipelineRejectsBlockingStatusesAndEvidenceErrors(t *testing.T) {
	for _, status := range []string{StatusFailed, StatusBlocked, StatusNotRun} {
		if aggregatePipelinePassed([]StepResult{{ID: "gate", Status: status}}, nil) {
			t.Errorf("status %s must not aggregate to passed", status)
		}
	}
	if aggregatePipelinePassed([]StepResult{{ID: "gate", Status: StatusPassed}}, errors.New("missing evidence")) {
		t.Error("evidence failure must not aggregate to passed")
	}
}

func TestCommandExitCodePreservesChildExitCode(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 7").Run()
	if got := commandExitCode(err); got != 7 {
		t.Fatalf("commandExitCode = %d, want 7", got)
	}
}

func TestPipelineRunsFullRaceRegressionExactlyOnce(t *testing.T) {
	repoDir := initTestGitRepo(t)
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "go-calls.log")
	binDir := t.TempDir()
	wrapper := filepath.Join(binDir, "go")
	wrapperContent := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellSingleQuote(logPath) + "\nexec " + shellSingleQuote(realGo) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(wrapperContent), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	write := func(rel, content string, mode os.FileMode) {
		path := filepath.Join(repoDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module testpkg\n\ngo 1.26\n", 0644)
	write("cmd/homeagent-agent/main.go", "package main\nfunc main() {}\n", 0644)
	write("internal/version/version.go", "package version\nvar Version = \"v1.0.0\"\nfunc Get() string {\n\treturn \"v1.0.0\"\n}\n", 0644)
	write("pkg/value.go", "package pkg\nfunc Value() int { return 1 }\n", 0644)
	write("pkg/value_test.go", "package pkg\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value() < 1 { t.Fail() } }\n", 0644)
	write("changes/once.yaml", "task: once\ndescription: count regression\nallowed_paths:\n  - changes/once.yaml\n  - pkg/**\n", 0644)
	write("scripts/check-diff-coverage.sh", "#!/bin/sh\n[ -s \"$3\" ] || exit 1\nprintf 'Diff Coverage: 100.0%% (1/1 statements)\\n'\n", 0755)
	runGitInDir(t, repoDir, "add", ".")
	runGitInDir(t, repoDir, "commit", "-m", "base")
	base := runGitInDir(t, repoDir, "rev-parse", "HEAD")
	write("pkg/value.go", "package pkg\nfunc Value() int { return 2 }\n", 0644)

	res, exitCode, err := RunPipeline(PipelineOptions{
		RootDir: repoDir, ChangeSpecPath: filepath.Join(repoDir, "changes", "once.yaml"),
		BaseRef: base, DiffCoverageThreshold: 60, ResultDir: t.TempDir(),
	})
	if err != nil || exitCode != 0 || res.Status != StatusPassed {
		t.Fatalf("pipeline failed: status=%v exit=%d err=%v", res.Status, exitCode, err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, call := range strings.Split(string(logData), "\n") {
		if strings.HasPrefix(call, "test -count=1 -race -coverprofile=") && strings.HasSuffix(call, " ./...") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("full race regression call count = %d, want 1; calls:\n%s", count, logData)
	}
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func TestPipeline_ExecutionInCleanRepo(t *testing.T) {
	repoDir := initTestGitRepo(t)

	// Create go.mod
	_ = os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module testpkg\n\ngo 1.26\n"), 0644)

	// Create a minimal change
	specContent := `task: test-pipeline
description: Test pipeline
allowed_paths:
  - changes/test-pipeline.yaml
  - pkg/hello/**
`
	specDir := filepath.Join(repoDir, "changes")
	_ = os.MkdirAll(specDir, 0755)
	specPath := filepath.Join(specDir, "test-pipeline.yaml")
	_ = os.WriteFile(specPath, []byte(specContent), 0644)

	pkgDir := filepath.Join(repoDir, "pkg/hello")
	_ = os.MkdirAll(pkgDir, 0755)
	_ = os.WriteFile(filepath.Join(pkgDir, "hello.go"), []byte("package hello\n\nfunc Greeting() string {\n\treturn \"hello\"\n}\n"), 0644)
	_ = os.WriteFile(filepath.Join(pkgDir, "hello_test.go"), []byte("package hello\n\nimport \"testing\"\n\nfunc TestGreeting(t *testing.T) {\n\tif Greeting() != \"hello\" {\n\t\tt.Fail()\n\t}\n}\n"), 0644)

	// Base version file
	verDir := filepath.Join(repoDir, "internal/version")
	_ = os.MkdirAll(verDir, 0755)
	_ = os.WriteFile(filepath.Join(verDir, "version.go"), []byte("package version\n\nvar Version = \"v0.6.5\"\nfunc Get() string {\n\treturn \"v0.6.5\"\n}\n"), 0644)
	agentDir := filepath.Join(repoDir, "cmd/homeagent-agent")
	_ = os.MkdirAll(agentDir, 0755)
	_ = os.WriteFile(filepath.Join(agentDir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)

	// Commit base files
	runGitInDir(t, repoDir, "add", ".")
	runGitInDir(t, repoDir, "commit", "-m", "setup test repo")
	baseCommit := runGitInDir(t, repoDir, "rev-parse", "HEAD")

	// Now modify hello.go
	_ = os.WriteFile(filepath.Join(pkgDir, "hello.go"), []byte("package hello\n\nfunc Greeting() string {\n\treturn \"world\"\n}\n"), 0644)
	_ = os.WriteFile(filepath.Join(pkgDir, "hello_test.go"), []byte("package hello\n\nimport \"testing\"\n\nfunc TestGreeting(t *testing.T) {\n\tif Greeting() != \"world\" {\n\t\tt.Fail()\n\t}\n}\n"), 0644)

	resultDir := t.TempDir()

	opts := PipelineOptions{
		RootDir:                repoDir,
		ChangeSpecPath:         specPath,
		BaseRef:                baseCommit,
		DiffCoverageThreshold:  50.0,
		ResultDir:              resultDir,
		SkipRegressionForTests: true, // For unit test isolation
	}

	res, exitCode, err := RunPipeline(opts)
	if err != nil {
		t.Fatalf("RunPipeline unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("expected exitCode 0, got %d", exitCode)
	}
	if res.Status != StatusPassed {
		t.Errorf("expected pipeline passed, got %s", res.Status)
	}
	if res.Task != "test-pipeline" {
		t.Errorf("expected task 'test-pipeline', got '%s'", res.Task)
	}
}

func TestPipeline_FastFailOnScopeViolation(t *testing.T) {
	repoDir := initTestGitRepo(t)
	_ = os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module testpkg\n\ngo 1.26\n"), 0644)
	runGitInDir(t, repoDir, "add", "go.mod")
	runGitInDir(t, repoDir, "commit", "-m", "add go.mod")

	specContent := `task: test-fail
description: Test fast fail
allowed_paths:
  - changes/test-fail.yaml
  - allowed/**
`
	specDir := filepath.Join(repoDir, "changes")
	_ = os.MkdirAll(specDir, 0755)
	specPath := filepath.Join(specDir, "test-fail.yaml")
	_ = os.WriteFile(specPath, []byte(specContent), 0644)

	// Untracked bad file
	_ = os.WriteFile(filepath.Join(repoDir, "bad.txt"), []byte("bad\n"), 0644)

	resultDir := t.TempDir()
	opts := PipelineOptions{
		RootDir:                repoDir,
		ChangeSpecPath:         specPath,
		BaseRef:                "HEAD",
		DiffCoverageThreshold:  60.0,
		ResultDir:              resultDir,
		SkipRegressionForTests: true,
	}

	res, exitCode, _ := RunPipeline(opts)
	if exitCode != 1 {
		t.Errorf("expected exitCode 1 on scope failure, got %d", exitCode)
	}
	if res.Status != StatusFailed {
		t.Errorf("expected pipeline status failed, got %s", res.Status)
	}

	// Verify later steps are marked not_run
	foundNotRun := false
	for _, s := range res.Steps {
		if s.Status == StatusNotRun {
			foundNotRun = true
			break
		}
	}
	if !foundNotRun {
		t.Errorf("expected downstream steps to be marked not_run on fast fail")
	}
}
