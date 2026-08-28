package qualitygate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCalculateDiffCoveragePreservesSpecialPaths(t *testing.T) {
	repoDir := initTestGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module testpkg\n\ngo 1.26\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(repoDir, "special")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	names := []string{"space name.go", "tab\tname.go", "非ASCII.go"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package special\nfunc value() int { return 1 }\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	runGitInDir(t, repoDir, "add", ".")
	runGitInDir(t, repoDir, "commit", "-m", "base")
	base := runGitInDir(t, repoDir, "rev-parse", "HEAD")
	var profile strings.Builder
	profile.WriteString("mode: atomic\n")
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package special\nfunc value() int { return 2 }\n"), 0644); err != nil {
			t.Fatal(err)
		}
		profile.WriteString("testpkg/special/" + name + ":2.1,2.30 1 1\n")
	}
	profilePath := filepath.Join(repoDir, "coverage.out")
	if err := os.WriteFile(profilePath, []byte(profile.String()), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := CalculateDiffCoverage(repoDir, base, profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if result.Percentage != 100 || result.Covered != 3 || result.Total != 3 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestCalculateDiffCoverageSafelyRejectsNewlinePath(t *testing.T) {
	repoDir := initTestGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module testpkg\n\ngo 1.26\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, repoDir, "add", "go.mod")
	runGitInDir(t, repoDir, "commit", "-m", "base")
	base := runGitInDir(t, repoDir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repoDir, "line\nname.go"), []byte("package testpkg\n"), 0644); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(repoDir, "coverage.out")
	if err := os.WriteFile(profile, []byte("mode: atomic\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := CalculateDiffCoverage(repoDir, base, profile)
	if err == nil || !strings.Contains(err.Error(), "newline") {
		t.Fatalf("newline path must safely fail, got %v", err)
	}
}
