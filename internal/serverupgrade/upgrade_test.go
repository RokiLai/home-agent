package serverupgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"homeagent/internal/githubrelease"
	"homeagent/internal/version"
)

func TestServerUpgrade_AlreadyUpToDate(t *testing.T) {
	opts := Options{
		TargetVersion: version.Get(),
		Force:         false,
	}
	res, err := PerformServerSelfUpgrade(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Updated {
		t.Fatal("expected updated=false, got true")
	}
	if res.TargetVersion != version.Get() {
		t.Fatalf("expected target %s, got %s", version.Get(), res.TargetVersion)
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
