package domainbinding

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadConfigSafelyDisablesInvalidOrInsecureConfiguration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cloudflare-ddns.json")

	result := LoadConfig(path)
	if result.Enabled || result.State != ConfigUnconfigured {
		t.Fatalf("missing config = %+v", result)
	}

	if err := os.WriteFile(path, []byte(`{"api_token":"secret","zone_id":"zone","managed_suffix":"wrong.example"}`), 0600); err != nil {
		t.Fatal(err)
	}
	result = LoadConfig(path)
	if result.Enabled || result.State != ConfigInvalid || strings.Contains(result.Diagnostic, "secret") {
		t.Fatalf("suffix mismatch = %+v", result)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		result = LoadConfig(path)
		if result.Enabled || result.State != ConfigInsecurePermissions {
			t.Fatalf("wide permissions = %+v", result)
		}
	}
}

func TestLoadConfigAcceptsStrictProductionShapeWithoutExposingToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloudflare-ddns.json")
	if err := os.WriteFile(path, []byte(`{"api_token":"top-secret","zone_id":"zone-1","managed_suffix":"rokilai.online"}`), 0600); err != nil {
		t.Fatal(err)
	}
	result := LoadConfig(path)
	if !result.Enabled || result.Config == nil || result.Config.Token() != "top-secret" {
		t.Fatalf("valid config = %+v", result)
	}
	if strings.Contains(result.Diagnostic, "top-secret") {
		t.Fatal("diagnostic leaked token")
	}
}

func TestPermissionsDirectCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.json")
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSecurePermissions(info) {
		t.Fatalf("expected 0600 file to have secure permissions")
	}
	if !ownedByCurrentUser(info) {
		t.Fatalf("expected file to be owned by current user")
	}
}
