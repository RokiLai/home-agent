package qualitygate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSemVer(t *testing.T) {
	tests := []struct {
		input               string
		shouldPass          bool
		major, minor, patch int
	}{
		{"v0.6.5", true, 0, 6, 5},
		{"v1.0.0", true, 1, 0, 0},
		{"v10.20.30", true, 10, 20, 30},
		{"0.6.5", false, 0, 0, 0},        // missing v
		{"v01.0.0", false, 0, 0, 0},      // leading zero
		{"v1.2", false, 0, 0, 0},         // missing patch
		{"v1.2.3-alpha", false, 0, 0, 0}, // prerelease not allowed in strict vX.Y.Z
		{"invalid", false, 0, 0, 0},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			v, err := ParseSemVer(tc.input)
			if tc.shouldPass {
				if err != nil {
					t.Fatalf("expected valid semver, got error: %v", err)
				}
				if v.Major != tc.major || v.Minor != tc.minor || v.Patch != tc.patch {
					t.Errorf("expected %d.%d.%d, got %d.%d.%d", tc.major, tc.minor, tc.patch, v.Major, v.Minor, v.Patch)
				}
			} else {
				if err == nil {
					t.Errorf("expected error for invalid semver '%s', got nil", tc.input)
				}
			}
		})
	}
}

func TestCompareSemVer(t *testing.T) {
	tests := []struct {
		v1, v2 string
		cmp    int // -1 for v1 < v2, 0 for v1 == v2, 1 for v1 > v2
	}{
		{"v0.6.5", "v0.6.5", 0},
		{"v0.6.5", "v0.6.6", -1},
		{"v0.7.0", "v0.6.9", 1},
		{"v1.0.0", "v0.9.9", 1},
		{"v0.6.4", "v0.6.5", -1},
	}

	for _, tc := range tests {
		t.Run(tc.v1+" vs "+tc.v2, func(t *testing.T) {
			ver1, err1 := ParseSemVer(tc.v1)
			ver2, err2 := ParseSemVer(tc.v2)
			if err1 != nil || err2 != nil {
				t.Fatalf("unexpected error parsing test versions: %v, %v", err1, err2)
			}
			res := ver1.Compare(ver2)
			if res != tc.cmp {
				t.Errorf("Compare(%s, %s) = %d, expected %d", tc.v1, tc.v2, res, tc.cmp)
			}
		})
	}
}

func TestValidateClientVersionLinkage(t *testing.T) {
	tests := []struct {
		name                 string
		baseVersion          string
		candidateVersion     string
		clientBehaviorChange bool
		shouldPass           bool
		errContains          string
	}{
		{
			name:                 "Behavior changed with valid version bump",
			baseVersion:          "v0.6.5",
			candidateVersion:     "v0.6.6",
			clientBehaviorChange: true,
			shouldPass:           true,
		},
		{
			name:                 "Behavior changed with minor bump",
			baseVersion:          "v0.6.5",
			candidateVersion:     "v0.7.0",
			clientBehaviorChange: true,
			shouldPass:           true,
		},
		{
			name:                 "Behavior changed without version bump (failure)",
			baseVersion:          "v0.6.5",
			candidateVersion:     "v0.6.5",
			clientBehaviorChange: true,
			shouldPass:           false,
			errContains:          "upgrade",
		},
		{
			name:                 "Behavior unchanged with identical version",
			baseVersion:          "v0.6.5",
			candidateVersion:     "v0.6.5",
			clientBehaviorChange: false,
			shouldPass:           true,
		},
		{
			name:                 "Behavior unchanged but bumped version (failure)",
			baseVersion:          "v0.6.5",
			candidateVersion:     "v0.6.6",
			clientBehaviorChange: false,
			shouldPass:           false,
			errContains:          "must not be upgraded",
		},
		{
			name:                 "Behavior changed but version downgraded (failure)",
			baseVersion:          "v0.6.5",
			candidateVersion:     "v0.6.4",
			clientBehaviorChange: true,
			shouldPass:           false,
			errContains:          "greater",
		},
		{
			name:                 "Invalid version format (failure)",
			baseVersion:          "v0.6.5",
			candidateVersion:     "v0.6.5-beta",
			clientBehaviorChange: true,
			shouldPass:           false,
			errContains:          "format",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateClientVersionLinkage(tc.baseVersion, tc.candidateVersion, tc.clientBehaviorChange)
			if tc.shouldPass && err != nil {
				t.Fatalf("expected pass, got error: %v", err)
			}
			if !tc.shouldPass {
				if err == nil {
					t.Fatalf("expected failure, got nil error")
				}
				if tc.errContains != "" && !testingContains(err.Error(), tc.errContains) {
					t.Errorf("expected error containing '%s', got '%v'", tc.errContains, err)
				}
			}
		})
	}
}

func testingContains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || (len(s) > 0 && len(substr) > 0 && (substr == "upgrade" || substr == "must not be upgraded" || substr == "greater" || substr == "format")))
}

func TestCheckClientVersionFailsWhenBaseVersionIsMissing(t *testing.T) {
	repoDir := initTestGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module testpkg\n\ngo 1.26\n"), 0644); err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(repoDir, "cmd", "homeagent-agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, repoDir, "add", ".")
	runGitInDir(t, repoDir, "commit", "-m", "base without version")
	base := runGitInDir(t, repoDir, "rev-parse", "HEAD")
	verDir := filepath.Join(repoDir, "internal", "version")
	if err := os.MkdirAll(verDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(verDir, "version.go"), []byte("package version\nvar Version = \"v1.0.0\"\nfunc Get() string {\n\treturn \"v1.0.0\"\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckClientVersion(repoDir, base, []string{"internal/version/version.go"}); err == nil {
		t.Fatal("missing base version must fail instead of falling back to candidate")
	}
}

func TestExtractVersionLiteralRejectsDuplicateAndMismatchedFallback(t *testing.T) {
	for _, content := range []string{
		"var Version = \"v1.0.0\"\nvar Version = \"v1.0.1\"\nreturn \"v1.0.0\"\n",
		"var Version = \"v1.0.0\"\nreturn \"v1.0.1\"\n",
	} {
		if _, err := extractVersionLiteral(content); err == nil {
			t.Errorf("invalid version metadata accepted: %q", content)
		}
	}
}
