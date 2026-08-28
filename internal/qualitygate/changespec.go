package qualitygate

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var taskNameRegex = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// ExternalDependency 声明任务新增的外部模块及其引入理由。
type ExternalDependency struct {
	Module    string `json:"module"`
	Rationale string `json:"rationale"`
}

// ChangeSpec 是任务变更清单数据结构。
type ChangeSpec struct {
	Task                 string               `json:"task"`
	Description          string               `json:"description"`
	AllowedPaths         []string             `json:"allowed_paths"`
	ExternalDependencies []ExternalDependency `json:"external_dependencies,omitempty"`
}

// LoadChangeSpecFromFile 从指定文件读取并严格校验变更清单。
func LoadChangeSpecFromFile(filePath string) (*ChangeSpec, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read change spec file %s failed: %w", filePath, err)
	}
	spec, err := ParseChangeSpec(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filePath, err)
	}

	baseName := filepath.Base(filePath)
	expectedName := spec.Task + ".yaml"
	if baseName != expectedName {
		return nil, fmt.Errorf("%s: change spec filename does not match task name '%s' (expected %s)", filePath, spec.Task, expectedName)
	}

	return spec, nil
}

// ValidateChangeSpecLocation validates the repository-relative path contract for --change.
func ValidateChangeSpecLocation(rootDir, filePath string, spec *ChangeSpec) error {
	rootAbs, err := filepath.Abs(rootDir)
	if err != nil {
		return fmt.Errorf("resolve repository root failed: %w", err)
	}
	pathAbs, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("resolve change spec path failed: %w", err)
	}
	if resolvedPath, resolveErr := filepath.EvalSymlinks(pathAbs); resolveErr == nil {
		pathAbs = resolvedPath
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("change spec must reside inside repository changes directory")
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if filepath.Dir(rel) != "changes" || filepath.Ext(rel) != ".yaml" {
		return fmt.Errorf("change spec must match changes/*.yaml, got '%s'", rel)
	}
	if filepath.Base(rel) != spec.Task+".yaml" {
		return fmt.Errorf("change spec filename must match task '%s'", spec.Task)
	}
	return nil
}

// ParseChangeSpec 从 reader 读取并严格解析受控 YAML 子集。
func ParseChangeSpec(r io.Reader) (*ChangeSpec, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read failed: %w", err)
	}

	if bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
		return nil, fmt.Errorf("line 1: BOM header detected, change spec must be UTF-8 without BOM")
	}

	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(content, "\n")

	spec := &ChangeSpec{}
	seenKeys := make(map[string]int)

	var currentSection string
	var currentDep *ExternalDependency
	seenModules := make(map[string]int)

	for idx, line := range lines {
		lineNum := idx + 1
		trimmed := strings.TrimRight(line, " \t")
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(strings.TrimSpace(trimmed), "#") {
			return nil, fmt.Errorf("line %d: comments are not supported in change spec", lineNum)
		}

		// Top-level key
		if !strings.HasPrefix(trimmed, " ") {
			colonIdx := strings.Index(trimmed, ":")
			if colonIdx == -1 {
				return nil, fmt.Errorf("line %d: invalid syntax, expected key-value pair", lineNum)
			}
			key := trimmed[:colonIdx]
			val := strings.TrimSpace(trimmed[colonIdx+1:])

			if _, ok := seenKeys[key]; ok {
				return nil, fmt.Errorf("line %d: duplicate key '%s'", lineNum, key)
			}
			seenKeys[key] = lineNum

			if key != "task" && key != "description" && key != "allowed_paths" && key != "external_dependencies" {
				return nil, fmt.Errorf("line %d: unknown key '%s'", lineNum, key)
			}

			currentSection = key
			currentDep = nil

			switch key {
			case "task":
				if val == "" {
					return nil, fmt.Errorf("line %d: task value cannot be empty", lineNum)
				}
				if strings.ContainsAny(val, "\"'") {
					return nil, fmt.Errorf("line %d: quotes are not permitted in scalar values", lineNum)
				}
				if !taskNameRegex.MatchString(val) {
					return nil, fmt.Errorf("line %d: task '%s' must match [a-z0-9]+(?:-[a-z0-9]+)*", lineNum, val)
				}
				spec.Task = val
			case "description":
				if val == "" {
					return nil, fmt.Errorf("line %d: description value cannot be empty", lineNum)
				}
				if strings.ContainsAny(val, "\"'") {
					return nil, fmt.Errorf("line %d: quotes are not permitted in scalar values", lineNum)
				}
				spec.Description = val
			case "allowed_paths", "external_dependencies":
				if val != "" {
					return nil, fmt.Errorf("line %d: list section '%s' must have empty value on key line", lineNum, key)
				}
			}
			continue
		}

		// Indented lines
		switch currentSection {
		case "allowed_paths":
			if !strings.HasPrefix(trimmed, "  - ") {
				return nil, fmt.Errorf("line %d: invalid indentation in allowed_paths, expected '  - <path>'", lineNum)
			}
			pathVal := strings.TrimSpace(trimmed[4:])
			if pathVal == "" {
				return nil, fmt.Errorf("line %d: allowed_paths entry cannot be empty", lineNum)
			}
			if strings.ContainsAny(pathVal, "\"'") {
				return nil, fmt.Errorf("line %d: quotes are not permitted in allowed_paths", lineNum)
			}
			if err := validateAllowedPath(pathVal); err != nil {
				return nil, fmt.Errorf("line %d: invalid allowed_paths entry '%s': %w", lineNum, pathVal, err)
			}
			spec.AllowedPaths = append(spec.AllowedPaths, pathVal)

		case "external_dependencies":
			if strings.HasPrefix(trimmed, "  - module:") {
				modVal := strings.TrimSpace(trimmed[11:])
				if modVal == "" {
					return nil, fmt.Errorf("line %d: module value cannot be empty", lineNum)
				}
				if strings.ContainsAny(modVal, "\"'") {
					return nil, fmt.Errorf("line %d: quotes are not permitted in module", lineNum)
				}
				if prevLine, exists := seenModules[modVal]; exists {
					return nil, fmt.Errorf("line %d: duplicate module '%s' (previously defined on line %d)", lineNum, modVal, prevLine)
				}
				seenModules[modVal] = lineNum
				spec.ExternalDependencies = append(spec.ExternalDependencies, ExternalDependency{Module: modVal})
				currentDep = &spec.ExternalDependencies[len(spec.ExternalDependencies)-1]
			} else if strings.HasPrefix(trimmed, "    rationale:") {
				if currentDep == nil || currentDep.Rationale != "" {
					return nil, fmt.Errorf("line %d: unexpected rationale outside module context", lineNum)
				}
				ratVal := strings.TrimSpace(trimmed[14:])
				if ratVal == "" {
					return nil, fmt.Errorf("line %d: rationale value cannot be empty", lineNum)
				}
				if strings.ContainsAny(ratVal, "\"'") {
					return nil, fmt.Errorf("line %d: quotes are not permitted in rationale", lineNum)
				}
				currentDep.Rationale = ratVal
			} else {
				return nil, fmt.Errorf("line %d: invalid indentation or structure in external_dependencies", lineNum)
			}
		default:
			return nil, fmt.Errorf("line %d: unexpected indented line outside list section", lineNum)
		}
	}

	if spec.Task == "" {
		return nil, fmt.Errorf("missing required field 'task'")
	}
	if spec.Description == "" {
		return nil, fmt.Errorf("missing required field 'description'")
	}
	if _, ok := seenKeys["allowed_paths"]; !ok || len(spec.AllowedPaths) == 0 {
		return nil, fmt.Errorf("missing or empty required field 'allowed_paths'")
	}
	if _, ok := seenKeys["external_dependencies"]; ok {
		if len(spec.ExternalDependencies) == 0 {
			return nil, fmt.Errorf("external_dependencies specified but empty")
		}
		for _, dep := range spec.ExternalDependencies {
			if dep.Rationale == "" {
				return nil, fmt.Errorf("module '%s' is missing rationale", dep.Module)
			}
		}
	}

	return spec, nil
}

func validateAllowedPath(p string) error {
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("absolute paths are not allowed")
	}
	if strings.Contains(p, `\`) {
		return fmt.Errorf("windows-style backslashes are not allowed")
	}
	clean := filepath.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path traversal ('..') is not allowed")
	}
	if strings.HasSuffix(p, "/**") {
		prefix := strings.TrimSuffix(p, "/**")
		if strings.Contains(prefix, "*") {
			return fmt.Errorf("unsupported wildcard before recursive '/**'")
		}
		return nil
	}
	if strings.Contains(p, "*") {
		return fmt.Errorf("wildcards other than '/**' suffix are not allowed")
	}
	return nil
}

// MatchAllowedPath 检查目标文件相对路径是否被 allowed_paths 允许。
func MatchAllowedPath(filePath string, allowedPaths []string) bool {
	cleanPath := filepath.Clean(filepath.ToSlash(filePath))
	for _, rule := range allowedPaths {
		cleanRule := filepath.Clean(filepath.ToSlash(rule))
		if strings.HasSuffix(rule, "/**") {
			prefix := strings.TrimSuffix(cleanRule, "/**")
			if cleanPath == prefix || strings.HasPrefix(cleanPath, prefix+"/") {
				return true
			}
		} else {
			if cleanPath == cleanRule {
				return true
			}
		}
	}
	return false
}

// FindTrackedChangeSpecs 从 candidate merge-base 差异中查找被修改或新增的 changes/*.yaml 文件
func FindTrackedChangeSpecs(changedFiles []string) []string {
	var specs []string
	for _, f := range changedFiles {
		clean := filepath.ToSlash(filepath.Clean(f))
		if strings.HasPrefix(clean, "changes/") && strings.HasSuffix(clean, ".yaml") {
			specs = append(specs, clean)
		}
	}
	return specs
}
