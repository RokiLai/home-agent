package qualitygate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v, output: %s", args, err, string(out))
		}
	}

	runGit("init", "-b", "main")
	runGit("config", "user.name", "Test")
	runGit("config", "user.email", "test@example.com")

	// Base file
	baseFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(baseFile, []byte("# Test Repo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "initial commit")

	return dir
}

func TestCheckScope_Valid(t *testing.T) {
	repoDir := initTestGitRepo(t)

	// Create change spec
	specContent := `task: my-feature
description: My feature
allowed_paths:
  - changes/my-feature.yaml
  - pkg/feature/**
`
	specDir := filepath.Join(repoDir, "changes")
	_ = os.MkdirAll(specDir, 0755)
	specPath := filepath.Join(specDir, "my-feature.yaml")
	_ = os.WriteFile(specPath, []byte(specContent), 0644)

	pkgDir := filepath.Join(repoDir, "pkg/feature")
	_ = os.MkdirAll(pkgDir, 0755)
	_ = os.WriteFile(filepath.Join(pkgDir, "code.go"), []byte("package feature\n"), 0644)

	spec, err := LoadChangeSpecFromFile(specPath)
	if err != nil {
		t.Fatalf("LoadChangeSpecFromFile failed: %v", err)
	}

	res, err := CheckScope(repoDir, "HEAD", spec, specPath)
	if err != nil {
		t.Fatalf("CheckScope unexpected error: %v", err)
	}
	if !res.Passed {
		t.Fatalf("expected scope check passed, violations: %v", res.Violations)
	}
	if len(res.Violations) != 0 {
		t.Errorf("expected 0 violations, got %v", res.Violations)
	}
}

func TestCheckScope_UntrackedViolation(t *testing.T) {
	repoDir := initTestGitRepo(t)

	specContent := `task: my-feature
description: My feature
allowed_paths:
  - pkg/feature/**
`
	specPath := filepath.Join(repoDir, "spec.yaml")
	_ = os.WriteFile(specPath, []byte(specContent), 0644)
	spec, _ := ParseChangeSpec(strings.NewReader(specContent))

	// Untracked file outside allowed_paths
	_ = os.WriteFile(filepath.Join(repoDir, "untracked.txt"), []byte("bad\n"), 0644)

	res, err := CheckScope(repoDir, "HEAD", spec, specPath)
	if err != nil {
		t.Fatalf("CheckScope error: %v", err)
	}
	if res.Passed {
		t.Errorf("expected CheckScope to fail on untracked file outside allowed_paths")
	}
	found := false
	for _, v := range res.Violations {
		if strings.Contains(v, "untracked.txt") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected violation mentioning untracked.txt, got %v", res.Violations)
	}
}

func runGitInDir(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed in %s: %v, output: %s", args, dir, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func TestCheckScope_DeletedFileOutOfScope(t *testing.T) {
	repoDir := initTestGitRepo(t)

	// Create and commit a file in base
	_ = os.MkdirAll(filepath.Join(repoDir, "pkg"), 0755)
	_ = os.WriteFile(filepath.Join(repoDir, "pkg/old.go"), []byte("package old\n"), 0644)
	runGitInDir(t, repoDir, "add", "pkg/old.go")
	runGitInDir(t, repoDir, "commit", "-m", "add old.go")

	baseCommit := runGitInDir(t, repoDir, "rev-parse", "HEAD")

	// Delete pkg/old.go in candidate
	_ = os.Remove(filepath.Join(repoDir, "pkg/old.go"))

	specContent := `task: my-feature
description: My feature
allowed_paths:
  - pkg/other/**
`
	spec, _ := ParseChangeSpec(strings.NewReader(specContent))

	res, err := CheckScope(repoDir, baseCommit, spec, "")
	if err != nil {
		t.Fatalf("CheckScope error: %v", err)
	}
	if res.Passed {
		t.Errorf("expected CheckScope to fail when deleted file is outside allowed_paths")
	}
}

func TestCheckScope_BOMDetection(t *testing.T) {
	repoDir := initTestGitRepo(t)

	specContent := `task: my-feature
description: My feature
allowed_paths:
  - pkg/bom.txt
`
	spec, _ := ParseChangeSpec(strings.NewReader(specContent))
	_ = os.MkdirAll(filepath.Join(repoDir, "pkg"), 0755)
	_ = os.WriteFile(filepath.Join(repoDir, "pkg/bom.txt"), []byte("\xef\xbb\xbfhello"), 0644)

	res, err := CheckScope(repoDir, "HEAD", spec, "")
	if err != nil {
		t.Fatalf("CheckScope error: %v", err)
	}
	if res.Passed {
		t.Errorf("expected CheckScope to fail on BOM")
	}
	found := false
	for _, v := range res.Violations {
		if strings.Contains(v, "BOM") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected violation mentioning BOM, got %v", res.Violations)
	}
}

func TestCheckScope_RenameViolation(t *testing.T) {
	repoDir := initTestGitRepo(t)

	// Commit source file
	_ = os.MkdirAll(filepath.Join(repoDir, "src"), 0755)
	_ = os.WriteFile(filepath.Join(repoDir, "src/foo.go"), []byte("package src\n"), 0644)
	runGitInDir(t, repoDir, "add", "src/foo.go")
	runGitInDir(t, repoDir, "commit", "-m", "add foo.go")
	baseCommit := runGitInDir(t, repoDir, "rev-parse", "HEAD")

	// Move to dst/foo.go
	_ = os.MkdirAll(filepath.Join(repoDir, "dst"), 0755)
	runGitInDir(t, repoDir, "mv", "src/foo.go", "dst/foo.go")

	// Spec only allows dst/** (src/** is outside allowed_paths)
	specContent := `task: rename-feature
description: Rename feature
allowed_paths:
  - dst/**
`
	spec, _ := ParseChangeSpec(strings.NewReader(specContent))

	res, err := CheckScope(repoDir, baseCommit, spec, "")
	if err != nil {
		t.Fatalf("CheckScope error: %v", err)
	}
	if res.Passed {
		t.Errorf("expected rename source out of scope to fail")
	}
}

func TestCheckScope_WhitespaceError(t *testing.T) {
	repoDir := initTestGitRepo(t)

	specContent := `task: ws-feature
description: WS feature
allowed_paths:
  - pkg/ws.go
`
	spec, _ := ParseChangeSpec(strings.NewReader(specContent))
	_ = os.MkdirAll(filepath.Join(repoDir, "pkg"), 0755)
	_ = os.WriteFile(filepath.Join(repoDir, "pkg/ws.go"), []byte("package pkg   \n"), 0644)

	res, err := CheckScope(repoDir, "HEAD", spec, "")
	if err != nil {
		t.Fatalf("CheckScope error: %v", err)
	}
	if res.Passed {
		t.Errorf("expected whitespace error on trailing space")
	}
}

func TestCheckScopeChecksIgnoredExplicitSpecFormattingAndStat(t *testing.T) {
	repoDir := initTestGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, ".gitignore"), []byte("/changes/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, repoDir, "add", ".gitignore")
	runGitInDir(t, repoDir, "commit", "-m", "base")
	dir := filepath.Join(repoDir, "changes")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ignored-spec.yaml")
	if err := os.WriteFile(path, []byte("task: ignored-spec   \n"), 0644); err != nil {
		t.Fatal(err)
	}
	spec := &ChangeSpec{Task: "ignored-spec", AllowedPaths: []string{"changes/ignored-spec.yaml"}}
	res, err := CheckScope(repoDir, "HEAD", spec, path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed {
		t.Fatal("ignored explicit spec trailing whitespace must fail")
	}
	if !strings.Contains(res.DiffStatUntracked, "ignored-spec.yaml") {
		t.Fatalf("ignored explicit spec missing from stat: %s", res.DiffStatUntracked)
	}
}

func TestCheckScopeRejectsRenameDestinationOutsideAllowedPaths(t *testing.T) {
	repoDir := initTestGitRepo(t)
	if err := os.MkdirAll(filepath.Join(repoDir, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "src", "file.go"), []byte("package src\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, repoDir, "add", ".")
	runGitInDir(t, repoDir, "commit", "-m", "base")
	base := runGitInDir(t, repoDir, "rev-parse", "HEAD")
	if err := os.MkdirAll(filepath.Join(repoDir, "dst"), 0755); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, repoDir, "mv", "src/file.go", "dst/file.go")
	res, err := CheckScope(repoDir, base, &ChangeSpec{AllowedPaths: []string{"src/**"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed {
		t.Fatal("rename destination outside allowed_paths must fail")
	}
}

func TestCheckScopePreservesSpecialPathBytes(t *testing.T) {
	repoDir := initTestGitRepo(t)
	base := runGitInDir(t, repoDir, "rev-parse", "HEAD")
	dir := filepath.Join(repoDir, "special")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	names := []string{"space name.go", "tab\tname.go", "line\nname.go", "非ASCII.go"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package special\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := CheckScope(repoDir, base, &ChangeSpec{AllowedPaths: []string{"special/**"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed {
		t.Fatalf("special paths rejected: %v", res.Violations)
	}
	for _, name := range names {
		want := filepath.ToSlash(filepath.Join("special", name))
		found := false
		for _, got := range res.AllChangedFiles {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("path was split or omitted: %q", want)
		}
	}
}

func TestCheckScopeRejectsInvalidUTF8(t *testing.T) {
	repoDir := initTestGitRepo(t)
	base := runGitInDir(t, repoDir, "rev-parse", "HEAD")
	path := filepath.Join(repoDir, "invalid.txt")
	if err := os.WriteFile(path, []byte{0xff, 0xfe}, 0644); err != nil {
		t.Fatal(err)
	}
	res, err := CheckScope(repoDir, base, &ChangeSpec{AllowedPaths: []string{"invalid.txt"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed {
		t.Fatal("invalid UTF-8 must fail")
	}
}
