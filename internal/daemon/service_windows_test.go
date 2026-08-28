package daemon

import (
	"strings"
	"testing"
)

func TestFormatWindowsServiceCommand(t *testing.T) {
	binaryPath := `C:\Program Files\HomeAgent\homeagent-agent.exe`
	serverURL := "https://agent.example.com:8443"
	token := "token-secret-12345"

	cmd := formatWindowsServiceCommand(binaryPath, serverURL, token)

	expectedPrefix := `"` + binaryPath + `" daemon --server "` + serverURL + `" --token "` + token + `"`
	if cmd != expectedPrefix {
		t.Fatalf("unexpected formatted command: got %q, want %q", cmd, expectedPrefix)
	}

	if !strings.Contains(cmd, binaryPath) {
		t.Fatalf("command missing binary path: %s", cmd)
	}
	if !strings.Contains(cmd, "daemon") {
		t.Fatalf("command missing daemon subcommand: %s", cmd)
	}
}
