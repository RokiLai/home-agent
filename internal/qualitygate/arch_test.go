package qualitygate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckArchitectureImports(t *testing.T) {
	tests := []struct {
		name        string
		sourcePkg   string
		imports     []string
		shouldPass  bool
		errContains string
	}{
		{
			name:       "Legal cross-module import (public to public)",
			sourcePkg:  "homeagent/internal/api",
			imports:    []string{"homeagent/internal/auth", "homeagent/internal/device"},
			shouldPass: true,
		},
		{
			name:       "Legal self internal import",
			sourcePkg:  "homeagent/internal/auth",
			imports:    []string{"homeagent/internal/auth/internal/store"},
			shouldPass: true,
		},
		{
			name:        "Illegal cross-module private internal import",
			sourcePkg:   "homeagent/internal/api",
			imports:     []string{"homeagent/internal/auth/internal/store"},
			shouldPass:  false,
			errContains: "private",
		},
		{
			name:        "Illegal cross-module private infrastructure import",
			sourcePkg:   "homeagent/internal/sshsync",
			imports:     []string{"homeagent/internal/device/infrastructure/db"},
			shouldPass:  false,
			errContains: "private",
		},
		{
			name:        "Illegal relative import",
			sourcePkg:   "homeagent/internal/api",
			imports:     []string{"./subpkg"},
			shouldPass:  false,
			errContains: "relative",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			violations := checkPackageImports(tc.sourcePkg, tc.imports)
			if tc.shouldPass && len(violations) > 0 {
				t.Fatalf("expected pass, got violations: %v", violations)
			}
			if !tc.shouldPass {
				if len(violations) == 0 {
					t.Fatalf("expected failure, got 0 violations")
				}
				joined := strings.Join(violations, "; ")
				if !strings.Contains(strings.ToLower(joined), strings.ToLower(tc.errContains)) {
					t.Errorf("expected violation containing '%s', got '%s'", tc.errContains, joined)
				}
			}
		})
	}
}

func TestCheckGoModDependencies(t *testing.T) {
	baseGoMod := `module homeagent

go 1.26

require (
	golang.org/x/sys v0.47.0
	golang.org/x/crypto v0.55.0
)
`
	candidateGoMod := `module homeagent

go 1.26

require (
	github.com/example/newlib v1.0.0
	golang.org/x/sys v0.47.0
	golang.org/x/crypto v0.55.0
)
`
	specWithDecl := &ChangeSpec{
		Task:         "test",
		Description:  "test",
		AllowedPaths: []string{"go.mod"},
		ExternalDependencies: []ExternalDependency{
			{
				Module:    "github.com/example/newlib",
				Rationale: "Needed for new feature",
			},
		},
	}

	violations1 := checkGoModChanges(baseGoMod, candidateGoMod, specWithDecl)
	if len(violations1) != 0 {
		t.Errorf("expected pass with declared external dependency, got %v", violations1)
	}

	specWithoutDecl := &ChangeSpec{
		Task:         "test",
		Description:  "test",
		AllowedPaths: []string{"go.mod"},
	}
	violations2 := checkGoModChanges(baseGoMod, candidateGoMod, specWithoutDecl)
	if len(violations2) == 0 {
		t.Errorf("expected failure when new dependency is undeclared")
	}
}

func TestCheckGoSumDependencies(t *testing.T) {
	spec := &ChangeSpec{ExternalDependencies: []ExternalDependency{{Module: "example.com/declared", Rationale: "needed"}}}
	base := "example.com/existing v1.0.0 h1:base\n"
	candidate := base + "example.com/declared v1.0.0 h1:ok\nexample.com/undeclared v1.0.0 h1:bad\n"
	violations := checkGoSumChanges(base, candidate, spec)
	if len(violations) != 1 || !strings.Contains(violations[0], "undeclared") {
		t.Fatalf("unexpected go.sum violations: %v", violations)
	}
}

func TestCheckArchitectureRejectsSymlinkOutsideRepository(t *testing.T) {
	repoDir := initTestGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module homeagent\n\ngo 1.26\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, repoDir, "add", "go.mod")
	runGitInDir(t, repoDir, "commit", "-m", "base")
	base := runGitInDir(t, repoDir, "rev-parse", "HEAD")
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package outside\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repoDir, "outside.go")); err != nil {
		t.Fatal(err)
	}
	res, err := CheckArchitecture(repoDir, base, &ChangeSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed {
		t.Fatal("repository-external symlink must fail architecture check")
	}
}
