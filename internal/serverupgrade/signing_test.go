package serverupgrade

import (
	"os"
	"runtime"
	"testing"
)

func TestParseSignerConfig(t *testing.T) {
	origEnv := os.Getenv(EnvServerCodesignIdentity)
	defer os.Setenv(EnvServerCodesignIdentity, origEnv)

	t.Run("empty or whitespace disabled", func(t *testing.T) {
		for _, v := range []string{"", "   ", "\t\n"} {
			os.Setenv(EnvServerCodesignIdentity, v)
			cfg, err := LoadSignerConfig()
			if err != nil {
				t.Fatalf("expected nil error for %q, got: %v", v, err)
			}
			if cfg.Enabled {
				t.Fatalf("expected disabled for %q", v)
			}
		}
	})

	t.Run("valid 40 hex fingerprint normalized to uppercase", func(t *testing.T) {
		validFP := "1835c8fcc239ccac08b6cbeb7d94f07d07327fb7"
		expected := "1835C8FCC239CCAC08B6CBEB7D94F07D07327FB7"
		os.Setenv(EnvServerCodesignIdentity, "  "+validFP+"  ")
		cfg, err := LoadSignerConfig()
		if runtime.GOOS != "darwin" {
			if err == nil {
				t.Fatalf("expected error on non-darwin platform when fingerprint configured")
			}
			return
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.Enabled {
			t.Fatalf("expected enabled")
		}
		if cfg.Fingerprint != expected {
			t.Fatalf("expected fingerprint %s, got %s", expected, cfg.Fingerprint)
		}
		if cfg.Identifier != DefaultSignerIdentifier {
			t.Fatalf("expected identifier %s, got %s", DefaultSignerIdentifier, cfg.Identifier)
		}
	})

	t.Run("invalid fingerprint non-hex rejected", func(t *testing.T) {
		os.Setenv(EnvServerCodesignIdentity, "HomeAgent Code Signing")
		_, err := LoadSignerConfig()
		if err == nil {
			t.Fatalf("expected error for cert name string")
		}
	})

	t.Run("invalid fingerprint wrong length rejected", func(t *testing.T) {
		os.Setenv(EnvServerCodesignIdentity, "1835C8FCC239")
		_, err := LoadSignerConfig()
		if err == nil {
			t.Fatalf("expected error for short fingerprint")
		}
	})
}
