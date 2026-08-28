//go:build linux

package main

import "os/exec"

func stableIPv6Addresses() []string {
	output, err := exec.Command("ip", "-o", "-6", "addr", "show").Output()
	if err != nil {
		return nil
	}
	return parseLinuxStableIPv6(string(output))
}
