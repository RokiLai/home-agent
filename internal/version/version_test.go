package version

import (
	"os"
	"strings"
	"testing"
)

func TestDefaultVersionHasSingleSource(t *testing.T) {
	if defaultServerVersion != "v0.6.14" || defaultAgentVersion != "v0.6.14" {
		t.Fatalf("defaults = %q/%q, want v0.6.14/v0.6.14", defaultServerVersion, defaultAgentVersion)
	}
	if ServerVersion != defaultServerVersion || AgentVersion != defaultAgentVersion {
		t.Fatalf("injected versions = %q/%q, want defaults", ServerVersion, AgentVersion)
	}

	source, err := os.ReadFile("version.go")
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(source), `"v0.6.14"`); count != 2 {
		t.Fatalf("version.go contains %d component version literals, want 2", count)
	}
}

func TestComponentVersionsAreIndependent(t *testing.T) {
	originalServer, originalAgent := ServerVersion, AgentVersion
	t.Cleanup(func() { ServerVersion, AgentVersion = originalServer, originalAgent })

	ServerVersion = " v1.2.3 "
	AgentVersion = "v9.8.7"
	if got := GetServer(); got != "v1.2.3" {
		t.Fatalf("GetServer() = %q", got)
	}
	if got := GetAgent(); got != "v9.8.7" {
		t.Fatalf("GetAgent() = %q", got)
	}
	ServerVersion, AgentVersion = "", " \t"
	if GetServer() != defaultServerVersion || GetAgent() != defaultAgentVersion {
		t.Fatal("empty injected versions must use component defaults")
	}
}
