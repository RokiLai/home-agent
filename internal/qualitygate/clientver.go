package qualitygate

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var semVerRegex = regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$`)

// SemVer 代表语义化版本 vMAJOR.MINOR.PATCH。
type SemVer struct {
	Major int
	Minor int
	Patch int
	Raw   string
}

// ParseSemVer 严格解析 vX.Y.Z 格式。
func ParseSemVer(s string) (SemVer, error) {
	matches := semVerRegex.FindStringSubmatch(strings.TrimSpace(s))
	if len(matches) != 4 {
		return SemVer{}, fmt.Errorf("invalid SemVer format '%s', must be strictly vMAJOR.MINOR.PATCH", s)
	}

	major, _ := strconv.Atoi(matches[1])
	minor, _ := strconv.Atoi(matches[2])
	patch, _ := strconv.Atoi(matches[3])

	return SemVer{
		Major: major,
		Minor: minor,
		Patch: patch,
		Raw:   s,
	}, nil
}

// Compare 比较两个 SemVer：v1 < v2 返回 -1，v1 == v2 返回 0，v1 > v2 返回 1。
func (v SemVer) Compare(other SemVer) int {
	if v.Major != other.Major {
		if v.Major < other.Major {
			return -1
		}
		return 1
	}
	if v.Minor != other.Minor {
		if v.Minor < other.Minor {
			return -1
		}
		return 1
	}
	if v.Patch != other.Patch {
		if v.Patch < other.Patch {
			return -1
		}
		return 1
	}
	return 0
}

// ValidateClientVersionLinkage 校验客户端行为变化与版本升级联动契约。
func ValidateClientVersionLinkage(baseVerStr, candidateVerStr string, behaviorChanged bool) error {
	baseVer, err := ParseSemVer(baseVerStr)
	if err != nil {
		return fmt.Errorf("base client version invalid: %w", err)
	}
	candidateVer, err := ParseSemVer(candidateVerStr)
	if err != nil {
		return fmt.Errorf("candidate client version invalid: %w", err)
	}

	cmp := candidateVer.Compare(baseVer)

	if behaviorChanged {
		if cmp <= 0 {
			return fmt.Errorf("client behavior changed but candidate version (%s) is not strictly greater than base version (%s); client must upgrade version", candidateVerStr, baseVerStr)
		}
	} else {
		if cmp != 0 {
			return fmt.Errorf("client behavior has not changed but client version was modified (base: %s, candidate: %s); version must not be upgraded without behavior changes", baseVerStr, candidateVerStr)
		}
	}

	return nil
}

// ClientVersionResult 记录客户端版本联动检查结果。
type ClientVersionResult struct {
	BaseVersion      string   `json:"base_version"`
	CandidateVersion string   `json:"candidate_version"`
	BehaviorChanged  bool     `json:"behavior_changed"`
	ChangedFiles     []string `json:"changed_files"`
	Violations       []string `json:"violations"`
	Passed           bool     `json:"passed"`
}

type clientDepsPackage struct {
	Dir          string   `json:"Dir"`
	ImportPath   string   `json:"ImportPath"`
	GoFiles      []string `json:"GoFiles"`
	CgoFiles     []string `json:"CgoFiles"`
	CFiles       []string `json:"CFiles"`
	CXXFiles     []string `json:"CXXFiles"`
	MFiles       []string `json:"MFiles"`
	HFiles       []string `json:"HFiles"`
	FFiles       []string `json:"FFiles"`
	SFiles       []string `json:"SFiles"`
	SwigFiles    []string `json:"SwigFiles"`
	SwigCXXFiles []string `json:"SwigCXXFiles"`
	SysoFiles    []string `json:"SysoFiles"`
	EmbedFiles   []string `json:"EmbedFiles"`
}

// CheckClientVersion 执行客户端行为与版本联动检查。
func CheckClientVersion(rootDir, mergeBase string, allChangedFiles []string) (*ClientVersionResult, error) {
	res := &ClientVersionResult{
		Passed: true,
	}

	// 1. Collect candidate and base dependency file sets so deleted packages cannot disappear from detection.
	clientFilesMap, err := collectClientDependencyFiles(rootDir)
	if err != nil {
		return nil, fmt.Errorf("collect candidate client dependencies failed: %w", err)
	}
	baseRoot, cleanup, err := extractGitTree(rootDir, mergeBase)
	if err != nil {
		return nil, fmt.Errorf("restore base client tree failed: %w", err)
	}
	defer cleanup()
	baseFiles, err := collectClientDependencyFiles(baseRoot)
	if err != nil {
		return nil, fmt.Errorf("collect base client dependencies failed: %w", err)
	}
	for file := range baseFiles {
		clientFilesMap[file] = true
	}

	// 2. 判定是否有客户端行为变化
	for _, changedFile := range allChangedFiles {
		clean := filepath.ToSlash(filepath.Clean(changedFile))
		if clientFilesMap[clean] {
			res.BehaviorChanged = true
			res.ChangedFiles = append(res.ChangedFiles, clean)
		}
	}

	// 3. 读取 base 与 candidate 版本
	baseVerBytes, err := getGitFileContent(rootDir, mergeBase, "internal/version/version.go")
	if err != nil {
		return nil, fmt.Errorf("read base internal/version/version.go failed: %w", err)
	}
	candVerBytes, err := os.ReadFile(filepath.Join(rootDir, "internal/version/version.go"))
	if err != nil {
		return nil, fmt.Errorf("read internal/version/version.go failed: %w", err)
	}
	for _, changedFile := range allChangedFiles {
		if filepath.ToSlash(filepath.Clean(changedFile)) == "internal/version/version.go" && versionImplementationChanged(string(baseVerBytes), string(candVerBytes)) {
			res.BehaviorChanged = true
			res.ChangedFiles = append(res.ChangedFiles, "internal/version/version.go")
			break
		}
	}

	baseVer, err := extractVersionLiteral(string(baseVerBytes))
	if err != nil {
		return nil, fmt.Errorf("parse base client version failed: %w", err)
	}
	candVer, err := extractVersionLiteral(string(candVerBytes))
	if err != nil {
		return nil, fmt.Errorf("parse candidate client version failed: %w", err)
	}

	res.BaseVersion = baseVer
	res.CandidateVersion = candVer

	if err := ValidateClientVersionLinkage(baseVer, candVer, res.BehaviorChanged); err != nil {
		res.Violations = append(res.Violations, err.Error())
		res.Passed = false
	}

	return res, nil
}

func collectClientDependencyFiles(rootDir string) (map[string]bool, error) {
	files := map[string]bool{"scripts/install.sh": true, "scripts/install.ps1": true}
	cmd := exec.Command("go", "list", "-deps", "-json", "./cmd/homeagent-agent")
	cmd.Dir = rootDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("go list client dependencies failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var pkg clientDepsPackage
		if err := dec.Decode(&pkg); err != nil {
			return nil, fmt.Errorf("decode go list output failed: %w", err)
		}
		relDir, err := filepath.Rel(rootDir, pkg.Dir)
		if err != nil || relDir == ".." || strings.HasPrefix(relDir, ".."+string(filepath.Separator)) {
			continue
		}
		add := func(names []string) {
			for _, name := range names {
				rel := filepath.ToSlash(filepath.Clean(filepath.Join(relDir, name)))
				if rel != "internal/version/version.go" {
					files[rel] = true
				}
			}
		}
		add(pkg.GoFiles)
		add(pkg.CgoFiles)
		add(pkg.CFiles)
		add(pkg.CXXFiles)
		add(pkg.MFiles)
		add(pkg.HFiles)
		add(pkg.FFiles)
		add(pkg.SFiles)
		add(pkg.SwigFiles)
		add(pkg.SwigCXXFiles)
		add(pkg.SysoFiles)
		add(pkg.EmbedFiles)
	}
	return files, nil
}

func extractGitTree(rootDir, ref string) (string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "homeagent-client-base-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }
	cmd := exec.Command("git", "archive", "--format=tar", ref)
	cmd.Dir = rootDir
	archive, err := cmd.Output()
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			cleanup()
			return "", func() {}, err
		}
		clean := filepath.Clean(filepath.FromSlash(hdr.Name))
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
			cleanup()
			return "", func() {}, fmt.Errorf("unsafe archive path %q", hdr.Name)
		}
		dst := filepath.Join(tmpDir, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(dst, 0755)
		case tar.TypeReg, tar.TypeRegA:
			if err = os.MkdirAll(filepath.Dir(dst), 0755); err == nil {
				var file *os.File
				file, err = os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0777)
				if err == nil {
					_, err = io.Copy(file, tr)
					closeErr := file.Close()
					if err == nil {
						err = closeErr
					}
				}
			}
		case tar.TypeSymlink:
			if err = os.MkdirAll(filepath.Dir(dst), 0755); err == nil {
				err = os.Symlink(hdr.Linkname, dst)
			}
		}
		if err != nil {
			cleanup()
			return "", func() {}, err
		}
	}
	return tmpDir, cleanup, nil
}

var (
	versionLiteralRegex      = regexp.MustCompile(`(?m)^\s*var\s+Version\s*=\s*"([^"]+)"\s*$`)
	fallbackVersionRegex     = regexp.MustCompile(`(?m)^\s*return\s+"(v[^"]+)"\s*$`)
	defaultVersionRegex      = regexp.MustCompile(`(?m)^\s*const\s+defaultVersion\s*=\s*"([^"]+)"\s*$`)
	versionDefaultRefRegex   = regexp.MustCompile(`(?m)^\s*var\s+Version\s*=\s*defaultVersion\s*$`)
	fallbackDefaultRefRegex  = regexp.MustCompile(`(?m)^\s*return\s+defaultVersion\s*$`)
	semanticVersionTextRegex = regexp.MustCompile(`v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)`)
)

func extractVersionLiteral(content string) (string, error) {
	legacyDefaults := versionLiteralRegex.FindAllStringSubmatch(content, -1)
	legacyFallbacks := fallbackVersionRegex.FindAllStringSubmatch(content, -1)
	singleDefaults := defaultVersionRegex.FindAllStringSubmatch(content, -1)
	versionRefs := versionDefaultRefRegex.FindAllString(content, -1)
	fallbackRefs := fallbackDefaultRefRegex.FindAllString(content, -1)

	legacy := len(legacyDefaults) == 1 && len(legacyFallbacks) == 1 && len(singleDefaults) == 0 && len(versionRefs) == 0 && len(fallbackRefs) == 0
	if legacy {
		if legacyDefaults[0][1] != legacyFallbacks[0][1] {
			return "", fmt.Errorf("Version default %s does not match fallback %s", legacyDefaults[0][1], legacyFallbacks[0][1])
		}
		return legacyDefaults[0][1], nil
	}

	singleSource := len(singleDefaults) == 1 && len(versionRefs) == 1 && len(fallbackRefs) == 1 && len(legacyDefaults) == 0 && len(legacyFallbacks) == 0
	if singleSource {
		return singleDefaults[0][1], nil
	}

	return "", fmt.Errorf("expected exactly one supported version metadata definition")
}

func versionImplementationChanged(baseContent, candidateContent string) bool {
	_, baseErr := extractVersionLiteral(baseContent)
	_, candidateErr := extractVersionLiteral(candidateContent)
	if baseErr != nil || candidateErr != nil {
		return true
	}
	normalize := func(content string) string {
		return semanticVersionTextRegex.ReplaceAllString(content, "v0.0.0")
	}
	return normalize(baseContent) != normalize(candidateContent)
}
