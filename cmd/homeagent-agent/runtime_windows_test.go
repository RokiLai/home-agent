//go:build windows

package main

import (
	"strings"
	"testing"
	"time"
	"unsafe"

	"homeagent/internal/device"
)

func TestMemoryStatusExABI(t *testing.T) {
	var status memoryStatusEx
	if got := unsafe.Sizeof(status); got != 64 {
		t.Fatalf("memoryStatusEx size = %d, want 64", got)
	}
	if got := unsafe.Offsetof(status.TotalPhys); got != 8 {
		t.Fatalf("TotalPhys offset = %d, want 8", got)
	}
	if got := unsafe.Offsetof(status.AvailPhys); got != 16 {
		t.Fatalf("AvailPhys offset = %d, want 16", got)
	}
}

func TestPopulateWindowsRuntimeFacts(t *testing.T) {
	tests := []struct {
		name          string
		uptime        time.Duration
		memTotal      uint64
		memAvail      uint64
		memOK         bool
		diskTotal     uint64
		diskAvail     uint64
		diskOK        bool
		wantUptime    int64
		wantMemTotal  uint64
		wantMemAvail  uint64
		wantDiskTotal uint64
		wantDiskAvail uint64
	}{
		{
			name:          "valid memory and disk",
			uptime:        60 * 24 * time.Hour,
			memTotal:      32 << 30,
			memAvail:      12 << 30,
			memOK:         true,
			diskTotal:     500 << 30,
			diskAvail:     200 << 30,
			diskOK:        true,
			wantUptime:    5_184_000,
			wantMemTotal:  32 << 30,
			wantMemAvail:  12 << 30,
			wantDiskTotal: 500 << 30,
			wantDiskAvail: 200 << 30,
		},
		{
			name:          "long uptime does not wrap",
			uptime:        800 * 24 * time.Hour,
			memTotal:      16 << 30,
			memAvail:      8 << 30,
			memOK:         true,
			diskTotal:     100 << 30,
			diskAvail:     50 << 30,
			diskOK:        true,
			wantUptime:    69_120_000,
			wantMemTotal:  16 << 30,
			wantMemAvail:  8 << 30,
			wantDiskTotal: 100 << 30,
			wantDiskAvail: 50 << 30,
		},
		{
			name:          "memory failure preserves disk and uptime",
			uptime:        2 * time.Hour,
			memTotal:      32 << 30,
			memAvail:      12 << 30,
			memOK:         false,
			diskTotal:     500 << 30,
			diskAvail:     200 << 30,
			diskOK:        true,
			wantUptime:    7_200,
			wantDiskTotal: 500 << 30,
			wantDiskAvail: 200 << 30,
		},
		{
			name:         "disk failure preserves memory and uptime",
			uptime:       2 * time.Hour,
			memTotal:     32 << 30,
			memAvail:     12 << 30,
			memOK:        true,
			diskTotal:    500 << 30,
			diskAvail:    200 << 30,
			diskOK:       false,
			wantUptime:   7_200,
			wantMemTotal: 32 << 30,
			wantMemAvail: 12 << 30,
		},
		{
			name:         "disk available above total omits disk",
			uptime:       2 * time.Hour,
			memTotal:     32 << 30,
			memAvail:     12 << 30,
			memOK:        true,
			diskTotal:    100 << 30,
			diskAvail:    120 << 30,
			diskOK:       true,
			wantUptime:   7_200,
			wantMemTotal: 32 << 30,
			wantMemAvail: 12 << 30,
		},
		{
			name:         "disk total zero omits disk",
			uptime:       2 * time.Hour,
			memTotal:     32 << 30,
			memAvail:     12 << 30,
			memOK:        true,
			diskTotal:    0,
			diskAvail:    0,
			diskOK:       true,
			wantUptime:   7_200,
			wantMemTotal: 32 << 30,
			wantMemAvail: 12 << 30,
		},
		{
			name:          "subsecond uptime preserves memory and disk",
			uptime:        500 * time.Millisecond,
			memTotal:      8 << 30,
			memAvail:      4 << 30,
			memOK:         true,
			diskTotal:     250 << 30,
			diskAvail:     100 << 30,
			diskOK:        true,
			wantMemTotal:  8 << 30,
			wantMemAvail:  4 << 30,
			wantDiskTotal: 250 << 30,
			wantDiskAvail: 100 << 30,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			facts := &device.RuntimeFacts{}
			populateWindowsRuntimeFacts(facts, tt.uptime, tt.memTotal, tt.memAvail, tt.memOK, tt.diskTotal, tt.diskAvail, tt.diskOK)
			if facts.UptimeSeconds != tt.wantUptime ||
				facts.MemoryTotalBytes != tt.wantMemTotal ||
				facts.MemoryAvailableBytes != tt.wantMemAvail ||
				facts.DiskTotalBytes != tt.wantDiskTotal ||
				facts.DiskAvailableBytes != tt.wantDiskAvail {
				t.Fatalf("facts = %+v, want uptime=%d memTotal=%d memAvail=%d diskTotal=%d diskAvail=%d",
					facts, tt.wantUptime, tt.wantMemTotal, tt.wantMemAvail, tt.wantDiskTotal, tt.wantDiskAvail)
			}
		})
	}
}

func TestDetermineWindowsDiskMount(t *testing.T) {
	mount := determineWindowsDiskMount()
	if mount == "" || !strings.HasSuffix(mount, `\`) {
		t.Fatalf("determineWindowsDiskMount() = %q, want non-empty path ending with \\", mount)
	}
}

func TestGetSystemRuntimeFactsReportsWindowsMemoryUptimeAndDisk(t *testing.T) {
	facts := getSystemRuntimeFacts()
	if facts.UptimeSeconds <= 0 {
		t.Fatalf("UptimeSeconds = %d, want positive", facts.UptimeSeconds)
	}
	if facts.MemoryTotalBytes == 0 || facts.MemoryAvailableBytes == 0 || facts.MemoryAvailableBytes > facts.MemoryTotalBytes {
		t.Fatalf("invalid Windows memory facts: %+v", facts)
	}
	if facts.DiskMount == "" || !strings.HasSuffix(facts.DiskMount, `\`) {
		t.Fatalf("DiskMount = %q, want non-empty drive root ending with \\", facts.DiskMount)
	}
	if facts.DiskTotalBytes == 0 || facts.DiskAvailableBytes > facts.DiskTotalBytes {
		t.Fatalf("invalid Windows disk facts: %+v", facts)
	}
}
