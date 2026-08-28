package main

import (
	"runtime"
	"time"

	"homeagent/internal/device"
)

func getSystemRuntimeFacts() *device.RuntimeFacts {
	facts := &device.RuntimeFacts{
		ObservedAt:      time.Now().UTC(),
		LogicalCPUCount: runtime.NumCPU(),
	}

	populatePlatformRuntimeFacts(facts)

	return facts
}

