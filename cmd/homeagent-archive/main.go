// Package main implements the homeagent-archive CLI tool for building,
// signing, and verifying macos-app-archive-v2 bundles and manifests.
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"homeagent/internal/daemon/upgrade"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "pack":
		if err := runPack(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "Error packing archive: %v\n", err)
			os.Exit(1)
		}
	case "verify":
		if err := runVerify(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "Error verifying archive: %v\n", err)
			os.Exit(1)
		}
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: homeagent-archive <pack|verify> [options]")
	fmt.Println("Commands:")
	fmt.Println("  pack    Package a HomeAgent.app directory into macos-app-archive-v2 and generate a signed manifest")
	fmt.Println("  verify  Verify a macos-app-archive-v2 ZIP file against its signed manifest")
}

func runPack(args []string) error {
	fs := flag.NewFlagSet("pack", flag.ContinueOnError)
	appDir := fs.String("app", "", "Path to HomeAgent.app directory")
	outZip := fs.String("out", "", "Output path for archive ZIP file")
	targetVer := fs.String("version", "", "Target release version (e.g. v0.7.0)")
	minVer := fs.String("min-version", "v0.6.11", "Minimum supported upgrade version")
	seq := fs.Uint64("seq", 1, "Release sequence number")
	manifestOut := fs.String("manifest-out", "", "Output path for manifest JSON")
	keyFiles := fs.String("keys", "", "Comma-separated paths to Ed25519 private key seed files (hex or raw 32/64 bytes)")
	keyIDs := fs.String("key-ids", "", "Comma-separated key IDs corresponding to private keys")
	teamID := fs.String("team-id", "", "Apple Developer Team ID")
	bundleID := fs.String("bundle-id", "online.rokilai.homeagent", "macOS App Bundle ID")
	dr := fs.String("designated-requirement", "", "Apple Designated Requirement string")
	downloadURL := fs.String("url", "", "Download URL for the archive ZIP")
	recoveryURL := fs.String("recovery-url", "", "Download URL for the recovery binary")
	recoverySHA := fs.String("recovery-sha256", "", "SHA256 hash for the recovery binary")
	recoverySize := fs.Uint64("recovery-size", 0, "Size in bytes of recovery binary")
	validityHours := fs.Int("validity-hours", 720, "Manifest validity duration in hours (default 30 days)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *appDir == "" || *outZip == "" || *targetVer == "" {
		return fmt.Errorf("required flags: -app, -out, -version")
	}

	// 1. Pack App Archive
	bundleDigest, err := upgrade.PackAppArchive(*appDir, *outZip)
	if err != nil {
		return fmt.Errorf("pack app archive: %w", err)
	}

	// 2. Compute Archive SHA256 and Size
	zipFile, err := os.Open(*outZip)
	if err != nil {
		return fmt.Errorf("open output zip: %w", err)
	}
	defer zipFile.Close()

	hasher := sha256.New()
	sizeBytes, err := io.Copy(hasher, zipFile)
	if err != nil {
		return fmt.Errorf("hash zip file: %w", err)
	}
	archiveSHA256 := hex.EncodeToString(hasher.Sum(nil))

	now := time.Now().UTC()
	issuedAt := now.Unix()
	expiresAt := now.Add(time.Duration(*validityHours) * time.Hour).Unix()

	recSHA := *recoverySHA
	if len(recSHA) != 64 {
		recSHA = strings.Repeat("0", 64)
	}

	m := upgrade.Manifest{
		Protocol:             2,
		TargetVersion:        *targetVer,
		MinimumSourceVersion: *minVer,
		ReleaseSequence:      *seq,
		IssuedAt:             issuedAt,
		ExpiresAt:            expiresAt,
		Artifact: upgrade.ArtifactSpec{
			Format:              "macos-app-archive-v2",
			URL:                 *downloadURL,
			SHA256:              archiveSHA256,
			SizeBytes:           uint64(sizeBytes),
			RunningBundleDigest: bundleDigest,
		},
		Recovery: upgrade.RecoverySpec{
			Format:                "macos-recovery-binary-v1",
			URL:                   *recoveryURL,
			SHA256:                recSHA,
			SizeBytes:             *recoverySize,
			DesignatedRequirement: *dr,
		},
		Identity: upgrade.IdentitySpec{
			Component:             "agent",
			OS:                    "darwin",
			Arch:                  "arm64",
			BundleID:              *bundleID,
			TeamID:                *teamID,
			DesignatedRequirement: *dr,
		},
		Force:      false,
		Signatures: []upgrade.Signature{},
	}

	// 3. Sign Manifest if keys provided
	if *keyFiles != "" {
		paths := strings.Split(*keyFiles, ",")
		ids := strings.Split(*keyIDs, ",")
		if len(ids) != len(paths) {
			return fmt.Errorf("number of key-ids (%d) must match number of keys (%d)", len(ids), len(paths))
		}

		canonicalBytes := m.EncodeLengthPrefixed()

		for i, p := range paths {
			keyData, err := os.ReadFile(strings.TrimSpace(p))
			if err != nil {
				return fmt.Errorf("read key file %s: %w", p, err)
			}
			keyBytes := parsePrivateKeyBytes(keyData)
			if len(keyBytes) != ed25519.SeedSize && len(keyBytes) != ed25519.PrivateKeySize {
				return fmt.Errorf("invalid private key size (%d bytes) in %s", len(keyBytes), p)
			}
			var privKey ed25519.PrivateKey
			if len(keyBytes) == ed25519.SeedSize {
				privKey = ed25519.NewKeyFromSeed(keyBytes)
			} else {
				privKey = ed25519.PrivateKey(keyBytes)
			}
			sig := ed25519.Sign(privKey, canonicalBytes)
			m.Signatures = append(m.Signatures, upgrade.Signature{
				KeyID:     strings.TrimSpace(ids[i]),
				Signature: base64.StdEncoding.EncodeToString(sig),
			})
		}
	}

	manifestBytes, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}

	if *manifestOut != "" {
		if err := os.WriteFile(*manifestOut, manifestBytes, 0644); err != nil {
			return fmt.Errorf("write manifest file: %w", err)
		}
	}

	manifestDigest := m.ComputeDigest()
	fmt.Printf("Archive packed successfully:\n")
	fmt.Printf("  Archive:         %s (%d bytes)\n", *outZip, sizeBytes)
	fmt.Printf("  Archive SHA256:  %s\n", archiveSHA256)
	fmt.Printf("  Bundle Digest:   %s\n", bundleDigest)
	fmt.Printf("  Manifest Digest: %s\n", manifestDigest)
	fmt.Printf("  Signatures:      %d\n", len(m.Signatures))
	return nil
}

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	zipPath := fs.String("zip", "", "Path to archive ZIP file")
	manifestPath := fs.String("manifest", "", "Path to manifest JSON file")
	trustedKeys := fs.String("trusted-keys", "", "Comma-separated key_id:public_key_hex pairs")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *zipPath == "" || *manifestPath == "" {
		return fmt.Errorf("required flags: -zip, -manifest")
	}

	manifestData, err := os.ReadFile(*manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	m, err := upgrade.ParseManifestStrict(manifestData)
	if err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}

	if err := m.ValidateTimeWindow(time.Now().UTC()); err != nil {
		return fmt.Errorf("manifest validity window error: %w", err)
	}

	if *trustedKeys != "" {
		keyMap := make(map[string]ed25519.PublicKey)
		pairs := strings.Split(*trustedKeys, ",")
		for _, pair := range pairs {
			parts := strings.SplitN(strings.TrimSpace(pair), ":", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid trusted key format %q, expected key_id:hex", pair)
			}
			pubBytes, err := hex.DecodeString(strings.TrimSpace(parts[1]))
			if err != nil || len(pubBytes) != ed25519.PublicKeySize {
				return fmt.Errorf("invalid public key hex for %s", parts[0])
			}
			keyMap[strings.TrimSpace(parts[0])] = ed25519.PublicKey(pubBytes)
		}
		keySet := upgrade.KeySet{
			SetID:     "trusted",
			Threshold: 1,
			Keys:      keyMap,
		}
		if err := m.VerifySignatures([]upgrade.KeySet{keySet}); err != nil {
			return fmt.Errorf("manifest signature verification failed: %w", err)
		}
	}

	// Verify Archive SHA256
	f, err := os.Open(*zipPath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer f.Close()

	hasher := sha256.New()
	size, err := io.Copy(hasher, f)
	if err != nil {
		return fmt.Errorf("hash zip: %w", err)
	}
	actualSHA := hex.EncodeToString(hasher.Sum(nil))
	if actualSHA != m.Artifact.SHA256 {
		return fmt.Errorf("archive SHA256 mismatch: expected %s, got %s", m.Artifact.SHA256, actualSHA)
	}
	if uint64(size) != m.Artifact.SizeBytes {
		return fmt.Errorf("archive size mismatch: expected %d, got %d", m.Artifact.SizeBytes, size)
	}

	fmt.Printf("Manifest and archive verified successfully!\n")
	return nil
}

func parsePrivateKeyBytes(data []byte) []byte {
	str := strings.TrimSpace(string(data))
	if b, err := hex.DecodeString(str); err == nil && (len(b) == 32 || len(b) == 64) {
		return b
	}
	return data
}
