//go:build windows

package domainbinding

import (
	"os"
	"testing"
	"time"
)

type dummyFileInfo struct{}

func (dummyFileInfo) Name() string       { return "dummy" }
func (dummyFileInfo) Size() int64        { return 0 }
func (dummyFileInfo) Mode() os.FileMode  { return 0666 }
func (dummyFileInfo) ModTime() time.Time { return time.Time{} }
func (dummyFileInfo) IsDir() bool        { return false }
func (dummyFileInfo) Sys() any           { return nil }

func TestWindowsPermissionsCompatibility(t *testing.T) {
	info := dummyFileInfo{}
	if !hasSecurePermissions(info) {
		t.Fatalf("expected hasSecurePermissions to return true on windows")
	}
	if !ownedByCurrentUser(info) {
		t.Fatalf("expected ownedByCurrentUser to return true on windows")
	}
}
