package sshsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyManagedFilePreservesModeAndCreatesBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	original := []byte("ssh-ed25519 QUFBQQ== personal\n")
	if err := os.WriteFile(path, original, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	changed, err := ApplyManagedFile(path, []Key{{DeviceID: "dev-1", PublicKey: "ssh-ed25519 QkJCQg=="}})
	if err != nil || !changed {
		t.Fatalf("apply: changed=%v err=%v", changed, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode changed: %o", info.Mode().Perm())
	}
	backup, err := os.ReadFile(path + ".homeagent.bak")
	if err != nil || string(backup) != string(original) {
		t.Fatalf("backup: %q %v", backup, err)
	}
	updated, _ := os.ReadFile(path)
	if !strings.Contains(string(updated), "QkJCQg== dev-1") {
		t.Fatalf("updated key missing: %s", updated)
	}
}

func TestApplyManagedFileRejectsSymlinkWithoutSideEffect(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(target, []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyManagedFile(link, []Key{{DeviceID: "dev", PublicKey: "ssh-ed25519 QkJCQg=="}}); err == nil {
		t.Fatal("expected symlink rejection")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "original\n" {
		t.Fatalf("symlink target changed: %q", got)
	}
}
