//go:build darwin || linux

package main

import (
	"encoding/binary"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"homeagent/internal/device"
)

func populatePlatformRuntimeFacts(facts *device.RuntimeFacts) {
	// 只采集受管运行目录所在的可写文件系统，避免把只读固件分区误判为磁盘已满。
	if diskPath, diskMount, ok := managedDiskTarget(); ok {
		var stat syscall.Statfs_t
		if err := syscall.Statfs(diskPath, &stat); err == nil {
			facts.DiskMount = diskMount
			facts.DiskTotalBytes = uint64(stat.Blocks) * uint64(stat.Bsize)
			facts.DiskAvailableBytes = uint64(stat.Bavail) * uint64(stat.Bsize)
		}
	}

	if runtime.GOOS == "linux" {
		populateLinuxFacts(facts)
	} else if runtime.GOOS == "darwin" {
		populateDarwinFacts(facts)
	}
}

func populateLinuxFacts(facts *device.RuntimeFacts) {
	// 1. Uptime
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if seconds, ok := parseLinuxUptime(b); ok {
			facts.UptimeSeconds = seconds
		}
	}

	// 2. Load
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) > 0 {
			if l1, err := strconv.ParseFloat(fields[0], 64); err == nil {
				facts.Load1 = &l1
			}
		}
	}

	// 3. Memory
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		lines := strings.Split(string(b), "\n")
		for _, line := range lines {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				if parts[0] == "MemTotal:" {
					if kb, err := strconv.ParseUint(parts[1], 10, 64); err == nil {
						facts.MemoryTotalBytes = kb * 1024
					}
				} else if parts[0] == "MemAvailable:" {
					if kb, err := strconv.ParseUint(parts[1], 10, 64); err == nil {
						facts.MemoryAvailableBytes = kb * 1024
					}
				}
			}
		}
	}
}

func populateDarwinFacts(facts *device.RuntimeFacts) {
	// 1. Memory: hw.memsize via Sysctl / SysctlUint32 or sysctl CLI fallback
	if out, err := platformSysctl("hw.memsize"); err == nil && len(out) > 0 {
		b := []byte(out)
		if len(b) >= 8 {
			total := binary.LittleEndian.Uint64(b[:8])
			if total > 0 {
				facts.MemoryTotalBytes = total
				facts.MemoryAvailableBytes = total / 2
			}
		}
	}
	if facts.MemoryTotalBytes == 0 {
		if phys, err := platformSysctlUint32("hw.physmem"); err == nil && phys > 0 {
			facts.MemoryTotalBytes = uint64(phys)
			facts.MemoryAvailableBytes = uint64(phys) / 2
		} else if out, err := exec.Command("sysctl", "-n", "hw.memsize").Output(); err == nil {
			if num, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64); err == nil && num > 0 {
				facts.MemoryTotalBytes = num
				facts.MemoryAvailableBytes = num / 2
			}
		}
	}

	populateDarwinUptime(facts, time.Now(), platformBootTime)

	// 2. Load: vm.loadavg
	if out, err := platformSysctl("vm.loadavg"); err == nil {
		clean := strings.Trim(strings.TrimSpace(out), "{ }")
		fields := strings.Fields(clean)
		if len(fields) > 0 {
			if l1, err := strconv.ParseFloat(fields[0], 64); err == nil {
				facts.Load1 = &l1
			}
		}
	} else if out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output(); err == nil {
		clean := strings.Trim(strings.TrimSpace(string(out)), "{ }")
		fields := strings.Fields(clean)
		if len(fields) > 0 {
			if l1, err := strconv.ParseFloat(fields[0], 64); err == nil {
				facts.Load1 = &l1
			}
		}
	}
}

func parseLinuxUptime(raw []byte) (int64, bool) {
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, false
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || seconds < 1 {
		return 0, false
	}
	return int64(seconds), true
}

func populateDarwinUptime(facts *device.RuntimeFacts, now time.Time, bootTime func() (time.Time, error)) {
	bootedAt, err := bootTime()
	if err != nil || !now.After(bootedAt) {
		return
	}
	if seconds := int64(now.Sub(bootedAt) / time.Second); seconds > 0 {
		facts.UptimeSeconds = seconds
	}
}
