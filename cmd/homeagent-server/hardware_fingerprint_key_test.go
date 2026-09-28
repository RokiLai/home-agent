package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateHardwareFingerprintKeyIsStableAndPrivate(t *testing.T) {
	dir := t.TempDir()
	first, err := loadOrCreateHardwareFingerprintKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateHardwareFingerprintKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 || !bytes.Equal(first, second) {
		t.Fatal("key was not stable")
	}
	info, err := os.Stat(filepath.Join(dir, "hardware-fingerprint.key"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v, err=%v", info.Mode().Perm(), err)
	}
}
