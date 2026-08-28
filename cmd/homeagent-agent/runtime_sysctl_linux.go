//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

var errSysctlUnsupported = errors.New("sysctl is unsupported on this platform")

func managedDiskTarget() (string, string, bool) {
	executable, err := os.Executable()
	if err != nil {
		return "", "", false
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	mountInfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", "", false
	}
	mount, ok := selectLinuxDiskMount(executable, mountInfo)
	if !ok {
		return "", "", false
	}
	return filepath.Dir(executable), mount, true
}

func platformSysctl(string) (string, error) {
	return "", errSysctlUnsupported
}

func platformSysctlUint32(string) (uint32, error) {
	return 0, errSysctlUnsupported
}

func platformBootTime() (time.Time, error) {
	return time.Time{}, errSysctlUnsupported
}
