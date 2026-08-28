//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"homeagent/internal/device"
)

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

var (
	globalMemoryStatusExProc = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	getDiskFreeSpaceExWProc  = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")
)

func populatePlatformRuntimeFacts(facts *device.RuntimeFacts) {
	mount := determineWindowsDiskMount()
	facts.DiskMount = mount

	memStatus, memOK := readWindowsMemoryStatus()
	diskTotal, diskAvail, diskOK := readWindowsDiskUsage(mount)

	populateWindowsRuntimeFacts(
		facts,
		windows.DurationSinceBoot(),
		memStatus.TotalPhys,
		memStatus.AvailPhys,
		memOK,
		diskTotal,
		diskAvail,
		diskOK,
	)
}

func determineWindowsDiskMount() string {
	if exe, err := os.Executable(); err == nil {
		if vol := filepath.VolumeName(exe); vol != "" {
			return strings.ToUpper(vol) + `\`
		}
	}
	return `C:\`
}

func readWindowsMemoryStatus() (memoryStatusEx, bool) {
	status := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	r1, _, _ := globalMemoryStatusExProc.Call(uintptr(unsafe.Pointer(&status)))
	return status, r1 != 0
}

func readWindowsDiskUsage(directory string) (uint64, uint64, bool) {
	if directory == "" {
		directory = `C:\`
	}
	dirPtr, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return 0, 0, false
	}
	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	r1, _, _ := getDiskFreeSpaceExWProc.Call(
		uintptr(unsafe.Pointer(dirPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if r1 == 0 {
		return 0, 0, false
	}
	return totalBytes, freeBytesAvailable, true
}

func populateWindowsRuntimeFacts(
	facts *device.RuntimeFacts,
	uptime time.Duration,
	memTotal, memAvail uint64,
	memOK bool,
	diskTotal, diskAvail uint64,
	diskOK bool,
) {
	if seconds := int64(uptime / time.Second); seconds > 0 {
		facts.UptimeSeconds = seconds
	}
	if memOK && memTotal > 0 && memAvail > 0 && memAvail <= memTotal {
		facts.MemoryTotalBytes = memTotal
		facts.MemoryAvailableBytes = memAvail
	}
	if diskOK && diskTotal > 0 && diskAvail <= diskTotal {
		facts.DiskTotalBytes = diskTotal
		facts.DiskAvailableBytes = diskAvail
	}
}
