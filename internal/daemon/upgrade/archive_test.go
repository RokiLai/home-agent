package upgrade

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchivePackAndUnpack(t *testing.T) {
	tempDir := t.TempDir()
	appDir := filepath.Join(tempDir, "HomeAgent.app")
	contentsDir := filepath.Join(appDir, "Contents", "MacOS")
	if err := os.MkdirAll(contentsDir, 0755); err != nil {
		t.Fatal(err)
	}

	binPath := filepath.Join(contentsDir, "homeagent-agent")
	if err := os.WriteFile(binPath, []byte("executable binary content"), 0755); err != nil {
		t.Fatal(err)
	}
	infoPath := filepath.Join(appDir, "Contents", "Info.plist")
	if err := os.WriteFile(infoPath, []byte("<plist version=\"1.0\"></plist>"), 0644); err != nil {
		t.Fatal(err)
	}

	zipPath := filepath.Join(tempDir, "HomeAgent.zip")
	digest, err := PackAppArchive(appDir, zipPath)
	if err != nil {
		t.Fatalf("PackAppArchive failed: %v", err)
	}
	if len(digest) != 64 {
		t.Fatalf("unexpected digest length: %d", len(digest))
	}

	// Unpack to new destination
	destDir := filepath.Join(tempDir, "extracted")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := UnpackAppArchive(zipPath, destDir, digest); err != nil {
		t.Fatalf("UnpackAppArchive failed: %v", err)
	}

	// Verify extracted files exist
	extractedBin := filepath.Join(destDir, "HomeAgent.app", "Contents", "MacOS", "homeagent-agent")
	data, err := os.ReadFile(extractedBin)
	if err != nil {
		t.Fatalf("failed to read extracted bin: %v", err)
	}
	if string(data) != "executable binary content" {
		t.Fatalf("unexpected extracted content: %s", string(data))
	}

	// Negative case: digest mismatch
	if err := UnpackAppArchive(zipPath, destDir, strings.Repeat("0", 64)); err == nil {
		t.Fatal("expected digest mismatch error, got nil")
	}
}

func TestArchiveSecurityRejections(t *testing.T) {
	tempDir := t.TempDir()

	createMaliciousZip := func(name string, fileNames []string) string {
		zipPath := filepath.Join(tempDir, name)
		f, err := os.Create(zipPath)
		if err != nil {
			t.Fatal(err)
		}
		w := zip.NewWriter(f)
		for _, fn := range fileNames {
			fw, err := w.Create(fn)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = fw.Write([]byte("content"))
		}
		_ = w.Close()
		_ = f.Close()
		return zipPath
	}

	destDir := filepath.Join(tempDir, "extracted_sec")
	_ = os.MkdirAll(destDir, 0755)

	// 1. Directory traversal
	zipTraversal := createMaliciousZip("traversal.zip", []string{"HomeAgent.app/../evil.txt"})
	if err := UnpackAppArchive(zipTraversal, destDir, ""); err == nil {
		t.Fatal("expected directory traversal error, got nil")
	}

	// 2. Non HomeAgent.app top-level element
	zipNonApp := createMaliciousZip("nonapp.zip", []string{"OtherApp.app/Contents/file.txt"})
	if err := UnpackAppArchive(zipNonApp, destDir, ""); err == nil {
		t.Fatal("expected invalid top-level layout error, got nil")
	}

	// 3. AppleDouble metadata entry
	zipAppleDouble := createMaliciousZip("appledouble.zip", []string{"HomeAgent.app/Contents/._Info.plist"})
	if err := UnpackAppArchive(zipAppleDouble, destDir, ""); err == nil {
		t.Fatal("expected forbidden metadata error, got nil")
	}
}
