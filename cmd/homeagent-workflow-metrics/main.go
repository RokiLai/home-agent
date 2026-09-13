package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"homeagent/internal/workflowperf"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: homeagent-workflow-metrics <targets|test-report>")
	}
	switch args[0] {
	case "targets":
		flags := flag.NewFlagSet("targets", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		manifest := flags.String("manifest", "configs/release-targets.json", "release target manifest")
		component := flags.String("component", "", "server or agent")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *component != "server" && *component != "agent" {
			return fmt.Errorf("component must be server or agent")
		}
		file, err := os.Open(*manifest)
		if err != nil {
			return fmt.Errorf("open target manifest: %w", err)
		}
		defer file.Close()
		targets, err := workflowperf.DecodeTargets(file)
		if err != nil {
			return err
		}
		matched := 0
		for _, target := range targets {
			if target.Component != *component {
				continue
			}
			matched++
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", target.Component, target.GOOS, target.GOARCH, target.Output)
		}
		if matched == 0 {
			return fmt.Errorf("manifest has no %s targets", *component)
		}
		return nil
	case "test-report":
		if len(args) != 1 {
			return errors.New("test-report does not accept arguments")
		}
		report, err := workflowperf.ParseTestEvents(stdin)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
