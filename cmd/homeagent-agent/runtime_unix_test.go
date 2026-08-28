//go:build darwin || linux

package main

import (
	"errors"
	"runtime"
	"testing"
	"time"

	"homeagent/internal/device"
)

func TestGetSystemRuntimeFactsReportsDarwinUptime(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin runtime observation")
	}
	facts := getSystemRuntimeFacts()
	if facts.UptimeSeconds <= 0 {
		t.Fatalf("UptimeSeconds = %d, want positive Darwin system uptime", facts.UptimeSeconds)
	}
}

func TestParseLinuxUptime(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want int64
		ok   bool
	}{
		{name: "valid", raw: []byte("86400.75 123.00\n"), want: 86400, ok: true},
		{name: "subsecond is absent", raw: []byte("0.75 0.10\n")},
		{name: "zero is absent", raw: []byte("0 0\n")},
		{name: "negative is absent", raw: []byte("-1.0 0\n")},
		{name: "invalid is absent", raw: []byte("not-a-number\n")},
		{name: "empty is absent", raw: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseLinuxUptime(tt.raw)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("parseLinuxUptime(%q) = (%d, %v), want (%d, %v)", tt.raw, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestPopulateDarwinUptime(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		bootTime time.Time
		err      error
		want     int64
	}{
		{name: "valid", bootTime: now.Add(-49*time.Hour - 1500*time.Millisecond), want: 176401},
		{name: "equal is absent", bootTime: now},
		{name: "future is absent", bootTime: now.Add(time.Second)},
		{name: "read failure is isolated", err: errors.New("sysctl failed")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			facts := &device.RuntimeFacts{LogicalCPUCount: 8}
			populateDarwinUptime(facts, now, func() (time.Time, error) {
				return tt.bootTime, tt.err
			})
			if facts.UptimeSeconds != tt.want {
				t.Fatalf("uptime = %d, want %d", facts.UptimeSeconds, tt.want)
			}
			if facts.LogicalCPUCount != 8 {
				t.Fatalf("uptime failure changed unrelated facts: %+v", facts)
			}
		})
	}
}
