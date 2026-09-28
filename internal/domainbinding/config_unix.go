//go:build !windows

package domainbinding

import (
	"os"
	"syscall"
)

func hasSecurePermissions(info os.FileInfo) bool {
	return info.Mode().Perm() == 0600 && ownedByCurrentUser(info)
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}
