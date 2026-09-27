package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestFinalizeControlPlaneMigrationCreatesVerifiedPrivateBackups(t *testing.T) {
	dir := t.TempDir()
	devices := []byte(`{"devices":[]}`)
	enrollment := []byte(`{"tokens":{}}`)
	if err := os.WriteFile(filepath.Join(dir, "devices.json"), devices, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "enrollment.json"), enrollment, 0600); err != nil {
		t.Fatal(err)
	}
	if err := finalizeControlPlaneMigration(dir); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{"devices.json": devices, "enrollment.json": enrollment} {
		path := filepath.Join(dir, name+".control-plane.bak")
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("backup %s=%q err=%v", name, got, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("backup mode=%v err=%v", info.Mode().Perm(), err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "control-plane.migrated")); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizeControlPlaneMigrationRejectsMismatchedExistingBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devices.json")
	if err := os.WriteFile(path, []byte("current"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".control-plane.bak", []byte("different"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := finalizeControlPlaneMigration(dir); err == nil {
		t.Fatal("mismatched backup accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "control-plane.migrated")); !os.IsNotExist(err) {
		t.Fatalf("completion marker created after failure: %v", err)
	}
}
