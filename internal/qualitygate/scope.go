package qualitygate

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// DiffEntry 记录单条 git diff 变更记录。
type DiffEntry struct {
	Status  string
	Path    string
	OldPath string
}

// ScopeResult 记录变更范围检查的汇总与证据。
type ScopeResult struct {
	BaseRef           string      `json:"base_ref"`
	MergeBase         string      `json:"merge_base"`
	TrackedChanges    []DiffEntry `json:"tracked_changes"`
	UntrackedFiles    []string    `json:"untracked_files"`
	ExplicitSpecPath  string      `json:"explicit_spec_path,omitempty"`
	AllChangedFiles   []string    `json:"all_changed_files"`
	Violations        []string    `json:"violations"`
	DiffStatTracked   string      `json:"diff_stat_tracked"`
	DiffStatUntracked string      `json:"diff_stat_untracked"`
	DiffStatCombined  string      `json:"diff_stat_combined"`
	Passed            bool        `json:"passed"`
}

// ResolveMergeBase 计算基准提交与候选 HEAD 的 merge-base。
func ResolveMergeBase(rootDir, baseRef string) (string, error) {
	cmd := exec.Command("git", "merge-base", baseRef, "HEAD")
	cmd.Dir = rootDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git merge-base %s HEAD failed: %v, output: %s", baseRef, err, string(out))
	}
	return strings.TrimSpace(string(out)), nil
}

// CheckScope 执行变更清单与范围检查。
func CheckScope(rootDir, baseRef string, spec *ChangeSpec, explicitSpecPath string) (*ScopeResult, error) {
	mergeBase, err := ResolveMergeBase(rootDir, baseRef)
	if err != nil {
		return nil, err
	}

	res := &ScopeResult{
		BaseRef:          baseRef,
		MergeBase:        mergeBase,
		ExplicitSpecPath: explicitSpecPath,
		Passed:           true,
	}

	// 1. 获取已跟踪变更 git diff --name-status -z --find-renames <mergeBase>
	diffCmd := exec.Command("git", "diff", "--name-status", "-z", "--find-renames", mergeBase)
	diffCmd.Dir = rootDir
	diffOut, err := diffCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff failed: %w", err)
	}

	trackedChanges, err := parseNameStatusZ(diffOut)
	if err != nil {
		return nil, fmt.Errorf("parse git diff output failed: %w", err)
	}
	res.TrackedChanges = trackedChanges

	// 2. 获取未跟踪文件 git ls-files --others --exclude-standard -z
	lsCmd := exec.Command("git", "ls-files", "--others", "--exclude-standard", "-z")
	lsCmd.Dir = rootDir
	lsOut, err := lsCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files failed: %w", err)
	}
	untracked := parseNulStrings(lsOut)
	res.UntrackedFiles = untracked

	allChangedMap := make(map[string]struct{})
	trackedPathMap := make(map[string]struct{})

	// 3. 校验已跟踪变更
	for _, entry := range trackedChanges {
		allChangedMap[entry.Path] = struct{}{}
		trackedPathMap[entry.Path] = struct{}{}
		if entry.OldPath != "" {
			allChangedMap[entry.OldPath] = struct{}{}
			trackedPathMap[entry.OldPath] = struct{}{}
		}

		// 检查路径越界
		if !MatchAllowedPath(entry.Path, spec.AllowedPaths) {
			res.Violations = append(res.Violations, fmt.Sprintf("tracked file '%s' (status %s) is not within allowed_paths", entry.Path, entry.Status))
		}
		if entry.OldPath != "" && !MatchAllowedPath(entry.OldPath, spec.AllowedPaths) {
			res.Violations = append(res.Violations, fmt.Sprintf("renamed/copied source path '%s' is not within allowed_paths", entry.OldPath))
		}
	}

	// 4. 校验未跟踪文件
	for _, u := range untracked {
		cleanU := filepath.ToSlash(filepath.Clean(u))
		allChangedMap[cleanU] = struct{}{}
		if !MatchAllowedPath(cleanU, spec.AllowedPaths) {
			res.Violations = append(res.Violations, fmt.Sprintf("untracked file '%s' is not within allowed_paths", cleanU))
		}
	}

	// 5. 显式指定的 specPath（如果未包含在 git status 中，如被 .gitignore 忽略）
	var explicitExtra string
	if explicitSpecPath != "" {
		relSpec, err := filepath.Rel(rootDir, explicitSpecPath)
		if err == nil && !strings.HasPrefix(relSpec, "..") {
			cleanSpec := filepath.ToSlash(filepath.Clean(relSpec))
			allChangedMap[cleanSpec] = struct{}{}
			if _, tracked := trackedPathMap[cleanSpec]; !tracked {
				foundUntracked := false
				for _, path := range untracked {
					if filepath.ToSlash(filepath.Clean(path)) == cleanSpec {
						foundUntracked = true
						break
					}
				}
				if !foundUntracked {
					explicitExtra = cleanSpec
				}
			}
			if !MatchAllowedPath(cleanSpec, spec.AllowedPaths) {
				res.Violations = append(res.Violations, fmt.Sprintf("explicit change spec '%s' is not within allowed_paths", cleanSpec))
			}
		}
	}

	// 排序汇总全部修改文件列表
	var allFiles []string
	for f := range allChangedMap {
		allFiles = append(allFiles, f)
	}
	sort.Strings(allFiles)
	res.AllChangedFiles = allFiles

	// 6. 校验 UTF-8 无 BOM (删除的文件跳过)
	for _, f := range allFiles {
		absPath := filepath.Join(rootDir, f)
		info, err := os.Lstat(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue // 文件已被删除
			}
			res.Violations = append(res.Violations, fmt.Sprintf("file '%s' stat error: %v", f, err))
			continue
		}
		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}

		data, err := os.ReadFile(absPath)
		if err != nil {
			res.Violations = append(res.Violations, fmt.Sprintf("read file '%s' error: %v", f, err))
			continue
		}

		if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
			res.Violations = append(res.Violations, fmt.Sprintf("file '%s' contains UTF-8 BOM, which is prohibited", f))
		} else if !utf8.Valid(data) {
			res.Violations = append(res.Violations, fmt.Sprintf("file '%s' is not valid UTF-8", f))
		}
	}

	// 7. 运行 git diff --check <mergeBase>
	checkCmd := exec.Command("git", "diff", "--check", mergeBase)
	checkCmd.Dir = rootDir
	checkOut, checkErr := checkCmd.CombinedOutput()
	if checkErr != nil || len(bytes.TrimSpace(checkOut)) > 0 {
		res.Violations = append(res.Violations, fmt.Sprintf("git diff --check %s failed:\n%s", mergeBase, string(checkOut)))
	}

	// 运行 untracked 文件的 git diff --no-index --check /dev/null <file>
	extraFiles := append([]string(nil), untracked...)
	if explicitExtra != "" {
		extraFiles = append(extraFiles, explicitExtra)
	}
	for _, u := range extraFiles {
		absU := filepath.Join(rootDir, u)
		noIndexCheck := exec.Command("git", "diff", "--no-index", "--check", "/dev/null", absU)
		noIndexCheck.Dir = rootDir
		out, _ := noIndexCheck.CombinedOutput()
		if len(bytes.TrimSpace(out)) > 0 {
			res.Violations = append(res.Violations, fmt.Sprintf("git diff --no-index --check /dev/null %s failed:\n%s", u, string(out)))
		}
	}

	// 8. 统计 git diff --stat
	statCmd := exec.Command("git", "diff", "--stat", mergeBase)
	statCmd.Dir = rootDir
	statOut, _ := statCmd.CombinedOutput()
	res.DiffStatTracked = string(statOut)

	var untrackedStat strings.Builder
	for _, u := range extraFiles {
		absU := filepath.Join(rootDir, u)
		noIndexStat := exec.Command("git", "diff", "--no-index", "--stat", "/dev/null", absU)
		noIndexStat.Dir = rootDir
		out, _ := noIndexStat.CombinedOutput()
		untrackedStat.Write(out)
	}
	res.DiffStatUntracked = untrackedStat.String()

	res.DiffStatCombined = fmt.Sprintf("=== Tracked Diff Stat ===\n%s\n=== Untracked Diff Stat ===\n%s\n=== Total Changed Files: %d ===\n",
		res.DiffStatTracked, res.DiffStatUntracked, len(res.AllChangedFiles))

	if len(res.Violations) > 0 {
		res.Passed = false
	}

	return res, nil
}

func parseNameStatusZ(data []byte) ([]DiffEntry, error) {
	var entries []DiffEntry
	parts := bytes.Split(data, []byte{0})
	idx := 0
	for idx < len(parts) {
		token := string(parts[idx])
		if token == "" {
			idx++
			continue
		}
		status := token
		idx++
		if idx >= len(parts) {
			break
		}

		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			oldPath := string(parts[idx])
			idx++
			if idx >= len(parts) {
				return nil, fmt.Errorf("malformed rename/copy diff record")
			}
			newPath := string(parts[idx])
			idx++
			entries = append(entries, DiffEntry{
				Status:  status,
				Path:    newPath,
				OldPath: oldPath,
			})
		} else {
			path := string(parts[idx])
			idx++
			entries = append(entries, DiffEntry{
				Status: status,
				Path:   path,
			})
		}
	}
	return entries, nil
}

func parseNulStrings(data []byte) []string {
	var list []string
	parts := bytes.Split(data, []byte{0})
	for _, p := range parts {
		s := string(p)
		if s != "" {
			list = append(list, s)
		}
	}
	return list
}
