package githubsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHKey_GenerateAndFingerprint(t *testing.T) {
	tempDir := t.TempDir()
	privPath := filepath.Join(tempDir, "test_ed25519")

	// 1. Generate key pair
	pubStr, fp, created, err := EnsureEd25519KeyPair(privPath, "homeagent-test")
	if err != nil {
		t.Fatalf("EnsureEd25519KeyPair failed: %v", err)
	}
	if !created {
		t.Fatalf("expected created to be true")
	}
	if !strings.HasPrefix(pubStr, "ssh-ed25519 ") {
		t.Fatalf("expected ssh-ed25519 prefix, got: %s", pubStr)
	}
	if !strings.HasPrefix(fp, "SHA256:") {
		t.Fatalf("expected SHA256: prefix, got: %s", fp)
	}

	// Verify file permissions (0600 on unix)
	info, err := os.Stat(privPath)
	if err != nil {
		t.Fatalf("stat privPath: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected 0600 permissions, got %v", info.Mode().Perm())
	}

	// 2. Ensure again (idempotent, created = false)
	pubStr2, fp2, created2, err := EnsureEd25519KeyPair(privPath, "homeagent-test")
	if err != nil {
		t.Fatalf("EnsureEd25519KeyPair 2nd call failed: %v", err)
	}
	if created2 {
		t.Fatalf("expected created2 to be false")
	}
	if pubStr2 != pubStr || fp2 != fp {
		t.Fatalf("key mismatch between calls: %s != %s", pubStr, pubStr2)
	}

	// 3. Test Remove
	if err := RemoveEd25519KeyPair(privPath); err != nil {
		t.Fatalf("RemoveEd25519KeyPair failed: %v", err)
	}
	if _, err := os.Stat(privPath); !os.IsNotExist(err) {
		t.Fatalf("expected private key to be removed")
	}
	if _, err := os.Stat(privPath + ".pub"); !os.IsNotExist(err) {
		t.Fatalf("expected public key to be removed")
	}
}

func TestComputeFingerprint_KnownVector(t *testing.T) {
	// Standard ed25519 key line
	pub := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGf/FAKEPUBKEYTESTDATA1234567890abcdefghijkl test@homeagent"
	fp, err := ComputeFingerprint(pub)
	if err != nil {
		t.Fatalf("ComputeFingerprint failed: %v", err)
	}
	if !strings.HasPrefix(fp, "SHA256:") {
		t.Fatalf("invalid fingerprint format: %s", fp)
	}

	// Invalid key
	_, err = ComputeFingerprint("invalid-key-data")
	if err == nil {
		t.Fatalf("expected error for invalid key")
	}
}
