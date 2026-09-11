package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"homeagent/internal/version"
)

func TestSelfUpgrade_AlreadyUpToDate(t *testing.T) {
	opts := UpgradeOptions{
		TargetVersion: version.Get(),
		Force:         false,
		URL:           "http://example.com/binary",
	}
	result, err := PerformSelfUpgrade(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Updated {
		t.Fatalf("expected updated=false, got true")
	}
	if result.TargetVersion != version.Get() {
		t.Fatalf("expected target version %s, got %s", version.Get(), result.TargetVersion)
	}
}

func TestSelfUpgrade_SHAMismatch(t *testing.T) {
	tempDir := t.TempDir()
	originalExe := filepath.Join(tempDir, "agent")
	if err := os.WriteFile(originalExe, []byte("#!/bin/sh\necho original\n"), 0755); err != nil {
		t.Fatal(err)
	}

	content := []byte("#!/bin/sh\necho updated\n")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}))
	defer ts.Close()

	opts := UpgradeOptions{
		TargetVersion:  "v2.0.0",
		URL:            ts.URL,
		SHA256:         "0000000000000000000000000000000000000000000000000000000000000000",
		ExecutablePath: originalExe,
		SkipSmoke:      true,
	}

	_, err := PerformSelfUpgrade(context.Background(), opts)
	if err == nil {
		t.Fatal("expected SHA256 mismatch error, got nil")
	}

	// Verify original file is still untouched
	data, err := os.ReadFile(originalExe)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#!/bin/sh\necho original\n" {
		t.Fatalf("original file was modified: %s", string(data))
	}
}

func TestSelfUpgrade_DownloadBodyStallTimesOutAndCleansUp(t *testing.T) {
	tempDir := t.TempDir()
	originalExe := filepath.Join(tempDir, "agent")
	original := []byte("#!/bin/sh\necho original\n")
	if err := os.WriteFile(originalExe, original, 0755); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial download"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer ts.Close()

	client := &http.Client{Timeout: 0}
	started := time.Now()
	_, err := PerformSelfUpgrade(context.Background(), UpgradeOptions{
		TargetVersion:   "v2.0.0",
		URL:             ts.URL,
		ExecutablePath:  originalExe,
		HTTPClient:      client,
		DownloadTimeout: 50 * time.Millisecond,
		SkipSmoke:       true,
		Force:           true,
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected download deadline exceeded, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled download exceeded bounded timeout: %v", elapsed)
	}

	data, readErr := os.ReadFile(originalExe)
	if readErr != nil || string(data) != string(original) {
		t.Fatalf("original binary changed after timeout: readErr=%v content=%q", readErr, data)
	}
	matches, globErr := filepath.Glob(filepath.Join(tempDir, ".homeagent-agent-upgrade-*.tmp"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("expected timed-out download temp file cleanup, found %v", matches)
	}
}

func TestSelfUpgrade_PreflightSmokeFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping shell script smoke test on Windows")
	}

	tempDir := t.TempDir()
	originalExe := filepath.Join(tempDir, "agent")
	if err := os.WriteFile(originalExe, []byte("#!/bin/sh\necho original\n"), 0755); err != nil {
		t.Fatal(err)
	}

	// Script exits with non-zero on info
	badScript := []byte("#!/bin/sh\nif [ \"$1\" = \"info\" ]; then exit 1; fi\n")
	hasher := sha256.New()
	hasher.Write(badScript)
	expectedSHA := hex.EncodeToString(hasher.Sum(nil))

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(badScript)
	}))
	defer ts.Close()

	opts := UpgradeOptions{
		TargetVersion:  "v2.0.0",
		URL:            ts.URL,
		SHA256:         expectedSHA,
		ExecutablePath: originalExe,
		SkipSmoke:      false,
	}

	_, err := PerformSelfUpgrade(context.Background(), opts)
	if err == nil {
		t.Fatal("expected smoke preflight failure error, got nil")
	}

	// Verify original file still intact
	data, err := os.ReadFile(originalExe)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#!/bin/sh\necho original\n" {
		t.Fatalf("original file was modified after preflight failure: %s", string(data))
	}
}

func TestSelfUpgrade_Success(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping shell script smoke test on Windows")
	}

	tempDir := t.TempDir()
	originalExe := filepath.Join(tempDir, "agent")
	if err := os.WriteFile(originalExe, []byte("#!/bin/sh\necho original\n"), 0755); err != nil {
		t.Fatal(err)
	}

	newScript := []byte("#!/bin/sh\nif [ \"$1\" = \"info\" ]; then echo '{\"id\":\"test\",\"agent_version\":\"v2.0.0\",\"os\":\"" + runtime.GOOS + "\",\"arch\":\"" + runtime.GOARCH + "\"}'; exit 0; fi\n")
	hasher := sha256.New()
	hasher.Write(newScript)
	expectedSHA := hex.EncodeToString(hasher.Sum(nil))

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(newScript)
	}))
	defer ts.Close()

	var restarted atomic.Bool
	opts := UpgradeOptions{
		TargetVersion:  "v2.0.0",
		URL:            ts.URL,
		SHA256:         expectedSHA,
		ExecutablePath: originalExe,
		SkipSmoke:      false,
		RestartCallback: func() error {
			restarted.Store(true)
			return nil
		},
	}

	res, err := PerformSelfUpgrade(context.Background(), opts)
	if err != nil {
		t.Fatalf("upgrade failed: %v", err)
	}
	if !res.Updated {
		t.Fatal("expected updated=true")
	}

	// Verify new content in originalExe path
	data, err := os.ReadFile(originalExe)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(newScript) {
		t.Fatalf("unexpected content: %s", string(data))
	}

	// Verify timing metrics
	if res.Timing.TotalDurationMs < 0 || res.Timing.DownloadDurationMs < 0 || res.Timing.HashDurationMs < 0 {
		t.Fatalf("expected non-negative timing metrics, got %+v", res.Timing)
	}

	// Wait for restart callback
	time.Sleep(500 * time.Millisecond)
	if !restarted.Load() {
		t.Fatal("expected restart callback to be called")
	}
}

func TestSelfUpgrade_PreflightRejectsCandidateVersionMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping shell script smoke test on Windows")
	}

	tempDir := t.TempDir()
	originalExe := filepath.Join(tempDir, "agent")
	original := []byte("#!/bin/sh\necho original\n")
	if err := os.WriteFile(originalExe, original, 0755); err != nil {
		t.Fatal(err)
	}

	candidate := []byte("#!/bin/sh\nif [ \"$1\" = \"info\" ]; then echo '{\"agent_version\":\"v0.5.4\",\"os\":\"" + runtime.GOOS + "\",\"arch\":\"" + runtime.GOARCH + "\"}'; fi\n")
	sum := sha256.Sum256(candidate)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(candidate) }))
	defer ts.Close()

	_, err := PerformSelfUpgrade(context.Background(), UpgradeOptions{
		TargetVersion: "v0.6.1", URL: ts.URL, SHA256: hex.EncodeToString(sum[:]), ExecutablePath: originalExe, Force: true,
	})
	if err == nil || !strings.Contains(err.Error(), "candidate version mismatch") {
		t.Fatalf("expected candidate version mismatch, got %v", err)
	}
	got, readErr := os.ReadFile(originalExe)
	if readErr != nil || string(got) != string(original) {
		t.Fatalf("original binary changed after rejected candidate: readErr=%v content=%q", readErr, got)
	}
}

func TestSelfUpgrade_PreflightRejectsCandidatePlatformMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping shell script smoke test on Windows")
	}

	tempDir := t.TempDir()
	originalExe := filepath.Join(tempDir, "agent")
	if err := os.WriteFile(originalExe, []byte("#!/bin/sh\necho original\n"), 0755); err != nil {
		t.Fatal(err)
	}
	candidate := []byte("#!/bin/sh\nif [ \"$1\" = \"info\" ]; then echo '{\"agent_version\":\"v0.6.1\",\"os\":\"wrong-os\",\"arch\":\"wrong-arch\"}'; fi\n")
	sum := sha256.Sum256(candidate)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(candidate) }))
	defer ts.Close()

	_, err := PerformSelfUpgrade(context.Background(), UpgradeOptions{
		TargetVersion: "v0.6.1", URL: ts.URL, SHA256: hex.EncodeToString(sum[:]), ExecutablePath: originalExe, Force: true,
	})
	if err == nil || !strings.Contains(err.Error(), "candidate platform mismatch") {
		t.Fatalf("expected candidate platform mismatch, got %v", err)
	}
}
