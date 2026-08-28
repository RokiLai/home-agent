//go:build windows

package main

import "os/exec"

func stableIPv6Addresses() []string {
	const query = `Get-NetIPAddress -AddressFamily IPv6 | Select-Object IPAddress,InterfaceAlias,AddressState,SuffixOrigin | ConvertTo-Json -Compress`
	output, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", query).Output()
	if err != nil {
		return nil
	}
	return parseWindowsStableIPv6(string(output))
}
