package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".ssh", "authorized_keys")
	if err := atomicWrite(path, []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(path, []byte("two\n")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "two\n" {
		t.Fatalf("got %q", b)
	}
}
func TestApplyKeysRejectsInvalidKey(t *testing.T) {
	if err := applyKeys(strings.NewReader(`{"keys":[{"device_id":"x","public_key":"bad"}]}`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestRunServiceValidation(t *testing.T) {
	if err := runService(nil); err == nil {
		t.Fatal("expected error with no arguments")
	}
	if err := runService([]string{"invalid"}); err == nil {
		t.Fatal("expected error with invalid action")
	}
}

func TestRunDaemonRequiresArgs(t *testing.T) {
	t.Setenv("HOMEAGENT_SERVER", "")
	t.Setenv("HOMEAGENT_JOIN_TOKEN", "")
	if err := runDaemon(nil); err == nil {
		t.Fatal("expected error when server and token missing")
	}
}

