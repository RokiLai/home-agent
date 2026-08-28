package qualitygate

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DiffCoverageResult records statement coverage intersecting changed Go lines.
type DiffCoverageResult struct {
	Percentage float64
	Covered    int
	Total      int
}

var (
	hunkHeaderPattern = regexp.MustCompile(`^@@ -[0-9]+(?:,[0-9]+)? \+([0-9]+)(?:,([0-9]+))? @@`)
	coverLinePattern  = regexp.MustCompile(`^(.+):([0-9]+)\.[0-9]+,([0-9]+)\.[0-9]+ ([0-9]+) ([0-9]+)$`)
)

// CalculateDiffCoverage computes diff coverage without line-oriented filename transport.
func CalculateDiffCoverage(rootDir, baseRef, coverageProfile string) (*DiffCoverageResult, error) {
	changed, err := changedGoLines(rootDir, baseRef)
	if err != nil {
		return nil, err
	}
	moduleCmd := exec.Command("go", "list", "-m")
	moduleCmd.Dir = rootDir
	moduleOut, err := moduleCmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("go list -m failed: %w: %s", err, strings.TrimSpace(string(moduleOut)))
	}
	modulePrefix := strings.TrimSpace(string(moduleOut)) + "/"
	profile, err := os.Open(coverageProfile)
	if err != nil {
		return nil, fmt.Errorf("open coverage profile failed: %w", err)
	}
	defer profile.Close()

	result := &DiffCoverageResult{}
	scanner := bufio.NewScanner(profile)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "mode: ") {
			continue
		}
		matches := coverLinePattern.FindStringSubmatch(line)
		if len(matches) != 6 {
			return nil, fmt.Errorf("malformed coverage profile line %q", line)
		}
		path := matches[1]
		if strings.HasPrefix(path, modulePrefix) {
			path = strings.TrimPrefix(path, modulePrefix)
		}
		start, _ := strconv.Atoi(matches[2])
		end, _ := strconv.Atoi(matches[3])
		statements, _ := strconv.Atoi(matches[4])
		executions, _ := strconv.Atoi(matches[5])
		intersects := false
		for lineNumber := start; lineNumber <= end; lineNumber++ {
			if changed[path][lineNumber] {
				intersects = true
				break
			}
		}
		if intersects {
			result.Total += statements
			if executions > 0 {
				result.Covered += statements
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read coverage profile failed: %w", err)
	}
	if result.Total == 0 {
		result.Percentage = 100
	} else {
		result.Percentage = float64(result.Covered) * 100 / float64(result.Total)
	}
	return result, nil
}

func changedGoLines(rootDir, baseRef string) (map[string]map[int]bool, error) {
	changed := make(map[string]map[int]bool)
	diffNames := exec.Command("git", "diff", "--name-only", "-z", "--diff-filter=ACMRT", baseRef, "--", "*.go")
	diffNames.Dir = rootDir
	nameOutput, err := diffNames.Output()
	if err != nil {
		return nil, fmt.Errorf("list changed Go files failed: %w", err)
	}
	tracked := parseNulStrings(nameOutput)
	sort.Strings(tracked)
	for _, path := range tracked {
		if strings.Contains(path, "\n") || strings.Contains(path, "\r") {
			return nil, fmt.Errorf("Go coverage tool cannot safely represent newline in path %q", path)
		}
		cmd := exec.Command("git", "diff", "--unified=0", "--no-ext-diff", "--no-textconv", baseRef, "--", path)
		cmd.Dir = rootDir
		output, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("read changed lines for %q failed: %w", path, err)
		}
		lines := make(map[int]bool)
		for _, rawLine := range bytes.Split(output, []byte{'\n'}) {
			matches := hunkHeaderPattern.FindSubmatch(rawLine)
			if len(matches) == 0 {
				continue
			}
			first, _ := strconv.Atoi(string(matches[1]))
			count := 1
			if len(matches[2]) > 0 {
				count, _ = strconv.Atoi(string(matches[2]))
			}
			for offset := 0; offset < count; offset++ {
				lines[first+offset] = true
			}
		}
		changed[path] = lines
	}

	untrackedCmd := exec.Command("git", "ls-files", "--others", "--exclude-standard", "-z", "--", "*.go")
	untrackedCmd.Dir = rootDir
	untrackedOutput, err := untrackedCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list untracked Go files failed: %w", err)
	}
	for _, path := range parseNulStrings(untrackedOutput) {
		if strings.Contains(path, "\n") || strings.Contains(path, "\r") {
			return nil, fmt.Errorf("Go coverage tool cannot safely represent newline in path %q", path)
		}
		content, err := os.ReadFile(filepath.Join(rootDir, filepath.FromSlash(path)))
		if err != nil {
			return nil, fmt.Errorf("read untracked Go file %q failed: %w", path, err)
		}
		lineCount := bytes.Count(content, []byte{'\n'})
		if len(content) > 0 && content[len(content)-1] != '\n' {
			lineCount++
		}
		lines := make(map[int]bool, lineCount)
		for line := 1; line <= lineCount; line++ {
			lines[line] = true
		}
		changed[path] = lines
	}
	return changed, nil
}
