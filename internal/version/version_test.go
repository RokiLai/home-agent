package version

import (
	"os"
	"strings"
	"testing"
)

func TestDefaultVersionHasSingleSource(t *testing.T) {
	if defaultServerVersion != "v0.6.33" || defaultAgentVersion != "v0.6.17" {
		t.Fatalf("defaults = %q/%q, want v0.6.33/v0.6.17", defaultServerVersion, defaultAgentVersion)
	}
	if ServerVersion != defaultServerVersion || AgentVersion != defaultAgentVersion {
		t.Fatalf("injected versions = %q/%q, want defaults", ServerVersion, AgentVersion)
	}

	source, err := os.ReadFile("version.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), `const defaultServerVersion = "v0.6.33"`) {
		t.Fatalf("version.go does not contain expected server version literal")
	}
	if !strings.Contains(string(source), `const defaultAgentVersion = "v0.6.17"`) {
		t.Fatalf("version.go does not contain expected agent version literal")
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
