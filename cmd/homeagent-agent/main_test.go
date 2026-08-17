package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAtomicWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".ssh", "authorized_keys")
	if err := atomicWrite(path, []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(path, []byte("two\n")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "two\n" {
		t.Fatalf("got %q", b)
	}
}
func TestApplyKeysRejectsInvalidKey(t *testing.T) {
	if err := applyKeys(strings.NewReader(`{"keys":[{"device_id":"x","public_key":"bad"}]}`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestRunServiceValidation(t *testing.T) {
	if err := runService(nil); err == nil {
		t.Fatal("expected error with no arguments")
	}
	if err := runService([]string{"invalid"}); err == nil {
		t.Fatal("expected error with invalid action")
	}
}

func TestRunDaemonRequiresArgs(t *testing.T) {
	t.Setenv("HOMEAGENT_SERVER", "")
	t.Setenv("HOMEAGENT_JOIN_TOKEN", "")
	if err := runDaemon(nil); err == nil {
		t.Fatal("expected error when server and token missing")
	}
}

func TestDarwinInstallerCreatesResponsibleAppBundle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}

	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	installScript := filepath.Join(repoRoot, "scripts", "install.sh")
	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeExecutable := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable("uname", "#!/bin/sh\n[ \"${1:-}\" = \"-s\" ] && echo Darwin || echo arm64\n")
	writeExecutable("curl", `#!/bin/sh
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    printf '#!/bin/sh\nexit 0\n' > "$1"
    exit 0
  fi
  shift
done
exit 1
`)
	writeExecutable("codesign", "#!/bin/sh\nexit 0\n")

	appRoot := filepath.Join(tempDir, "HomeAgent.app")
	cmd := exec.Command("sh", installScript)
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"HOMEAGENT_SERVER=http://192.168.31.64:8888",
		"HOMEAGENT_JOIN_TOKEN=test-token",
		"HOMEAGENT_INSTALL_DIR="+filepath.Join(appRoot, "Contents", "MacOS"),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}

	info, err := os.ReadFile(filepath.Join(appRoot, "Contents", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"com.homeagent.app", "NSLocalNetworkUsageDescription"} {
		if !strings.Contains(string(info), want) {
			t.Fatalf("Info.plist missing %q:\n%s", want, info)
		}
	}
	if _, err := os.Stat(filepath.Join(appRoot, "Contents", "MacOS", "homeagent-agent")); err != nil {
		t.Fatal(err)
	}

	invalid := exec.Command("sh", installScript)
	invalid.Env = append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"HOMEAGENT_SERVER=http://192.168.31.64:8888",
		"HOMEAGENT_JOIN_TOKEN=test-token",
		"HOMEAGENT_INSTALL_DIR="+filepath.Join(tempDir, "plain-bin"),
	)
	out, err := invalid.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "must end with .app/Contents/MacOS") {
		t.Fatalf("expected a non-bundle macOS install path to fail safely, err=%v output=%s", err, out)
	}
}
