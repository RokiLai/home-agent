//go:build darwin

package main

import (
	"context"
	"net"
	"os/exec"
	"time"

	"homeagent/internal/device"
	"homeagent/internal/networkaddr"
)

func stableIPv6Addresses() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var values []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || isVirtualInterface(iface.Name) {
			continue
		}
		output, err := exec.CommandContext(ctx, "ifconfig", iface.Name).Output()
		if err != nil {
			continue
		}
		addresses := networkaddr.NormalizeAndFilterCandidates(networkaddr.ParseDarwinIfconfig(string(output), iface.Name), time.Now().UTC())
		for _, address := range addresses {
			values = append(values, address.Address)
		}
	}
	return device.FilterAndSortAddresses(values)
}
