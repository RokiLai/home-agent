package main

import (
	"errors"
	"testing"

	"homeagent/internal/device"
)

func TestParseIOPlatformUUID(t *testing.T) {
	got := parseIOPlatformUUID([]byte(`    "IOPlatformUUID" = "11111111-2222-3333-4444-555555555555"`))
	if got != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("got %q", got)
	}
}

func TestCollectHardwareIdentityForPlatforms(t *testing.T) {
	uuid := "11111111-2222-3333-4444-555555555555"
	tests := []struct {
		name   string
		goos   string
		file   []byte
		output []byte
		source string
	}{
		{name: "darwin", goos: "darwin", output: []byte(`"IOPlatformUUID" = "` + uuid + `"`), source: "io_platform_uuid"},
		{name: "windows", goos: "windows", output: []byte(uuid + "\r\n"), source: "win32_computer_system_product_uuid"},
		{name: "linux", goos: "linux", file: []byte(uuid + "\n"), source: "dmi_product_uuid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := collectHardwareIdentityFor(test.goos, func(string) ([]byte, error) { return test.file, nil }, func(string, ...string) ([]byte, error) { return test.output, nil })
			if got == nil || got.Source != test.source || got.Value != uuid || got.Version != 1 {
				t.Fatalf("identity = %+v", got)
			}
		})
	}
}

func TestCollectHardwareIdentityFailsSafely(t *testing.T) {
	fail := errors.New("unavailable")
	readFile := func(string) ([]byte, error) { return nil, fail }
	run := func(string, ...string) ([]byte, error) { return nil, fail }
	for _, goos := range []string{"darwin", "windows", "linux", "freebsd"} {
		if got := collectHardwareIdentityFor(goos, readFile, run); got != nil {
			t.Fatalf("%s identity = %+v, want nil", goos, got)
		}
	}
	if got := collectHardwareIdentityFor("linux", func(string) ([]byte, error) { return []byte("invalid"), nil }, run); got != nil {
		t.Fatalf("invalid identity = %+v", got)
	}
}

func TestValidHardwareIdentityReportRejectsPlaceholder(t *testing.T) {
	if validHardwareIdentityReport(device.HardwareIdentityReport{Version: 1, Source: "dmi_product_uuid", Value: "00000000-0000-0000-0000-000000000000"}) {
		t.Fatal("placeholder UUID accepted")
	}
}
