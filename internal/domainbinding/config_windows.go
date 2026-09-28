//go:build windows

package domainbinding

import "os"

func hasSecurePermissions(_ os.FileInfo) bool {
	return true
}

func ownedByCurrentUser(_ os.FileInfo) bool {
	return true
}
