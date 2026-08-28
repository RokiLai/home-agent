package qualitygate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PipelineOptions 包含流水线启动参数。
type PipelineOptions struct {
	RootDir                string
	ChangeSpecPath         string
	BaseRef                string
	DiffCoverageThreshold  float64
	ResultDir              string
	Stdout                 io.Writer
	Stderr                 io.Writer
	SkipRegressionForTests bool
}

// RunPipeline 执行完整质量门禁流水线。
func RunPipeline(opts PipelineOptions) (*PipelineResult, int, error) {
	if opts.RootDir == "" {
		opts.RootDir = "."
	}
	absRootDir, err := filepath.Abs(opts.RootDir)
	if err != nil {
		return nil, 2, fmt.Errorf("resolve rootDir failed: %w", err)
	}
	if resolvedRoot, resolveErr := filepath.EvalSymlinks(absRootDir); resolveErr == nil {
		absRootDir = resolvedRoot
	}
	opts.RootDir = absRootDir

	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.DiffCoverageThreshold < 0 || opts.DiffCoverageThreshold > 100 {
		return nil, 2, fmt.Errorf("invalid diff coverage threshold: %.1f, must be between 0 and 100", opts.DiffCoverageThreshold)
	}

	// 结果目录准备（必须位于工作树外）
	if opts.ResultDir == "" {
		tmpDir, err := os.MkdirTemp("", "homeagent-quality-gate-*")
		if err != nil {
			return nil, 2, fmt.Errorf("create temp result dir failed: %w", err)
		}
		opts.ResultDir = tmpDir
	} else {
		absResultDir, err := filepath.Abs(opts.ResultDir)
		if err != nil {
			return nil, 2, fmt.Errorf("resolve resultDir failed: %w", err)
		}
		opts.ResultDir = absResultDir
		if resolved, resolveErr := filepath.EvalSymlinks(opts.ResultDir); resolveErr == nil {
			opts.ResultDir = resolved
		}
		// 校验结果目录是否在工作树外
		rel, err := filepath.Rel(opts.RootDir, opts.ResultDir)
		if err == nil && (rel == "." || !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return nil, 2, fmt.Errorf("result-dir '%s' must reside outside git working tree '%s'", opts.ResultDir, opts.RootDir)
		}
	}
	if err := os.MkdirAll(filepath.Join(opts.ResultDir, "logs"), 0755); err != nil {
		return nil, 2, fmt.Errorf("create result logs directory failed: %w", err)
	}

	startTime := time.Now().UTC()
	pipelineRes := &PipelineResult{
		SchemaVersion: 1,
		Status:        StatusPassed,
		StartedAt:     startTime.Format(time.RFC3339),
		Steps: []StepResult{
			{ID: "preflight", Status: StatusNotRun, Reason: "Pending execution", Log: "logs/preflight.log"},
			{ID: "scope", Status: StatusNotRun, Reason: "Pending execution", Log: "logs/scope.log"},
			{ID: "static", Status: StatusNotRun, Reason: "Pending execution", Log: "logs/static.log"},
			{ID: "frontend", Status: StatusNotRun, Reason: "Pending execution", Log: "logs/frontend.log"},
			{ID: "module", Status: StatusNotRun, Reason: "Pending execution", Log: "logs/module.log"},
			{ID: "regression", Status: StatusNotRun, Reason: "Pending execution", Log: "logs/regression.log"},
			{ID: "coverage", Status: StatusNotRun, Reason: "Pending execution", Log: "logs/coverage.log"},
		},
	}

	var allChangedFiles []string
	var diffStatCombined string
	var coverageSummary string
	var spec *ChangeSpec
	var mergeBase string

	setStep := func(idx int, status string, exitCode *int, dur time.Duration, reason string, logContent string) {
		pipelineRes.Steps[idx].Status = status
		pipelineRes.Steps[idx].ExitCode = exitCode
		pipelineRes.Steps[idx].DurationMS = dur.Milliseconds()
		pipelineRes.Steps[idx].Reason = SanitizeOutput(reason)
		logFile := filepath.Join(opts.ResultDir, pipelineRes.Steps[idx].Log)
		if err := os.WriteFile(logFile, []byte(SanitizeOutput(logContent)), 0644); err != nil && pipelineRes.EvidenceError == nil {
			pipelineRes.EvidenceError = fmt.Errorf("write step log %s failed: %w", pipelineRes.Steps[idx].Log, err)
		}
	}

	exitCodeZero := 0
	exitCodeOne := 1

	// ==================== Step 0: Preflight ====================
	step0Start := time.Now()
	var step0Log strings.Builder

	if opts.BaseRef == "" {
		setStep(0, StatusFailed, &exitCodeOne, time.Since(step0Start), "missing required --base parameter", "missing --base")
		return finishPipeline(opts, pipelineRes, 2, allChangedFiles, diffStatCombined, coverageSummary)
	}

	// 校验 BaseRef 为合法 commit
	cmd := exec.Command("git", "rev-parse", "--verify", opts.BaseRef+"^{commit}")
	cmd.Dir = opts.RootDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		reason := fmt.Sprintf("base commit '%s' is invalid: %s", opts.BaseRef, string(out))
		step0Log.WriteString(reason + "\n")
		setStep(0, StatusFailed, &exitCodeOne, time.Since(step0Start), reason, step0Log.String())
		return finishPipeline(opts, pipelineRes, 2, allChangedFiles, diffStatCombined, coverageSummary)
	}
	baseCommit := strings.TrimSpace(string(out))
	pipelineRes.BaseCommit = baseCommit

	// 计算 merge-base
	mb, err := ResolveMergeBase(opts.RootDir, baseCommit)
	if err != nil {
		reason := fmt.Sprintf("calculate merge-base failed: %v", err)
		step0Log.WriteString(reason + "\n")
		setStep(0, StatusFailed, &exitCodeOne, time.Since(step0Start), reason, step0Log.String())
		return finishPipeline(opts, pipelineRes, 2, allChangedFiles, diffStatCombined, coverageSummary)
	}
	mergeBase = mb
	pipelineRes.MergeBase = mergeBase

	// 获取 candidate HEAD commit
	cmdHead := exec.Command("git", "rev-parse", "HEAD")
	cmdHead.Dir = opts.RootDir
	headOut, err := cmdHead.CombinedOutput()
	if err != nil {
		reason := fmt.Sprintf("get candidate HEAD failed: %v", err)
		step0Log.WriteString(reason + "\n")
		setStep(0, StatusFailed, &exitCodeOne, time.Since(step0Start), reason, step0Log.String())
		return finishPipeline(opts, pipelineRes, 2, allChangedFiles, diffStatCombined, coverageSummary)
	}
	pipelineRes.CandidateCommit = strings.TrimSpace(string(headOut))

	// 解析变更清单
	if opts.ChangeSpecPath != "" {
		if !filepath.IsAbs(opts.ChangeSpecPath) {
			opts.ChangeSpecPath = filepath.Join(opts.RootDir, opts.ChangeSpecPath)
		}
		step0Log.WriteString(fmt.Sprintf("Loading explicit change spec: %s\n", opts.ChangeSpecPath))
		loaded, err := LoadChangeSpecFromFile(opts.ChangeSpecPath)
		if err != nil {
			reason := fmt.Sprintf("load change spec '%s' failed: %v", opts.ChangeSpecPath, err)
			step0Log.WriteString(reason + "\n")
			setStep(0, StatusFailed, &exitCodeOne, time.Since(step0Start), reason, step0Log.String())
			return finishPipeline(opts, pipelineRes, 2, allChangedFiles, diffStatCombined, coverageSummary)
		}
		spec = loaded
		if err := ValidateChangeSpecLocation(opts.RootDir, opts.ChangeSpecPath, spec); err != nil {
			reason := fmt.Sprintf("invalid explicit change spec location: %v", err)
			setStep(0, StatusFailed, &exitCodeOne, time.Since(step0Start), reason, reason+"\n")
			return finishPipeline(opts, pipelineRes, 2, allChangedFiles, diffStatCombined, coverageSummary)
		}
	} else {
		// 差异中自动发现唯一已跟踪 changes/*.yaml
		diffCmd := exec.Command("git", "diff", "--name-status", "-z", "--find-renames", mergeBase)
		diffCmd.Dir = opts.RootDir
		diffOut, _ := diffCmd.Output()
		entries, _ := parseNameStatusZ(diffOut)
		var changedFileList []string
		for _, e := range entries {
			changedFileList = append(changedFileList, e.Path)
		}
		discovered := FindTrackedChangeSpecs(changedFileList)
		if len(discovered) == 0 {
			reason := "no changes/*.yaml found in candidate diff; specify --change or commit your change spec"
			step0Log.WriteString(reason + "\n")
			setStep(0, StatusFailed, &exitCodeOne, time.Since(step0Start), reason, step0Log.String())
			return finishPipeline(opts, pipelineRes, 2, allChangedFiles, diffStatCombined, coverageSummary)
		}
		if len(discovered) > 1 {
			reason := fmt.Sprintf("multiple changes/*.yaml found in diff (%v); expected exactly one", discovered)
			step0Log.WriteString(reason + "\n")
			setStep(0, StatusFailed, &exitCodeOne, time.Since(step0Start), reason, step0Log.String())
			return finishPipeline(opts, pipelineRes, 2, allChangedFiles, diffStatCombined, coverageSummary)
		}

		specPath := filepath.Join(opts.RootDir, discovered[0])
		step0Log.WriteString(fmt.Sprintf("Discovered change spec: %s\n", discovered[0]))
		loaded, err := LoadChangeSpecFromFile(specPath)
		if err != nil {
			reason := fmt.Sprintf("load change spec '%s' failed: %v", specPath, err)
			step0Log.WriteString(reason + "\n")
			setStep(0, StatusFailed, &exitCodeOne, time.Since(step0Start), reason, step0Log.String())
			return finishPipeline(opts, pipelineRes, 2, allChangedFiles, diffStatCombined, coverageSummary)
		}
		spec = loaded
		opts.ChangeSpecPath = specPath
	}

	pipelineRes.Task = spec.Task
	step0Log.WriteString(fmt.Sprintf("Task: %s\nDescription: %s\nAllowed Paths: %v\n", spec.Task, spec.Description, spec.AllowedPaths))
	setStep(0, StatusPassed, &exitCodeZero, time.Since(step0Start), "Preflight checks passed", step0Log.String())

	// ==================== Step 1: Scope ====================
	step1Start := time.Now()
	var step1Log strings.Builder
	scopeRes, err := CheckScope(opts.RootDir, baseCommit, spec, opts.ChangeSpecPath)
	if err != nil {
		reason := fmt.Sprintf("check scope failed with error: %v", err)
		step1Log.WriteString(reason + "\n")
		setStep(1, StatusFailed, &exitCodeOne, time.Since(step1Start), reason, step1Log.String())
		return finishPipeline(opts, pipelineRes, 1, allChangedFiles, diffStatCombined, coverageSummary)
	}

	allChangedFiles = scopeRes.AllChangedFiles
	diffStatCombined = scopeRes.DiffStatCombined
	step1Log.WriteString(scopeRes.DiffStatCombined + "\n")

	if !scopeRes.Passed {
		reason := fmt.Sprintf("scope check failed with %d violation(s)", len(scopeRes.Violations))
		for _, v := range scopeRes.Violations {
			step1Log.WriteString("VIOLATION: " + v + "\n")
		}
		setStep(1, StatusFailed, &exitCodeOne, time.Since(step1Start), reason, step1Log.String())
		return finishPipeline(opts, pipelineRes, 1, allChangedFiles, diffStatCombined, coverageSummary)
	}
	setStep(1, StatusPassed, &exitCodeZero, time.Since(step1Start), fmt.Sprintf("Scope and formatting passed (%d files changed)", len(allChangedFiles)), step1Log.String())

	// ==================== Step 2: Static ====================
	step2Start := time.Now()
	var step2Log strings.Builder
	var staticFailed bool

	// 1. 架构依赖
	archRes, err := CheckArchitecture(opts.RootDir, mergeBase, spec)
	if err != nil {
		step2Log.WriteString(fmt.Sprintf("CheckArchitecture error: %v\n", err))
		staticFailed = true
	} else if !archRes.Passed {
		for _, v := range archRes.Violations {
			step2Log.WriteString("ARCH VIOLATION: " + v + "\n")
		}
		staticFailed = true
	} else {
		step2Log.WriteString("Architecture dependency check passed.\n")
	}

	// 2. 客户端版本联动
	clientRes, err := CheckClientVersion(opts.RootDir, mergeBase, allChangedFiles)
	if err != nil {
		step2Log.WriteString(fmt.Sprintf("CheckClientVersion error: %v\n", err))
		staticFailed = true
	} else if !clientRes.Passed {
		for _, v := range clientRes.Violations {
			step2Log.WriteString("CLIENT VERSION VIOLATION: " + v + "\n")
		}
		staticFailed = true
	} else {
		step2Log.WriteString(fmt.Sprintf("Client version check passed (base: %s, candidate: %s, behavior_changed: %v).\n",
			clientRes.BaseVersion, clientRes.CandidateVersion, clientRes.BehaviorChanged))
	}

	if staticFailed {
		setStep(2, StatusFailed, &exitCodeOne, time.Since(step2Start), "Static and architecture checks failed", step2Log.String())
		return finishPipeline(opts, pipelineRes, 1, allChangedFiles, diffStatCombined, coverageSummary)
	}
	setStep(2, StatusPassed, &exitCodeZero, time.Since(step2Start), "Architecture and client version checks passed", step2Log.String())

	// ==================== Step 3: Frontend ====================
	step3Start := time.Now()
	frontendModified := false
	for _, f := range allChangedFiles {
		if strings.HasPrefix(f, "internal/ui/") || strings.HasSuffix(f, ".js") || strings.HasSuffix(f, ".mjs") || strings.HasSuffix(f, ".css") || strings.HasSuffix(f, ".html") {
			frontendModified = true
			break
		}
	}

	if frontendModified {
		frontendScript := filepath.Join(opts.RootDir, "scripts/check-frontend-syntax.mjs")
		if _, err := os.Stat(frontendScript); err != nil {
			reason := fmt.Sprintf("frontend syntax checker unavailable: %v", err)
			setStep(3, StatusFailed, &exitCodeOne, time.Since(step3Start), reason, reason+"\n")
			return finishPipeline(opts, pipelineRes, 1, allChangedFiles, diffStatCombined, coverageSummary)
		}
		cmdNode := exec.Command("node", frontendScript)
		cmdNode.Dir = opts.RootDir
		nodeOut, nodeErr := cmdNode.CombinedOutput()
		if nodeErr != nil {
			actualExit := commandExitCode(nodeErr)
			setStep(3, StatusFailed, &actualExit, time.Since(step3Start), "Frontend syntax check failed", string(nodeOut))
			return finishPipeline(opts, pipelineRes, 1, allChangedFiles, diffStatCombined, coverageSummary)
		}
		setStep(3, StatusPassed, &exitCodeZero, time.Since(step3Start), "Frontend syntax check passed", string(nodeOut))
	} else {
		setStep(3, StatusNotApplicable, &exitCodeZero, time.Since(step3Start), "No frontend files modified", "Frontend syntax check not applicable: no frontend files modified.\n")
	}

	// ==================== Step 4: Module ====================
	step4Start := time.Now()
	var step4Log strings.Builder

	pkgs, err := collectChangedPackages(opts.RootDir, allChangedFiles)
	if err != nil {
		reason := fmt.Sprintf("resolve changed Go packages failed: %v", err)
		setStep(4, StatusFailed, &exitCodeOne, time.Since(step4Start), reason, reason+"\n")
		return finishPipeline(opts, pipelineRes, 1, allChangedFiles, diffStatCombined, coverageSummary)
	}

	if len(pkgs) == 0 {
		setStep(4, StatusNotApplicable, &exitCodeZero, time.Since(step4Start), "No Go source packages modified", "No Go files modified\n")
	} else {
		step4Log.WriteString(fmt.Sprintf("Running module tests for packages: %s\n", strings.Join(pkgs, ", ")))

		args := append([]string{"test", "-count=1"}, pkgs...)
		testCmd := exec.Command("go", args...)
		testCmd.Dir = opts.RootDir
		testOut, testErr := testCmd.CombinedOutput()
		step4Log.Write(testOut)

		if testErr != nil {
			actualExit := commandExitCode(testErr)
			setStep(4, StatusFailed, &actualExit, time.Since(step4Start), "Changed package tests failed", step4Log.String())
			return finishPipeline(opts, pipelineRes, 1, allChangedFiles, diffStatCombined, coverageSummary)
		}
		setStep(4, StatusPassed, &exitCodeZero, time.Since(step4Start), fmt.Sprintf("Module tests passed (%d package(s))", len(pkgs)), step4Log.String())
	}

	// ==================== Step 5: Regression ====================
	step5Start := time.Now()
	var step5Log strings.Builder
	coverageProfile := filepath.Join(opts.ResultDir, "coverage.out")

	if opts.SkipRegressionForTests {
		step5Log.WriteString("Skipping full regression for fast unit test.\n")
		setStep(5, StatusPassed, &exitCodeZero, time.Since(step5Start), "Full regression skipped in mock test", step5Log.String())
	} else {
		step5Log.WriteString(fmt.Sprintf("Running full race regression: go test -count=1 -race -coverprofile=%s ./...\n", coverageProfile))
		regCmd := exec.Command("go", "test", "-count=1", "-race", "-coverprofile="+coverageProfile, "./...")
		regCmd.Dir = opts.RootDir
		regOut, regErr := regCmd.CombinedOutput()
		step5Log.Write(regOut)

		if regErr != nil {
			actualExit := commandExitCode(regErr)
			setStep(5, StatusFailed, &actualExit, time.Since(step5Start), "Full race regression tests failed", step5Log.String())
			return finishPipeline(opts, pipelineRes, 1, allChangedFiles, diffStatCombined, coverageSummary)
		}
		setStep(5, StatusPassed, &exitCodeZero, time.Since(step5Start), "Full -race regression passed", step5Log.String())
	}

	// ==================== Step 6: Coverage ====================
	step6Start := time.Now()
	var step6Log strings.Builder

	if opts.SkipRegressionForTests {
		coverageSummary = fmt.Sprintf("Diff Coverage: 100.0%% (Mock/Mock statements)\nThreshold: %.1f%%\n", opts.DiffCoverageThreshold)
		step6Log.WriteString(coverageSummary)
		setStep(6, StatusPassed, &exitCodeZero, time.Since(step6Start), "Diff coverage passed in mock test", step6Log.String())
	} else {
		// 调用 scripts/check-diff-coverage.sh
		covScript := filepath.Join(opts.RootDir, "scripts/check-diff-coverage.sh")
		covCmd := exec.Command(covScript, mergeBase, fmt.Sprintf("%.1f", opts.DiffCoverageThreshold), coverageProfile)
		covCmd.Dir = opts.RootDir
		covCmd.Env = append(os.Environ(), "COVERAGE_PROFILE="+coverageProfile)
		covOut, covErr := covCmd.CombinedOutput()
		coverageSummary = string(covOut)
		step6Log.Write(covOut)

		if covErr != nil {
			actualExit := commandExitCode(covErr)
			setStep(6, StatusFailed, &actualExit, time.Since(step6Start), "Diff Coverage below threshold or calculation failed", step6Log.String())
			return finishPipeline(opts, pipelineRes, 1, allChangedFiles, diffStatCombined, coverageSummary)
		}
		setStep(6, StatusPassed, &exitCodeZero, time.Since(step6Start), "Diff Coverage requirement satisfied", step6Log.String())
	}

	return finishPipeline(opts, pipelineRes, 0, allChangedFiles, diffStatCombined, coverageSummary)
}

func finishPipeline(opts PipelineOptions, res *PipelineResult, exitCode int, changedFiles []string, diffStat, coverageSummary string) (*PipelineResult, int, error) {
	res.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	res.WorkingTreeDirty = HasWorkingTreeDirty(opts.RootDir, res.MergeBase, opts.ChangeSpecPath)

	// 计算 change_digest
	if res.MergeBase != "" {
		digest, err := CalculateChangeDigest(opts.RootDir, res.MergeBase, changedFiles, opts.ChangeSpecPath)
		if err != nil {
			res.EvidenceError = fmt.Errorf("calculate change digest failed: %w", err)
		} else {
			res.ChangeDigest = digest
		}
	}

	// 判定整体状态
	overallPassed := aggregatePipelinePassed(res.Steps, res.EvidenceError)
	if res.EvidenceError != nil {
		exitCode = 1
	}
	if overallPassed {
		res.Status = StatusPassed
	} else {
		res.Status = StatusFailed
	}

	// 写入产物文件
	if err := WriteResultFiles(opts.ResultDir, res, changedFiles, diffStat, coverageSummary); err != nil {
		return res, 2, fmt.Errorf("write quality gate evidence failed: %w", err)
	}

	// 格式化输出终端汇总
	printPipelineSummary(opts.Stdout, res, opts.ResultDir)

	return res, exitCode, nil
}

func collectChangedPackages(rootDir string, changedFiles []string) ([]string, error) {
	packages := make(map[string]struct{})
	for _, file := range changedFiles {
		if !strings.HasSuffix(file, ".go") {
			continue
		}
		dir := filepath.Dir(file)
		absDir := filepath.Join(rootDir, dir)
		info, err := os.Stat(absDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat changed package directory %s failed: %w", dir, err)
		}
		if !info.IsDir() {
			continue
		}
		cmd := exec.Command("go", "list", "./"+filepath.ToSlash(dir))
		cmd.Dir = rootDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("go list ./%s failed: %w: %s", filepath.ToSlash(dir), err, strings.TrimSpace(string(out)))
		}
		pkg := strings.TrimSpace(string(out))
		if pkg == "" {
			return nil, fmt.Errorf("go list ./%s returned an empty package", filepath.ToSlash(dir))
		}
		packages[pkg] = struct{}{}
	}
	result := make([]string, 0, len(packages))
	for pkg := range packages {
		result = append(result, pkg)
	}
	sort.Strings(result)
	return result, nil
}

func commandExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

func aggregatePipelinePassed(steps []StepResult, evidenceErr error) bool {
	if evidenceErr != nil {
		return false
	}
	for _, step := range steps {
		if step.Status == StatusFailed || step.Status == StatusBlocked || step.Status == StatusNotRun {
			return false
		}
	}
	return true
}

func printPipelineSummary(w io.Writer, res *PipelineResult, resultDir string) {
	fmt.Fprintln(w, "\n=======================================================")
	fmt.Fprintf(w, "  HomeAgent Quality Gate Summary (Task: %s)\n", res.Task)
	fmt.Fprintln(w, "=======================================================")
	fmt.Fprintf(w, "Base Commit     : %s\n", res.BaseCommit)
	fmt.Fprintf(w, "Candidate Commit: %s\n", res.CandidateCommit)
	fmt.Fprintf(w, "Merge Base      : %s\n", res.MergeBase)
	fmt.Fprintf(w, "Working Dirty   : %v\n", res.WorkingTreeDirty)
	fmt.Fprintf(w, "Change Digest   : %s\n", res.ChangeDigest)
	fmt.Fprintln(w, "-------------------------------------------------------")
	fmt.Fprintf(w, "%-12s | %-15s | %-8s | %s\n", "STEP", "STATUS", "DURATION", "REASON")
	fmt.Fprintln(w, "-------------+-----------------+----------+--------------------")
	for _, s := range res.Steps {
		durStr := fmt.Sprintf("%dms", s.DurationMS)
		fmt.Fprintf(w, "%-12s | %-15s | %-8s | %s\n", s.ID, s.Status, durStr, s.Reason)
	}
	fmt.Fprintln(w, "-------------------------------------------------------")
	if res.Status == StatusPassed {
		fmt.Fprintf(w, "OVERALL RESULT  : \x1b[32mPASSED\x1b[0m\n")
	} else {
		fmt.Fprintf(w, "OVERALL RESULT  : \x1b[31mFAILED\x1b[0m\n")
	}
	fmt.Fprintf(w, "Artifacts Dir   : %s\n", resultDir)
	fmt.Fprintln(w, "=======================================================")
}
