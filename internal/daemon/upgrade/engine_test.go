package upgrade

import (
	"strings"
	"testing"
)

func TestEngineFencedTokenAndFactsDigest(t *testing.T) {
	tokenStr, tokenBytes, err := GenerateFenceToken()
	if err != nil {
		t.Fatalf("GenerateFenceToken failed: %v", err)
	}
	if len(tokenBytes) != 32 || len(tokenStr) == 0 {
		t.Fatalf("unexpected token length: bytes=%d, str=%q", len(tokenBytes), tokenStr)
	}

	fenceDigest := ComputeFenceDigest(tokenBytes)
	if len(fenceDigest) != 64 {
		t.Fatalf("unexpected fence digest length: %d", len(fenceDigest))
	}

	params := FactsDigestParams{
		DeviceID:            "dev-1",
		CommandID:           "cmd-1",
		TransactionID:       "tx-1",
		TargetVersion:       "v0.7.0",
		UpgradeSecurityMode: "v2_locked",
		FenceRevision:       10,
		ReleaseSequence:     42,
		FenceTokenDigest:    fenceDigest,
		ManifestDigest:      strings.Repeat("a", 64),
		RunningBundleDigest: strings.Repeat("b", 64),
	}

	digest1, err := ComputeFactsDigest(params)
	if err != nil {
		t.Fatalf("ComputeFactsDigest failed: %v", err)
	}
	digest2, err := ComputeFactsDigest(params)
	if err != nil {
		t.Fatalf("ComputeFactsDigest repeat failed: %v", err)
	}
	if digest1 != digest2 || len(digest1) != 64 {
		t.Fatalf("expected deterministic 64-hex digest, got %q vs %q", digest1, digest2)
	}

	// Negative case: invalid hex length
	badParams := params
	badParams.ManifestDigest = "invalid"
	if _, err := ComputeFactsDigest(badParams); err == nil {
		t.Fatal("expected error on invalid manifest digest hex, got nil")
	}
}

func TestUpgradeConfirmationValidation(t *testing.T) {
	conf := UpgradeConfirmation{
		State:         "prepared",
		CommandID:     "cmd-1",
		FenceRevision: 10,
		FenceDigest:   "f-digest",
		FactsDigest:   "facts-digest",
		ServerNonce:   "nonce-123",
	}

	if err := conf.ValidateConfirmation("prepared", "cmd-1", 10, "f-digest", "facts-digest"); err != nil {
		t.Fatalf("unexpected confirmation validation failure: %v", err)
	}

	// State mismatch
	if err := conf.ValidateConfirmation("committed", "cmd-1", 10, "f-digest", "facts-digest"); err == nil {
		t.Fatal("expected state mismatch error, got nil")
	}

	// Command ID mismatch
	if err := conf.ValidateConfirmation("prepared", "cmd-2", 10, "f-digest", "facts-digest"); err == nil {
		t.Fatal("expected command_id mismatch error, got nil")
	}

	// Missing server nonce
	confNoNonce := conf
	confNoNonce.ServerNonce = ""
	if err := confNoNonce.ValidateConfirmation("prepared", "cmd-1", 10, "f-digest", "facts-digest"); err == nil {
		t.Fatal("expected missing server_nonce error, got nil")
	}
}
