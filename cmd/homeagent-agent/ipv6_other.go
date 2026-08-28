//go:build !darwin && !linux && !windows

package main

func stableIPv6Addresses() []string { return nil }
