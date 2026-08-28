package main

import (
	"path/filepath"
	"strings"
)

func selectLinuxDiskMount(managedPath string, mountInfo []byte) (string, bool) {
	managedPath = filepath.Clean(managedPath)
	best := ""
	for _, line := range strings.Split(string(mountInfo), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || !hasMountOption(fields[5], "rw") {
			continue
		}
		mount := decodeMountInfoPath(fields[4])
		if !pathWithinMount(managedPath, mount) || len(mount) <= len(best) {
			continue
		}
		best = mount
	}
	return best, best != ""
}

func hasMountOption(options, target string) bool {
	for _, option := range strings.Split(options, ",") {
		if option == target {
			return true
		}
	}
	return false
}

func pathWithinMount(path, mount string) bool {
	if mount == "/" {
		return filepath.IsAbs(path)
	}
	return path == mount || strings.HasPrefix(path, mount+string(filepath.Separator))
}

func decodeMountInfoPath(value string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return filepath.Clean(replacer.Replace(value))
}
