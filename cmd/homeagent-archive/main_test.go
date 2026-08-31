package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveTool_PackAndVerify(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Create a dummy HomeAgent.app structure
	appDir := filepath.Join(tempDir, "HomeAgent.app")
	if err := os.MkdirAll(filepath.Join(appDir, "Contents", "MacOS"), 0755); err != nil {
		t.Fatal(err)
	}
	execPath := filepath.Join(appDir, "Contents", "MacOS", "homeagent-agent")
	if err := os.WriteFile(execPath, []byte("#!/bin/sh\necho ok\n"), 0755); err != nil {
		t.Fatal(err)
	}
	infoPlist := filepath.Join(appDir, "Contents", "Info.plist")
	if err := os.WriteFile(infoPlist, []byte("<plist version=\"1.0\"><dict></dict></plist>"), 0644); err != nil {
		t.Fatal(err)
	}

	// 2. Generate Ed25519 key pair for signing
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(tempDir, "release_key.hex")
	if err := os.WriteFile(keyFile, []byte(hex.EncodeToString(priv.Seed())), 0600); err != nil {
		t.Fatal(err)
	}

	outZip := filepath.Join(tempDir, "homeagent-app-v0.7.0.zip")
	manifestOut := filepath.Join(tempDir, "manifest.json")

	// 3. Run pack
	packArgs := []string{
		"-app", appDir,
		"-out", outZip,
		"-version", "v0.7.0",
		"-seq", "1",
		"-manifest-out", manifestOut,
		"-keys", keyFile,
		"-key-ids", "key-rel-1",
		"-team-id", "TEAM12345",
		"-bundle-id", "online.rokilai.homeagent",
	}
	if err := runPack(packArgs); err != nil {
		t.Fatalf("runPack failed: %v", err)
	}

	// 4. Run verify
	trustedKeyArg := "key-rel-1:" + hex.EncodeToString(pub)
	verifyArgs := []string{
		"-zip", outZip,
		"-manifest", manifestOut,
		"-trusted-keys", trustedKeyArg,
	}
	if err := runVerify(verifyArgs); err != nil {
		t.Fatalf("runVerify failed: %v", err)
	}
}
