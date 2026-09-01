package upgrade

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestManifestEncodingAndThresholdVerification(t *testing.T) {
	pub1, priv1, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub2, priv2, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub3, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	keySet := KeySet{
		SetID:     "primary-2026",
		Threshold: 2,
		Keys: map[string]ed25519.PublicKey{
			"key-1": pub1,
			"key-2": pub2,
			"key-3": pub3,
		},
	}

	now := time.Now().Unix()
	m := Manifest{
		Protocol:             2,
		TargetVersion:        "v0.7.0",
		MinimumSourceVersion: "v0.6.0",
		ReleaseSequence:      42,
		IssuedAt:             now - 60,
		ExpiresAt:            now + 3600,
		Artifact: ArtifactSpec{
			Format:              "macos-app-archive-v2",
			URL:                 "https://example.com/HomeAgent.zip",
			SHA256:              strings.Repeat("a", 64),
			SizeBytes:           10240,
			RunningBundleDigest: strings.Repeat("b", 64),
		},
		Recovery: RecoverySpec{
			Format:                "macos-recovery-binary-v1",
			URL:                   "https://example.com/recovery",
			SHA256:                strings.Repeat("c", 64),
			SizeBytes:             2048,
			DesignatedRequirement: "designated => identifier homeagent-recovery",
		},
		Identity: IdentitySpec{
			Component:             "homeagent-agent",
			OS:                    "darwin",
			Arch:                  "arm64",
			BundleID:              "com.homeagent.agent",
			TeamID:                "ABC123XYZ",
			DesignatedRequirement: "designated => identifier com.homeagent.agent",
		},
		Force: false,
	}

	encoded := m.EncodeLengthPrefixed()
	sig1 := ed25519.Sign(priv1, encoded)
	sig2 := ed25519.Sign(priv2, encoded)

	m.Signatures = []Signature{
		{KeyID: "key-1", Signature: base64.StdEncoding.EncodeToString(sig1)},
		{KeyID: "key-2", Signature: base64.StdEncoding.EncodeToString(sig2)},
	}

	// 1. Valid 2-of-3 verification
	if err := m.VerifySignatures([]KeySet{keySet}); err != nil {
		t.Fatalf("unexpected signature verification failure: %v", err)
	}

	// Digest calculation check
	digest := m.ComputeDigest()
	if len(digest) != 64 {
		t.Fatalf("unexpected digest length: %d", len(digest))
	}
	if _, err := hex.DecodeString(digest); err != nil {
		t.Fatalf("digest is not valid hex: %v", err)
	}

	// Time window check
	if err := m.ValidateTimeWindow(time.Now()); err != nil {
		t.Fatalf("unexpected time window failure: %v", err)
	}

	// 2. Threshold not met (only 1 signature)
	mSingle := m
	mSingle.Signatures = []Signature{
		{KeyID: "key-1", Signature: base64.StdEncoding.EncodeToString(sig1)},
	}
	if err := mSingle.VerifySignatures([]KeySet{keySet}); err == nil {
		t.Fatal("expected threshold not met error, got nil")
	}

	// 3. Duplicate key ID rejection
	mDup := m
	mDup.Signatures = []Signature{
		{KeyID: "key-1", Signature: base64.StdEncoding.EncodeToString(sig1)},
		{KeyID: "key-1", Signature: base64.StdEncoding.EncodeToString(sig1)},
	}
	if err := mDup.VerifySignatures([]KeySet{keySet}); err == nil {
		t.Fatal("expected duplicate key error, got nil")
	}

	// 4. Corrupt signature
	mCorrupt := m
	corruptSig := make([]byte, len(sig1))
	copy(corruptSig, sig1)
	corruptSig[0] ^= 0xff
	mCorrupt.Signatures = []Signature{
		{KeyID: "key-1", Signature: base64.StdEncoding.EncodeToString(corruptSig)},
		{KeyID: "key-2", Signature: base64.StdEncoding.EncodeToString(sig2)},
	}
	if err := mCorrupt.VerifySignatures([]KeySet{keySet}); err == nil {
		t.Fatal("expected invalid signature verification failure, got nil")
	}
}

func TestManifestStrictParsing(t *testing.T) {
	validJSON := `{
		"protocol": 2,
		"target_version": "v0.7.0",
		"minimum_source_version": "v0.6.0",
		"release_sequence": 42,
		"issued_at": 1700000000,
		"expires_at": 1700003600,
		"artifact": {
			"format": "macos-app-archive-v2",
			"url": "https://example.com/app.zip",
			"sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"size_bytes": 10000,
			"running_bundle_digest": "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
		},
		"recovery": {
			"format": "macos-recovery-binary-v1",
			"url": "https://example.com/rec",
			"sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"size_bytes": 2000,
			"designated_requirement": "req"
		},
		"identity": {
			"component": "homeagent-agent",
			"os": "darwin",
			"arch": "arm64",
			"bundle_id": "com.homeagent.agent",
			"team_id": "ABC123XYZ",
			"designated_requirement": "req"
		},
		"force": false,
		"signatures": []
	}`

	m, err := ParseManifestStrict([]byte(validJSON))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if m.TargetVersion != "v0.7.0" || m.ReleaseSequence != 42 {
		t.Fatalf("unexpected parsed values: %+v", m)
	}

	// Rejection on unknown fields
	unknownFieldJSON := `{
		"protocol": 2,
		"target_version": "v0.7.0",
		"minimum_source_version": "v0.6.0",
		"release_sequence": 42,
		"issued_at": 1700000000,
		"expires_at": 1700003600,
		"artifact": {
			"format": "macos-app-archive-v2",
			"url": "https://example.com/app.zip",
			"sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"size_bytes": 10000,
			"running_bundle_digest": "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
		},
		"recovery": {
			"format": "macos-recovery-binary-v1",
			"url": "https://example.com/rec",
			"sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"size_bytes": 2000,
			"designated_requirement": "req"
		},
		"identity": {
			"component": "homeagent-agent",
			"os": "darwin",
			"arch": "arm64",
			"bundle_id": "com.homeagent.agent",
			"team_id": "ABC123XYZ",
			"designated_requirement": "req"
		},
		"force": false,
		"signatures": [],
		"unknown_field": 123
	}`
	if _, err := ParseManifestStrict([]byte(unknownFieldJSON)); err == nil {
		t.Fatal("expected unknown field parse error, got nil")
	}

	// Rejection on unsupported protocol
	badProtoJSON := strings.Replace(validJSON, `"protocol": 2`, `"protocol": 1`, 1)
	if _, err := ParseManifestStrict([]byte(badProtoJSON)); err == nil {
		t.Fatal("expected protocol version mismatch error, got nil")
	}
}

func TestSemVerComparison(t *testing.T) {
	tests := []struct {
		v1, v2 string
		want   int
	}{
		{"v0.6.10", "v0.6.10", 0},
		{"v0.6.11", "v0.6.10", 1},
		{"v0.6.9", "v0.6.10", -1},
		{"v1.0.0", "v0.9.9", 1},
		{"v0.7.0", "v1.0.0", -1},
	}

	for _, tt := range tests {
		got, err := CompareSemVer(tt.v1, tt.v2)
		if err != nil {
			t.Fatalf("CompareSemVer(%q, %q) error: %v", tt.v1, tt.v2, err)
		}
		if got != tt.want {
			t.Fatalf("CompareSemVer(%q, %q) = %d, want %d", tt.v1, tt.v2, got, tt.want)
		}
	}

	if _, _, _, err := ParseSemVer("v1.2"); err == nil {
		t.Fatal("expected invalid semver error for 2-part version")
	}
	if _, _, _, err := ParseSemVer("invalid"); err == nil {
		t.Fatal("expected invalid semver error for non-numeric string")
	}
}
