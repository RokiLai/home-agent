package serverupgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"homeagent/internal/githubrelease"
	"homeagent/internal/version"
)

func TestServerUpgrade_AlreadyUpToDate(t *testing.T) {
	opts := Options{
		TargetVersion: version.GetServer(),
		Force:         false,
	}
	res, err := PerformServerSelfUpgrade(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Updated {
		t.Fatal("expected updated=false, got true")
	}
	if res.TargetVersion != version.GetServer() {
		t.Fatalf("expected target %s, got %s", version.GetServer(), res.TargetVersion)
	}
}

func TestServerUpgradeResolvesOnlyServerChannel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell candidate is Unix-only")
	}
	tempDir := t.TempDir()
	origExe := filepath.Join(tempDir, "homeagent-server")
	if err := os.WriteFile(origExe, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	candidate := []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo 'homeagent-server v9.0.0'; fi\n")
	sum := sha256.Sum256(candidate)
	digest := hex.EncodeToString(sum[:])
	serverAsset := fmt.Sprintf("homeagent-server-%s-%s", runtime.GOOS, runtime.GOARCH)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/RokiLai/home-agent/releases":
			_, _ = fmt.Fprintf(w, `[{"id":1,"tag_name":"agent-v99.0.0","draft":false,"prerelease":false},{"id":2,"tag_name":"server-v9.0.0","draft":false,"prerelease":false}]`)
		case "/RokiLai/home-agent/releases/download/server-v9.0.0/" + serverAsset:
			_, _ = w.Write(candidate)
		case "/RokiLai/home-agent/releases/download/server-v9.0.0/" + serverAsset + ".sha256":
			_, _ = fmt.Fprintf(w, "%s  %s\n", digest, serverAsset)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	client := githubrelease.NewClient(githubrelease.Config{Repo: "RokiLai/home-agent", APIBase: ts.URL, DownloadBaseURL: ts.URL})
	result, err := PerformServerSelfUpgrade(context.Background(), Options{ExecutablePath: origExe, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if result.TargetVersion != "v9.0.0" {
		t.Fatalf("target = %s, want server channel v9.0.0", result.TargetVersion)
	}
}

func TestServerUpgrade_SHAMismatch(t *testing.T) {
	tempDir := t.TempDir()
	origExe := filepath.Join(tempDir, "homeagent-server")
	if err := os.WriteFile(origExe, []byte("#!/bin/sh\necho 'homeagent-server v0.6.11'\n"), 0755); err != nil {
		t.Fatal(err)
	}

	content := []byte("#!/bin/sh\necho 'homeagent-server v0.7.0'\n")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}))
	defer ts.Close()

	ghClient := githubrelease.NewClient(githubrelease.Config{
		DownloadBaseURL: ts.URL,
	})

	opts := Options{
		TargetVersion:  "v0.7.0",
		ExecutablePath: origExe,
		URL:            ts.URL + "/binary",
		SHA256:         "0000000000000000000000000000000000000000000000000000000000000000",
		Client:         ghClient,
		SkipSmoke:      true,
	}

	_, err := PerformServerSelfUpgrade(context.Background(), opts)
	if err == nil {
		t.Fatal("expected SHA mismatch error, got nil")
	}

	// Verify original file untouched
	data, _ := os.ReadFile(origExe)
	if string(data) != "#!/bin/sh\necho 'homeagent-server v0.6.11'\n" {
		t.Fatalf("original file was modified: %s", string(data))
	}
}

func TestServerUpgrade_SmokeFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping shell script smoke test on Windows")
	}

	tempDir := t.TempDir()
	origExe := filepath.Join(tempDir, "homeagent-server")
	if err := os.WriteFile(origExe, []byte("#!/bin/sh\necho 'homeagent-server v0.6.11'\n"), 0755); err != nil {
		t.Fatal(err)
	}

	badScript := []byte("#!/bin/sh\nexit 1\n")
	sum := sha256.Sum256(badScript)
	expectedSHA := hex.EncodeToString(sum[:])

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(badScript)
	}))
	defer ts.Close()

	opts := Options{
		TargetVersion:  "v0.7.0",
		ExecutablePath: origExe,
		URL:            ts.URL + "/binary",
		SHA256:         expectedSHA,
		SkipSmoke:      false,
	}

	_, err := PerformServerSelfUpgrade(context.Background(), opts)
	if err == nil {
		t.Fatal("expected smoke preflight error, got nil")
	}

	// Verify original file untouched
	data, _ := os.ReadFile(origExe)
	if string(data) != "#!/bin/sh\necho 'homeagent-server v0.6.11'\n" {
		t.Fatalf("original file modified after smoke failure: %s", string(data))
	}
}

func TestServerUpgrade_Success(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping shell script smoke test on Windows")
	}

	tempDir := t.TempDir()
	origExe := filepath.Join(tempDir, "homeagent-server")
	if err := os.WriteFile(origExe, []byte("#!/bin/sh\necho 'homeagent-server v0.6.11'\n"), 0755); err != nil {
		t.Fatal(err)
	}

	newScript := []byte("#!/bin/sh\nif [ \"$1\" = \"version\" ]; then echo 'homeagent-server v0.7.0'; exit 0; fi\n")
	sum := sha256.Sum256(newScript)
	expectedSHA := hex.EncodeToString(sum[:])

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/binary":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(newScript)
		case "/sha256":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, expectedSHA)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	var restarted atomic.Bool
	opts := Options{
		TargetVersion:  "v0.7.0",
		ExecutablePath: origExe,
		URL:            ts.URL + "/binary",
		SHA256:         expectedSHA,
		SkipSmoke:      false,
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
	if res.TargetVersion != "v0.7.0" {
		t.Fatalf("expected target v0.7.0, got %s", res.TargetVersion)
	}

	// Verify new content in place
	data, err := os.ReadFile(origExe)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(newScript) {
		t.Fatalf("unexpected content: %s", string(data))
	}

	// Verify backup exists
	bakData, err := os.ReadFile(origExe + ".bak")
	if err != nil || string(bakData) != "#!/bin/sh\necho 'homeagent-server v0.6.11'\n" {
		t.Fatalf("backup file missing or invalid: %v", err)
	}

	time.Sleep(600 * time.Millisecond)
	if !restarted.Load() {
		t.Fatal("expected restart callback called")
	}
}


func TestServerUpgrade_CodesignValidationAndFailurePaths(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("skipping darwin-specific codesign validation tests")
	}

	tempDir := t.TempDir()
	origExe := filepath.Join(tempDir, "homeagent-server")
	if err := os.WriteFile(origExe, []byte("#!/bin/sh\necho 'homeagent-server v0.6.11'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	candidateScript := []byte("#!/bin/sh\nif [ \"$1\" = \"version\" ]; then echo 'homeagent-server v0.7.0'; exit 0; fi\n")
	sum := sha256.Sum256(candidateScript)
	expectedSHA := hex.EncodeToString(sum[:])

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(candidateScript)
	}))
	defer ts.Close()

	t.Run("invalid fingerprint config rejects before download", func(t *testing.T) {
		t.Setenv(EnvServerCodesignIdentity, "InvalidIdentityName")
		opts := Options{
			TargetVersion:  "v0.7.0",
			ExecutablePath: origExe,
			URL:            ts.URL,
			SHA256:         expectedSHA,
		}
		_, err := PerformServerSelfUpgrade(context.Background(), opts)
		if err == nil {
			t.Fatal("expected error for invalid fingerprint config")
		}
	})

	t.Run("non-existent fingerprint rejects in preflight", func(t *testing.T) {
		t.Setenv(EnvServerCodesignIdentity, "0000000000000000000000000000000000000000")
		opts := Options{
			TargetVersion:  "v0.7.0",
			ExecutablePath: origExe,
			URL:            ts.URL,
			SHA256:         expectedSHA,
		}
		_, err := PerformServerSelfUpgrade(context.Background(), opts)
		if err == nil {
			t.Fatal("expected error for nonexistent fingerprint")
		}
	})

	t.Run("signing failure on non-macho preserves original and bak", func(t *testing.T) {
		// Valid fingerprint from test keychain
		t.Setenv(EnvServerCodesignIdentity, "1835C8FCC239CCAC08B6CBEB7D94F07D07327FB7")
		// candidateScript is a shell script, codesign will fail on non-Mach-O
		var restarted atomic.Bool
		opts := Options{
			TargetVersion:  "v0.7.0",
			ExecutablePath: origExe,
			URL:            ts.URL,
			SHA256:         expectedSHA,
			RestartCallback: func() error {
				restarted.Store(true)
				return nil
			},
		}
		_, err := PerformServerSelfUpgrade(context.Background(), opts)
		if err == nil {
			t.Fatal("expected codesign failure on shell script")
		}

		// Verify original file untouched
		data, err := os.ReadFile(origExe)
		if err != nil || string(data) != "#!/bin/sh\necho 'homeagent-server v0.6.11'\n" {
			t.Fatalf("original file modified or corrupted: %v", err)
		}

		// Verify no restart callback invoked
		if restarted.Load() {
			t.Fatal("restart callback should not have been called on signing failure")
		}

		// Verify temp files cleaned up
		entries, _ := os.ReadDir(tempDir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "homeagent-server-upgrade-") {
				t.Fatalf("leftover temp file found: %s", e.Name())
			}
		}
	})
}


func TestServerUpgrade_CodesignIdentityContinuitySuccess(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("skipping darwin-specific codesign tests")
	}

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
