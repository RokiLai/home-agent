package main

import (
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"homeagent/internal/device"
)

func collectHardwareIdentity() *device.HardwareIdentityReport {
	return collectHardwareIdentityFor(runtime.GOOS, os.ReadFile, func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).Output()
	})
}

func collectHardwareIdentityFor(goos string, readFile func(string) ([]byte, error), runCommand func(string, ...string) ([]byte, error)) *device.HardwareIdentityReport {
	var source string
	var value string
	switch goos {
	case "darwin":
		source = "io_platform_uuid"
		out, err := runCommand("ioreg", "-rd1", "-c", "IOPlatformExpertDevice")
		if err != nil {
			return nil
		}
		value = parseIOPlatformUUID(out)
	case "windows":
		source = "win32_computer_system_product_uuid"
		out, err := runCommand("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "(Get-CimInstance Win32_ComputerSystemProduct).UUID")
		if err != nil {
			return nil
		}
		value = strings.TrimSpace(string(out))
	case "linux":
		source = "dmi_product_uuid"
		out, err := readFile("/sys/class/dmi/id/product_uuid")
		if err != nil {
			return nil
		}
		value = strings.TrimSpace(string(out))
	default:
		return nil
	}
	report := device.HardwareIdentityReport{Version: 1, Source: source, Value: value}
	if !validHardwareIdentityReport(report) {
		return nil
	}
	return &report
}

func parseIOPlatformUUID(out []byte) string {
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		if !bytes.Contains(line, []byte("IOPlatformUUID")) {
			continue
		}
		parts := strings.Split(string(line), "\"")
		if len(parts) >= 4 {
			return strings.TrimSpace(parts[3])
		}
	}
	return ""
}

func validHardwareIdentityReport(report device.HardwareIdentityReport) bool {
	_, err := device.FingerprintHardwareIdentity(bytes.Repeat([]byte{1}, 32), report)
	return err == nil
}
