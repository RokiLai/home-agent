package qualitygate

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResultJSON_MarshallingAndValidation(t *testing.T) {
	exitCodeZero := 0
	res := &PipelineResult{
		SchemaVersion:    1,
		Task:             "test-task",
		BaseCommit:       "0123456789abcdef0123456789abcdef01234567",
		CandidateCommit:  "abcdef0123456789abcdef0123456789abcdef01",
		MergeBase:        "1111111111222222222233333333334444444444",
		WorkingTreeDirty: true,
		ChangeDigest:     "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Status:           StatusPassed,
		StartedAt:        time.Now().UTC().Format(time.RFC3339),
		FinishedAt:       time.Now().UTC().Format(time.RFC3339),
		Steps: []StepResult{
			{
				ID:         "preflight",
				Status:     StatusPassed,
				ExitCode:   &exitCodeZero,
				DurationMS: 12,
				Reason:     "Parameters and spec validated",
				Log:        "logs/preflight.log",
			},
			{
				ID:         "scope",
				Status:     StatusNotRun,
				ExitCode:   nil,
				DurationMS: 0,
				Reason:     "Not reached",
				Log:        "logs/scope.log",
			},
		},
	}

	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var parsed PipelineResult
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if parsed.SchemaVersion != 1 || parsed.Task != "test-task" {
		t.Errorf("unexpected parsed result: %+v", parsed)
	}
	if len(parsed.Steps) != 2 || parsed.Steps[1].ExitCode != nil {
		t.Errorf("unexpected steps: %+v", parsed.Steps)
	}
}

func TestSanitizeOutput(t *testing.T) {
	raw := `
Token: bearer abcdef1234567890abcdef1234567890
Cookie: session_id=secret_cookie_value_12345
-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
-----END OPENSSH PRIVATE KEY-----
`
	sanitized := SanitizeOutput(raw)
	if strings.Contains(sanitized, "secret_cookie_value_12345") {
		t.Errorf("cookie was not sanitized")
	}
	if strings.Contains(sanitized, "b3BlbnNzaC") {
		t.Errorf("private key content was not sanitized")
	}
}

func TestCalculateChangeDigestDoesNotDuplicateTrackedFiles(t *testing.T) {
	repoDir := initTestGitRepo(t)
	path := filepath.Join(repoDir, "tracked.txt")
	if err := os.WriteFile(path, []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, repoDir, "add", "tracked.txt")
	runGitInDir(t, repoDir, "commit", "-m", "base")
	base := runGitInDir(t, repoDir, "rev-parse", "HEAD")
	if err := os.WriteFile(path, []byte("candidate\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := CalculateChangeDigest(repoDir, base, []string{"tracked.txt"}, "")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", base)
	cmd.Dir = repoDir
	diff, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	h.Write([]byte("homeagent-change-digest-v1\x00"))
	h.Write(diff)
	want := fmt.Sprintf("%x", h.Sum(nil))
	if got != want {
		t.Fatalf("digest duplicated tracked content: got %s want %s", got, want)
	}
}

func TestWriteResultFilesSanitizesEveryTextArtifact(t *testing.T) {
	dir := t.TempDir()
	secret := "abcdefghijklmnop1234567890"
	res := &PipelineResult{SchemaVersion: 1, Task: "test", Steps: []StepResult{{ID: "x", Status: StatusFailed, Reason: SanitizeOutput("Token: " + secret), Log: "logs/x.log"}}}
	if err := WriteResultFiles(dir, res, []string{"Token: " + secret}, "Cookie: session="+secret, "Bearer "+secret); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"result.json", "changed-files.txt", "diff-stat.txt", "coverage-summary.txt"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), secret) {
			t.Errorf("secret remained in %s", name)
		}
	}
}
