//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func managedDiskTarget() (string, string, bool) {
	executable, err := os.Executable()
	if err != nil {
		return "", "", false
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return filepath.Dir(executable), "/", true
}

func platformSysctl(name string) (string, error) {
	return unix.Sysctl(name)
}

func platformSysctlUint32(name string) (uint32, error) {
	return unix.SysctlUint32(name)
}

func platformBootTime() (time.Time, error) {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(tv.Sec, int64(tv.Usec)*1_000), nil
}
