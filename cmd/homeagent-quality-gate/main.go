package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"homeagent/internal/qualitygate"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "internal-diff-coverage" {
		runInternalDiffCoverage(os.Args[2:])
		return
	}
	var (
		changeSpecPath string
		baseRef        string
		diffCoverage   float64
		resultDir      string
	)

	flag.StringVar(&changeSpecPath, "change", "", "Path to changes/<task>.yaml change spec file")
	flag.StringVar(&baseRef, "base", "", "Base commit or branch to compare against (required)")
	flag.Float64Var(&diffCoverage, "diff-coverage", 60.0, "Minimum diff coverage percentage (0..100)")
	flag.StringVar(&resultDir, "result-dir", "", "Directory outside working tree to store results and logs")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s --base <commit> [--change changes/<task>.yaml] [--diff-coverage 60] [--result-dir /path]\n", os.Args[0])
		flag.PrintDefaults()
	}

	flag.Parse()

	if baseRef == "" {
		fmt.Fprintf(os.Stderr, "Error: --base parameter is required\n\n")
		flag.Usage()
		os.Exit(2)
	}

	opts := qualitygate.PipelineOptions{
		RootDir:               ".",
		ChangeSpecPath:        changeSpecPath,
		BaseRef:               baseRef,
		DiffCoverageThreshold: diffCoverage,
		ResultDir:             resultDir,
		Stdout:                os.Stdout,
		Stderr:                os.Stderr,
	}

	_, exitCode, err := qualitygate.RunPipeline(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Pipeline error: %v\n", err)
	}

	os.Exit(exitCode)
}

func runInternalDiffCoverage(args []string) {
	flags := flag.NewFlagSet("internal-diff-coverage", flag.ContinueOnError)
	base := flags.String("base", "", "base commit")
	profile := flags.String("profile", "", "coverage profile")
	minimum := flags.Float64("minimum", 60, "minimum percentage")
	if err := flags.Parse(args); err != nil || *base == "" || *profile == "" || *minimum < 0 || *minimum > 100 {
		os.Exit(2)
	}
	result, err := qualitygate.CalculateDiffCoverage(".", *base, *profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Diff Coverage calculation failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Diff Coverage: %.1f%% (%d/%d statements)\n", result.Percentage, result.Covered, result.Total)
	if result.Percentage+0.000001 < *minimum {
		fmt.Fprintf(os.Stderr, "Diff Coverage %s%% is below the required %.1f%%.\n", strconv.FormatFloat(result.Percentage, 'f', 1, 64), *minimum)
		os.Exit(1)
	}
}
