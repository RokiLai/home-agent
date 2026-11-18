//go:build darwin

package serverupgrade

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func buildDarwinDummyBinary(t *testing.T, dir, name string) string {
	t.Helper()
	srcPath := filepath.Join(dir, name+".go")
	binPath := filepath.Join(dir, name)
	src := `package main
import "fmt"
func main() { fmt.Println("dummy binary") }
`
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatalf("write dummy src: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binPath, srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build dummy bin failed: %v, out: %s", err, string(out))
	}
	return binPath
}

func TestDarwinSignerPreflightAndSigning(t *testing.T) {
	fp := "1835C8FCC239CCAC08B6CBEB7D94F07D07327FB7"
	signer := newDarwinSigner(SignerConfig{
		Enabled:     true,
		Fingerprint: fp,
		Identifier:  DefaultSignerIdentifier,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := signer.Preflight(ctx); err != nil {
		t.Skipf("skipping live darwin signer test (identity might not be in test keychain): %v", err)
	}

	tmpDir := t.TempDir()
	bin1 := buildDarwinDummyBinary(t, tmpDir, "bin1")
	bin2 := buildDarwinDummyBinary(t, tmpDir, "bin2")

	// Read initial requirement of bin1 (should be adhoc)
	_, isAdhoc, err := signer.ReadDesignatedRequirement(ctx, bin1)
	if err != nil {
		t.Fatalf("read designated requirement of adhoc bin1 failed: %v", err)
	}
	if !isAdhoc {
		t.Fatalf("expected bin1 to be adhoc")
	}

	// Sign bin1
	if err := signer.Sign(ctx, bin1); err != nil {
		t.Fatalf("sign bin1 failed: %v", err)
	}

	// Read bin1 requirement after signing
	reqSigned, isAdhocSigned, err := signer.ReadDesignatedRequirement(ctx, bin1)
	if err != nil {
		t.Fatalf("read requirement after sign failed: %v", err)
	}
	if isAdhocSigned {
		t.Fatalf("expected signed bin1 not to be adhoc")
	}
	if !strings.Contains(reqSigned, DefaultSignerIdentifier) {
		t.Fatalf("expected signed requirement to contain identifier %s, got: %s", DefaultSignerIdentifier, reqSigned)
	}

	// Verify bin1 with its own requirement
	if err := signer.VerifyRequirement(ctx, bin1, reqSigned); err != nil {
		t.Fatalf("verify requirement for signed bin1 failed: %v", err)
	}

	// Sign bin2
	if err := signer.Sign(ctx, bin2); err != nil {
		t.Fatalf("sign bin2 failed: %v", err)
	}

	// Verify identity continuity: bin2 must satisfy bin1's signed requirement
	if err := signer.VerifyRequirement(ctx, bin2, reqSigned); err != nil {
		t.Fatalf("verify identity continuity between bin1 and bin2 failed: %v", err)
	}

	// Verify mismatched requirement fails
	badReq := `identifier "different.identifier" and certificate leaf = H"0000000000000000000000000000000000000000"`
	if err := signer.VerifyRequirement(ctx, bin2, badReq); err == nil {
		t.Fatalf("expected mismatch requirement to fail")
	}
}
