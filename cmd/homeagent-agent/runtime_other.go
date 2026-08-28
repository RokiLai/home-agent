//go:build !darwin && !linux && !windows

package main

import (
	"homeagent/internal/device"
)

func populatePlatformRuntimeFacts(facts *device.RuntimeFacts) {
}

