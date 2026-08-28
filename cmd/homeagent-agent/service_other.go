//go:build !windows

package main

// runServiceIfWindows 在非 Windows 平台为空操作，直接返回 false。
func runServiceIfWindows() bool {
	return false
}
