package workflowperf

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

type TestEvent struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
}

type TestTiming struct {
	Package        string  `json:"package"`
	Test           string  `json:"test,omitempty"`
	Outcome        string  `json:"outcome"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
}

type TestReport struct {
	Outcome  string       `json:"outcome"`
	Packages []TestTiming `json:"packages"`
	Tests    []TestTiming `json:"tests"`
}

func ParseTestEvents(reader io.Reader) (TestReport, error) {
	report := TestReport{Outcome: "passed"}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	terminalPackages := 0
	for scanner.Scan() {
		var event TestEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return TestReport{}, fmt.Errorf("decode go test event: %w", err)
		}
		if event.Package == "" {
			return TestReport{}, fmt.Errorf("go test event is missing package")
		}
		if event.Action != "pass" && event.Action != "fail" && event.Action != "skip" {
			continue
		}
		timing := TestTiming{Package: event.Package, Test: event.Test, Outcome: event.Action, ElapsedSeconds: event.Elapsed}
		if event.Test == "" {
			report.Packages = append(report.Packages, timing)
			terminalPackages++
		} else {
			report.Tests = append(report.Tests, timing)
		}
		if event.Action == "fail" {
			report.Outcome = "failed"
		}
	}
	if err := scanner.Err(); err != nil {
		return TestReport{}, fmt.Errorf("read go test events: %w", err)
	}
	if terminalPackages == 0 {
		return TestReport{}, fmt.Errorf("go test event stream has no terminal package result")
	}
	sort.Slice(report.Packages, func(i, j int) bool { return report.Packages[i].ElapsedSeconds > report.Packages[j].ElapsedSeconds })
	sort.Slice(report.Tests, func(i, j int) bool { return report.Tests[i].ElapsedSeconds > report.Tests[j].ElapsedSeconds })
	return report, nil
}
