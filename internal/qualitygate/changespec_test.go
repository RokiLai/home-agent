package qualitygate

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseChangeSpec_Valid(t *testing.T) {
	content := `task: example-task
description: An example task description
allowed_paths:
  - scripts/quality-gate.sh
  - internal/qualitygate/**
external_dependencies:
  - module: example.com/foo
    rationale: Foo library is needed for bar
`
	spec, err := ParseChangeSpec(strings.NewReader(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Task != "example-task" {
		t.Errorf("expected task 'example-task', got '%s'", spec.Task)
	}
	if spec.Description != "An example task description" {
		t.Errorf("expected description 'An example task description', got '%s'", spec.Description)
	}
	if len(spec.AllowedPaths) != 2 || spec.AllowedPaths[0] != "scripts/quality-gate.sh" || spec.AllowedPaths[1] != "internal/qualitygate/**" {
		t.Errorf("unexpected allowed_paths: %v", spec.AllowedPaths)
	}
	if len(spec.ExternalDependencies) != 1 {
		t.Fatalf("expected 1 external dependency, got %d", len(spec.ExternalDependencies))
	}
	if spec.ExternalDependencies[0].Module != "example.com/foo" || spec.ExternalDependencies[0].Rationale != "Foo library is needed for bar" {
		t.Errorf("unexpected external dependency: %+v", spec.ExternalDependencies[0])
	}
}

func TestParseChangeSpec_WithoutExternalDependencies(t *testing.T) {
	content := `task: simple-task
description: Simple description without external dependencies
allowed_paths:
  - scripts/simple.sh
`
	spec, err := ParseChangeSpec(strings.NewReader(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Task != "simple-task" {
		t.Errorf("expected simple-task, got %s", spec.Task)
	}
	if len(spec.ExternalDependencies) != 0 {
		t.Errorf("expected empty external dependencies, got %v", spec.ExternalDependencies)
	}
}

func TestParseChangeSpec_CRLFNormalized(t *testing.T) {
	content := "task: crlf-task\r\ndescription: CRLF description\r\nallowed_paths:\r\n  - foo.txt\r\n"
	spec, err := ParseChangeSpec(strings.NewReader(content))
	if err != nil {
		t.Fatalf("unexpected error with CRLF: %v", err)
	}
	if spec.Task != "crlf-task" {
		t.Errorf("expected crlf-task, got %s", spec.Task)
	}
}

func TestParseChangeSpec_NegativeCases(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		errContains string
	}{
		{
			name:        "BOM header",
			content:     "\xef\xbb\xbftask: bom-task\ndescription: d\nallowed_paths:\n  - a\n",
			errContains: "BOM",
		},
		{
			name:        "Invalid task format (uppercase)",
			content:     "task: InvalidTask\ndescription: d\nallowed_paths:\n  - a\n",
			errContains: "task",
		},
		{
			name:        "Invalid task format (underscore)",
			content:     "task: invalid_task\ndescription: d\nallowed_paths:\n  - a\n",
			errContains: "task",
		},
		{
			name:        "Empty description",
			content:     "task: task-one\ndescription: \nallowed_paths:\n  - a\n",
			errContains: "description",
		},
		{
			name:        "Empty allowed_paths list",
			content:     "task: task-one\ndescription: d\nallowed_paths:\n",
			errContains: "allowed_paths",
		},
		{
			name:        "Allowed path absolute",
			content:     "task: task-one\ndescription: d\nallowed_paths:\n  - /etc/passwd\n",
			errContains: "allowed_paths",
		},
		{
			name:        "Allowed path dot dot",
			content:     "task: task-one\ndescription: d\nallowed_paths:\n  - ../foo\n",
			errContains: "allowed_paths",
		},
		{
			name:        "Allowed path unsupported glob",
			content:     "task: task-one\ndescription: d\nallowed_paths:\n  - internal/*.go\n",
			errContains: "allowed_paths",
		},
		{
			name:        "Quotes not allowed in scalar",
			content:     "task: \"task-one\"\ndescription: d\nallowed_paths:\n  - a\n",
			errContains: "quote",
		},
		{
			name:        "Comments not allowed",
			content:     "# comment\ntask: task-one\ndescription: d\nallowed_paths:\n  - a\n",
			errContains: "comment",
		},
		{
			name:        "Unknown top-level key",
			content:     "task: task-one\ndescription: d\nallowed_paths:\n  - a\nextra: bad\n",
			errContains: "unknown key",
		},
		{
			name:        "Duplicate top-level key",
			content:     "task: task-one\ntask: task-two\ndescription: d\nallowed_paths:\n  - a\n",
			errContains: "duplicate key",
		},
		{
			name:        "External dependency duplicate module",
			content:     "task: task-one\ndescription: d\nallowed_paths:\n  - a\nexternal_dependencies:\n  - module: foo\n    rationale: r1\n  - module: foo\n    rationale: r2\n",
			errContains: "duplicate",
		},
		{
			name:        "External dependency missing rationale",
			content:     "task: task-one\ndescription: d\nallowed_paths:\n  - a\nexternal_dependencies:\n  - module: foo\n",
			errContains: "rationale",
		},
		{
			name:        "External dependency empty rationale",
			content:     "task: task-one\ndescription: d\nallowed_paths:\n  - a\nexternal_dependencies:\n  - module: foo\n    rationale: \n",
			errContains: "rationale",
		},
		{
			name:        "Invalid indentation",
			content:     "task: task-one\ndescription: d\nallowed_paths:\n - a\n",
			errContains: "indentation",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseChangeSpec(strings.NewReader(tc.content))
			if err == nil {
				t.Fatalf("expected error containing '%s', got nil", tc.errContains)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.errContains)) {
				t.Errorf("expected error containing '%s', got '%v'", tc.errContains, err)
			}
		})
	}
}

func TestMatchAllowedPath(t *testing.T) {
	rules := []string{
		"scripts/quality-gate.sh",
		"internal/qualitygate/**",
		"docs/design.md",
	}

	if !MatchAllowedPath("scripts/quality-gate.sh", rules) {
		t.Errorf("exact match failed")
	}
	if !MatchAllowedPath("internal/qualitygate/changespec.go", rules) {
		t.Errorf("recursive glob match failed")
	}
	if !MatchAllowedPath("internal/qualitygate/sub/sub.go", rules) {
		t.Errorf("recursive nested glob match failed")
	}
	if MatchAllowedPath("internal/other/foo.go", rules) {
		t.Errorf("unrelated path matched unexpectedly")
	}
	if MatchAllowedPath("scripts/other.sh", rules) {
		t.Errorf("unrelated script matched unexpectedly")
	}
}

func TestFindTrackedChangeSpecs(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  int
	}{
		{"zero", []string{"README.md"}, 0},
		{"one", []string{"changes/my-task.yaml", "README.md"}, 1},
		{"multiple", []string{"changes/a.yaml", "changes/b.yaml"}, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(FindTrackedChangeSpecs(tc.files)); got != tc.want {
				t.Fatalf("got %d specs, want %d", got, tc.want)
			}
		})
	}
}

func TestValidateChangeSpecLocation(t *testing.T) {
	root := t.TempDir()
	spec := &ChangeSpec{Task: "quality-gate"}
	valid := filepath.Join(root, "changes", "quality-gate.yaml")
	if err := ValidateChangeSpecLocation(root, valid, spec); err != nil {
		t.Fatalf("valid location rejected: %v", err)
	}
	for _, invalid := range []string{
		filepath.Join(root, "quality-gate.yaml"),
		filepath.Join(root, "docs", "quality-gate.yaml"),
		filepath.Join(root, "changes", "other.yaml"),
		filepath.Join(filepath.Dir(root), "changes", "quality-gate.yaml"),
	} {
		if err := ValidateChangeSpecLocation(root, invalid, spec); err == nil {
			t.Errorf("invalid location accepted: %s", invalid)
		}
	}
}
