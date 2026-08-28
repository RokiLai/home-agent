package qualitygate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 步骤与流水线状态枚举
const (
	StatusPassed        = "passed"
	StatusFailed        = "failed"
	StatusNotApplicable = "not_applicable"
	StatusBlocked       = "blocked"
	StatusNotRun        = "not_run"
)

// StepResult 记录单个步骤的执行状态。
type StepResult struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	ExitCode   *int   `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Reason     string `json:"reason"`
	Log        string `json:"log"`
}

// PipelineResult 记录流水线完整执行结果。
type PipelineResult struct {
	SchemaVersion    int          `json:"schema_version"`
	Task             string       `json:"task"`
	BaseCommit       string       `json:"base_commit"`
	CandidateCommit  string       `json:"candidate_commit"`
	MergeBase        string       `json:"merge_base"`
	WorkingTreeDirty bool         `json:"working_tree_dirty"`
	ChangeDigest     string       `json:"change_digest"`
	Status           string       `json:"status"`
	StartedAt        string       `json:"started_at"`
	FinishedAt       string       `json:"finished_at"`
	Steps            []StepResult `json:"steps"`
	EvidenceError    error        `json:"-"`
}

// CalculateChangeDigest 计算确定性变更摘要。
func CalculateChangeDigest(rootDir, mergeBase string, untrackedFiles []string, explicitSpecPath string) (string, error) {
	h := sha256.New()

	// 1. 写入版本头
	h.Write([]byte("homeagent-change-digest-v1\x00"))

	// 2. 写入 git diff --binary --full-index --no-ext-diff --no-textconv <mergeBase>
	cmd := exec.Command("git", "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", mergeBase)
	cmd.Dir = rootDir
	diffOut, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git binary diff failed: %w", err)
	}
	h.Write(diffOut)

	// 3. 收集未跟踪文件及未提交/被忽略的显式 spec
	untrackedMap := make(map[string]bool)
	for _, u := range untrackedFiles {
		cleanU := filepath.ToSlash(filepath.Clean(u))
		trackedCmd := exec.Command("git", "ls-files", "--error-unmatch", "--", cleanU)
		trackedCmd.Dir = rootDir
		if trackedCmd.Run() == nil {
			continue
		}
		baseTrackedCmd := exec.Command("git", "cat-file", "-e", mergeBase+":"+cleanU)
		baseTrackedCmd.Dir = rootDir
		if baseTrackedCmd.Run() == nil {
			continue
		}
		untrackedMap[cleanU] = true
	}
	if explicitSpecPath != "" {
		relSpec, err := filepath.Rel(rootDir, explicitSpecPath)
		if err == nil && !strings.HasPrefix(relSpec, "..") {
			cleanSpec := filepath.ToSlash(filepath.Clean(relSpec))
			untrackedMap[cleanSpec] = true
		}
	}

	var extraFiles []string
	for f := range untrackedMap {
		extraFiles = append(extraFiles, f)
	}
	sort.Strings(extraFiles)

	for _, relPath := range extraFiles {
		if !utf8.ValidString(relPath) {
			return "", fmt.Errorf("changed path is not valid UTF-8")
		}
		absPath := filepath.Join(rootDir, relPath)
		info, err := os.Lstat(absPath)
		if err != nil {
			return "", fmt.Errorf("stat digest input %s failed: %w", relPath, err)
		}

		var content []byte
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(absPath)
			if err != nil {
				return "", err
			}
			content = []byte(target)
		} else if info.IsDir() {
			continue
		} else {
			data, err := os.ReadFile(absPath)
			if err != nil {
				return "", err
			}
			content = data
		}
		afterInfo, err := os.Lstat(absPath)
		if err != nil || info.Mode() != afterInfo.Mode() || info.Size() != afterInfo.Size() || !info.ModTime().Equal(afterInfo.ModTime()) {
			return "", fmt.Errorf("digest input %s changed while being read", relPath)
		}

		pathBytes := []byte(relPath)
		h.Write([]byte(strconv.Itoa(len(pathBytes))))
		h.Write([]byte{0})
		h.Write(pathBytes)
		h.Write([]byte{0})
		h.Write([]byte(strconv.Itoa(len(content))))
		h.Write([]byte{0})
		h.Write(content)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

var (
	bearerTokenRegex = regexp.MustCompile(`(?i)(bearer\s+|token[:=]\s*)[a-zA-Z0-9_\-\.]{16,}`)
	cookieRegex      = regexp.MustCompile(`(?i)(cookie[:=]\s*)[^\r\n]+`)
	privateKeyRegex  = regexp.MustCompile(`(?s)-----BEGIN[ A-Z_-]*PRIVATE KEY-----.*?-----END[ A-Z_-]*PRIVATE KEY-----`)
)

// SanitizeOutput 对敏感信息进行掩码处理。
func SanitizeOutput(s string) string {
	s = privateKeyRegex.ReplaceAllString(s, "[REDACTED PRIVATE KEY]")
	s = bearerTokenRegex.ReplaceAllString(s, "$1[REDACTED TOKEN]")
	s = cookieRegex.ReplaceAllString(s, "$1[REDACTED COOKIE]")
	return s
}

// WriteResultFiles 将流水线执行产物写入指定结果目录。
func WriteResultFiles(resultDir string, res *PipelineResult, changedFiles []string, diffStat string, coverageSummary string) error {
	if err := os.MkdirAll(resultDir, 0755); err != nil {
		return fmt.Errorf("create result dir failed: %w", err)
	}
	logsDir := filepath.Join(resultDir, "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return fmt.Errorf("create logs dir failed: %w", err)
	}

	// 1. result.json
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(resultDir, "result.json"), data, 0644); err != nil {
		return err
	}

	// 2. changed-files.txt
	changedFilesContent := strings.Join(changedFiles, "\n")
	if len(changedFiles) > 0 {
		changedFilesContent += "\n"
	}
	if err := os.WriteFile(filepath.Join(resultDir, "changed-files.txt"), []byte(SanitizeOutput(changedFilesContent)), 0644); err != nil {
		return err
	}

	// 3. diff-stat.txt
	if err := os.WriteFile(filepath.Join(resultDir, "diff-stat.txt"), []byte(SanitizeOutput(diffStat)), 0644); err != nil {
		return err
	}

	// 4. coverage-summary.txt
	if coverageSummary != "" {
		if err := os.WriteFile(filepath.Join(resultDir, "coverage-summary.txt"), []byte(SanitizeOutput(coverageSummary)), 0644); err != nil {
			return err
		}
	}

	return nil
}

// HasWorkingTreeDirty 检查候选工作树是否包含脏变更。
func HasWorkingTreeDirty(rootDir string, mergeBase string, explicitSpecPath string) bool {
	cmd := exec.Command("git", "status", "--porcelain", "-z")
	cmd.Dir = rootDir
	out, err := cmd.Output()
	if err != nil {
		return true
	}
	if len(bytes.TrimSpace(out)) > 0 {
		return true
	}
	if explicitSpecPath != "" {
		rel, relErr := filepath.Rel(rootDir, explicitSpecPath)
		if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			tracked := exec.Command("git", "ls-files", "--error-unmatch", "--", filepath.ToSlash(rel))
			tracked.Dir = rootDir
			return tracked.Run() != nil
		}
	}
	return false
}
