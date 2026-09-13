// Package workflowperf 提供 GitHub 工作流性能证据的解析与校验能力。
package workflowperf

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
)

var targetValuePattern = regexp.MustCompile(`^[a-z0-9]+$`)

type Target struct {
	Component string `json:"component"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	Output    string `json:"output"`
}

func (t Target) ID() string {
	return t.Component + "-" + t.GOOS + "-" + t.GOARCH
}

func DecodeTargets(reader io.Reader) ([]Target, error) {
	var targets []Target
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&targets); err != nil {
		return nil, fmt.Errorf("decode release targets: %w", err)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("release target manifest is empty")
	}
	seenIDs := make(map[string]struct{}, len(targets))
	seenOutputs := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if target.Component != "server" && target.Component != "agent" {
			return nil, fmt.Errorf("target %q has unknown component %q", target.ID(), target.Component)
		}
		if !targetValuePattern.MatchString(target.GOOS) || !targetValuePattern.MatchString(target.GOARCH) {
			return nil, fmt.Errorf("target %q has invalid platform", target.ID())
		}
		if target.Output == "" || filepath.Base(target.Output) != target.Output {
			return nil, fmt.Errorf("target %q has unsafe output %q", target.ID(), target.Output)
		}
		if _, exists := seenIDs[target.ID()]; exists {
			return nil, fmt.Errorf("duplicate release target %q", target.ID())
		}
		if _, exists := seenOutputs[target.Output]; exists {
			return nil, fmt.Errorf("duplicate release output %q", target.Output)
		}
		seenIDs[target.ID()] = struct{}{}
		seenOutputs[target.Output] = struct{}{}
	}
	return targets, nil
}
