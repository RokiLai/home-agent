//go:build darwin

package serverupgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
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


func TestDarwinServerUpgrade_CodesignIdentityContinuitySuccess(t *testing.T) {
	fp := "1835C8FCC239CCAC08B6CBEB7D94F07D07327FB7"
	signer := newDarwinSigner(SignerConfig{
		Enabled:     true,
		Fingerprint: fp,
		Identifier:  DefaultSignerIdentifier,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := signer.Preflight(ctx); err != nil {
		t.Skipf("skipping live darwin signer continuity test: %v", err)
	}

	tempDir := t.TempDir()
	origExe := buildDarwinDummyBinary(t, tempDir, "homeagent-server-old")
	// Sign the original binary with the fixed identity to simulate an already signed install
	if err := signer.Sign(ctx, origExe); err != nil {
		t.Fatalf("sign original binary failed: %v", err)
	}
	oldReq, isAdhoc, err := signer.ReadDesignatedRequirement(ctx, origExe)
	if err != nil || isAdhoc || oldReq == "" {
		t.Fatalf("read old requirement failed: req=%s, isAdhoc=%v, err=%v", oldReq, isAdhoc, err)
	}

	// Build candidate binary (different content)
	candidateSrc := filepath.Join(tempDir, "candidate.go")
	candidateBin := filepath.Join(tempDir, "candidate-bin")
	src := `package main
import (
	"fmt"
	"os"
)
func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("homeagent-server v0.7.0")
		return
	}
	fmt.Println("candidate binary")
}
`
	if err := os.WriteFile(candidateSrc, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", candidateBin, candidateSrc)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build candidate failed: %v, out: %s", err, string(out))
	}
	candidateBytes, err := os.ReadFile(candidateBin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(candidateBytes)
	candidateSHA := hex.EncodeToString(sum[:])

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(candidateBytes)
	}))
	defer ts.Close()

	t.Setenv(EnvServerCodesignIdentity, fp)
	var restarted atomic.Bool
	opts := Options{
		TargetVersion:  "v0.7.0",
		ExecutablePath: origExe,
		URL:            ts.URL,
		SHA256:         candidateSHA,
		RestartCallback: func() error {
			restarted.Store(true)
			return nil
		},
	}

	res, err := PerformServerSelfUpgrade(context.Background(), opts)
	if err != nil {
		t.Fatalf("upgrade failed: %v", err)
	}
	if !res.Updated {
		t.Fatal("expected updated=true")
	}

	// Verify new installed binary satisfies the old requirement!
	if err := signer.VerifyRequirement(ctx, origExe, oldReq); err != nil {
		t.Fatalf("newly installed binary failed identity continuity check against old requirement: %v", err)
	}

	// Verify backup preserves old signature
	bakPath := origExe + ".bak"
	if err := signer.VerifyRequirement(ctx, bakPath, oldReq); err != nil {
		t.Fatalf("backup failed requirement check: %v", err)
	}
}
