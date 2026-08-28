package qualitygate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ArchitectureResult 记录架构依赖检查结果。
type ArchitectureResult struct {
	Violations []string `json:"violations"`
	Passed     bool     `json:"passed"`
}

type goListPackage struct {
	Dir        string   `json:"Dir"`
	ImportPath string   `json:"ImportPath"`
	Imports    []string `json:"Imports"`
}

// CheckArchitecture 执行架构依赖与 go.mod 检查。
func CheckArchitecture(rootDir, mergeBase string, spec *ChangeSpec) (*ArchitectureResult, error) {
	res := &ArchitectureResult{
		Passed: true,
	}
	rootAbs, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}
	walkErr := filepath.WalkDir(rootAbs, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			res.Violations = append(res.Violations, fmt.Sprintf("symlink '%s' cannot be resolved: %v", path, err))
			return nil
		}
		rel, err := filepath.Rel(rootAbs, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			res.Violations = append(res.Violations, fmt.Sprintf("symlink '%s' points outside repository", path))
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk repository symlinks failed: %w", walkErr)
	}

	// 1. 获取包依赖信息 go list -json ./...
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = rootDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list failed: %w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			return nil, fmt.Errorf("decode go list output failed: %w", err)
		}
		pkgViolations := checkPackageImports(pkg.ImportPath, pkg.Imports)
		res.Violations = append(res.Violations, pkgViolations...)
	}

	// 2. 检查 go.mod 外部依赖变更
	baseGoModBytes, err := getGitFileContent(rootDir, mergeBase, "go.mod")
	if err != nil {
		return nil, fmt.Errorf("read base go.mod failed: %w", err)
	}
	candidateGoModBytes, err := os.ReadFile(filepath.Join(rootDir, "go.mod"))
	if err != nil {
		return nil, fmt.Errorf("read candidate go.mod failed: %w", err)
	}
	res.Violations = append(res.Violations, checkGoModChanges(string(baseGoModBytes), string(candidateGoModBytes), spec)...)
	baseGoSumBytes, baseSumErr := getGitFileContent(rootDir, mergeBase, "go.sum")
	candidateGoSumBytes, candidateSumErr := os.ReadFile(filepath.Join(rootDir, "go.sum"))
	if baseSumErr == nil || candidateSumErr == nil {
		if baseSumErr != nil {
			baseGoSumBytes = nil
		}
		if candidateSumErr != nil && !os.IsNotExist(candidateSumErr) {
			return nil, fmt.Errorf("read candidate go.sum failed: %w", candidateSumErr)
		}
		res.Violations = append(res.Violations, checkGoSumChanges(string(baseGoSumBytes), string(candidateGoSumBytes), spec)...)
	}

	if len(res.Violations) > 0 {
		res.Passed = false
	}

	return res, nil
}

func checkGoSumChanges(baseContent, candidateContent string, spec *ChangeSpec) []string {
	parse := func(content string) map[string]bool {
		modules := make(map[string]bool)
		for _, line := range strings.Split(content, "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				modules[fields[0]] = true
			}
		}
		return modules
	}
	baseModules, candidateModules := parse(baseContent), parse(candidateContent)
	declared := make(map[string]bool)
	for _, dep := range spec.ExternalDependencies {
		declared[dep.Module] = true
	}
	var violations []string
	for module := range candidateModules {
		if !baseModules[module] && !declared[module] {
			violations = append(violations, fmt.Sprintf("go.sum introduces new external module '%s' which is not declared in change spec external_dependencies", module))
		}
	}
	sort.Strings(violations)
	return violations
}

func checkPackageImports(sourcePkg string, imports []string) []string {
	var violations []string

	// 解析 source module root: homeagent/internal/<module>
	const internalPrefix = "homeagent/internal/"
	var sourceModule string
	if strings.HasPrefix(sourcePkg, internalPrefix) {
		rel := strings.TrimPrefix(sourcePkg, internalPrefix)
		parts := strings.Split(rel, "/")
		sourceModule = parts[0]
	}

	for _, imp := range imports {
		// 检查相对路径导入
		if strings.HasPrefix(imp, ".") || strings.Contains(imp, "/../") {
			violations = append(violations, fmt.Sprintf("package '%s' contains relative import '%s'", sourcePkg, imp))
			continue
		}

		// 检查跨模块私有导入
		if strings.HasPrefix(imp, internalPrefix) {
			rel := strings.TrimPrefix(imp, internalPrefix)
			parts := strings.Split(rel, "/")
			targetModule := parts[0]

			// 如果是跨模块导入 (sourceModule != targetModule)
			if sourceModule != "" && targetModule != sourceModule {
				// 如果导入了 targetModule 下的 internal 或 infrastructure
				for i := 1; i < len(parts); i++ {
					if parts[i] == "internal" || parts[i] == "infrastructure" {
						violations = append(violations, fmt.Sprintf(
							"package '%s' illegally imports private sub-package '%s' of module '%s'",
							sourcePkg, imp, targetModule,
						))
						break
					}
				}
			}
		}
	}

	return violations
}

func checkGoModChanges(baseContent, candidateContent string, spec *ChangeSpec) []string {
	var violations []string
	baseRequires := parseGoModRequires(baseContent)
	candidateRequires := parseGoModRequires(candidateContent)

	declaredDeps := make(map[string]bool)
	for _, dep := range spec.ExternalDependencies {
		declaredDeps[dep.Module] = true
	}

	for mod := range candidateRequires {
		if !baseRequires[mod] {
			// 新增了外部依赖
			if !declaredDeps[mod] {
				violations = append(violations, fmt.Sprintf(
					"go.mod introduces new external dependency '%s' which is not declared in change spec external_dependencies",
					mod,
				))
			}
		}
	}

	return violations
}

func parseGoModRequires(content string) map[string]bool {
	requires := make(map[string]bool)
	scanner := bufio.NewScanner(strings.NewReader(content))
	inRequireBlock := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}

		if line == "require (" {
			inRequireBlock = true
			continue
		}
		if inRequireBlock && line == ")" {
			inRequireBlock = false
			continue
		}

		if inRequireBlock {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				requires[fields[0]] = true
			}
			continue
		}

		if strings.HasPrefix(line, "require ") {
			rest := strings.TrimSpace(strings.TrimPrefix(line, "require"))
			fields := strings.Fields(rest)
			if len(fields) >= 2 {
				requires[fields[0]] = true
			}
		}
	}
	return requires
}

func getGitFileContent(rootDir, commitRef, relPath string) ([]byte, error) {
	cmd := exec.Command("git", "show", fmt.Sprintf("%s:%s", commitRef, relPath))
	cmd.Dir = rootDir
	return cmd.Output()
}
