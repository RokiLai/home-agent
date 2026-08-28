package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"homeagent/internal/version"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

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

func TestAgentBuildsForSupportedPlatforms(t *testing.T) {
	targets := []struct {
		os   string
		arch string
	}{
		{os: "darwin", arch: "amd64"},
		{os: "darwin", arch: "arm64"},
		{os: "linux", arch: "arm64"},
		{os: "windows", arch: "amd64"},
	}
	for _, target := range targets {
		t.Run(target.os+"-"+target.arch, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), "homeagent-agent")
			cmd := exec.Command("go", "build", "-o", outputPath, ".")
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+target.os, "GOARCH="+target.arch)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("build %s/%s: %v\n%s", target.os, target.arch, err, output)
			}
		})
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
target=""
url=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    target="$1"
  elif [ "$1" != "-fsSL" ] && [ -z "$url" ]; then
    url="$1"
  fi
  shift
done
if [ -n "$target" ]; then
  case "$url" in
    *.sha256)
      if command -v sha256sum >/dev/null 2>&1; then
        printf '#!/bin/sh\nexit 0\n' | sha256sum | awk '{print $1}' > "$target"
      elif command -v shasum >/dev/null 2>&1; then
        printf '#!/bin/sh\nexit 0\n' | shasum -a 256 | awk '{print $1}' > "$target"
      else
        printf 'f4539bd87c9f69bd46a9a7a972c3d5272a2cfbb008ab558e80112dfd6d9c66a4\n' > "$target"
      fi
      ;;
    *)
      printf '#!/bin/sh\nexit 0\n' > "$target"
      ;;
  esac
  exit 0
fi
exit 1
`)
	writeExecutable("codesign", "#!/bin/sh\nexit 0\n")

	appRoot := filepath.Join(tempDir, "HomeAgent.app")
	cmd := exec.Command("sh", installScript)
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"HOMEAGENT_SERVER=http://192.168.50.10:8888",
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
		"HOMEAGENT_SERVER=http://192.168.50.10:8888",
		"HOMEAGENT_JOIN_TOKEN=test-token",
		"HOMEAGENT_INSTALL_DIR="+filepath.Join(tempDir, "plain-bin"),
	)
	out, err := invalid.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "must end with .app/Contents/MacOS") {
		t.Fatalf("expected a non-bundle macOS install path to fail safely, err=%v output=%s", err, out)
	}
}

func TestInstallScriptChecksumVerification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}

	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	installScript := filepath.Join(repoRoot, "scripts", "install.sh")

	dummyBinary := []byte("#!/bin/sh\necho 'running agent'\nexit 0\n")
	// SHA256 of dummyBinary
	correctSHA := fmt.Sprintf("%x", sha256sumBytes(dummyBinary))
	mismatchedSHA := "0000000000000000000000000000000000000000000000000000000000000000"

	t.Run("matching checksum succeeds", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, ".sha256") {
				fmt.Fprintf(w, "%s  homeagent-agent\n", correctSHA)
				return
			}
			w.Write(dummyBinary)
		}))
		defer server.Close()

		tempDir := t.TempDir()
		installDir := filepath.Join(tempDir, "bin")

		cmd := exec.Command("sh", installScript)
		cmd.Env = append(os.Environ(),
			"HOMEAGENT_SERVER="+server.URL,
			"HOMEAGENT_CLAIM_TOKEN=test-claim-token",
			"HOMEAGENT_INSTALL_DIR="+installDir,
		)
		if runtime.GOOS == "darwin" {
			appRoot := filepath.Join(tempDir, "HomeAgent.app")
			cmd.Env = append(os.Environ(),
				"HOMEAGENT_SERVER="+server.URL,
				"HOMEAGENT_CLAIM_TOKEN=test-claim-token",
				"HOMEAGENT_INSTALL_DIR="+filepath.Join(appRoot, "Contents", "MacOS"),
			)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("install.sh with valid checksum failed: %v\n%s", err, out)
		}
	})

	t.Run("mismatched checksum fails safely and cleans up", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, ".sha256") {
				fmt.Fprintf(w, "%s  homeagent-agent\n", mismatchedSHA)
				return
			}
			w.Write(dummyBinary)
		}))
		defer server.Close()

		tempDir := t.TempDir()
		installDir := filepath.Join(tempDir, "bin")
		if runtime.GOOS == "darwin" {
			installDir = filepath.Join(tempDir, "HomeAgent.app", "Contents", "MacOS")
		}

		cmd := exec.Command("sh", installScript)
		cmd.Env = append(os.Environ(),
			"HOMEAGENT_SERVER="+server.URL,
			"HOMEAGENT_CLAIM_TOKEN=test-claim-token",
			"HOMEAGENT_INSTALL_DIR="+installDir,
		)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected install.sh with mismatched checksum to fail, but it succeeded")
		}
		if !strings.Contains(string(out), "SHA256 checksum mismatch") {
			t.Fatalf("expected output to mention checksum mismatch, got:\n%s", out)
		}
		// Verify destination binary was not created
		destBinary := filepath.Join(installDir, "homeagent-agent")
		if _, statErr := os.Stat(destBinary); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("expected binary not to be installed on checksum mismatch, found: %v", statErr)
		}
	})

	t.Run("missing checksum 404 fails safely", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, ".sha256") {
				http.NotFound(w, r)
				return
			}
			w.Write(dummyBinary)
		}))
		defer server.Close()

		tempDir := t.TempDir()
		installDir := filepath.Join(tempDir, "bin")
		if runtime.GOOS == "darwin" {
			installDir = filepath.Join(tempDir, "HomeAgent.app", "Contents", "MacOS")
		}

		cmd := exec.Command("sh", installScript)
		cmd.Env = append(os.Environ(),
			"HOMEAGENT_SERVER="+server.URL,
			"HOMEAGENT_CLAIM_TOKEN=test-claim-token",
			"HOMEAGENT_INSTALL_DIR="+installDir,
		)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected install.sh with 404 checksum to fail, but it succeeded, out=%s", out)
		}
		// Verify destination binary was not created
		destBinary := filepath.Join(installDir, "homeagent-agent")
		if _, statErr := os.Stat(destBinary); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("expected binary not to be installed on 404 checksum, found: %v", statErr)
		}
	})

	t.Run("download base URL override succeeds", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, ".sha256") {
				fmt.Fprintf(w, "%s  homeagent-agent\n", correctSHA)
				return
			}
			w.Write(dummyBinary)
		}))
		defer server.Close()

		tempDir := t.TempDir()
		installDir := filepath.Join(tempDir, "bin")
		if runtime.GOOS == "darwin" {
			installDir = filepath.Join(tempDir, "HomeAgent.app", "Contents", "MacOS")
		}

		cmd := exec.Command("sh", installScript)
		cmd.Env = append(os.Environ(),
			"HOMEAGENT_DOWNLOAD_BASE_URL="+server.URL+"/custom-downloads",
			"HOMEAGENT_CLAIM_TOKEN=test-claim-token",
			"HOMEAGENT_INSTALL_DIR="+installDir,
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("install.sh with HOMEAGENT_DOWNLOAD_BASE_URL failed: %v\n%s", err, out)
		}
		destBinary := filepath.Join(installDir, "homeagent-agent")
		if _, statErr := os.Stat(destBinary); statErr != nil {
			t.Fatalf("expected binary to be installed, err: %v", statErr)
		}
	})

	t.Run("zero token binary only install succeeds and prints guidance", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, ".sha256") {
				fmt.Fprintf(w, "%s  homeagent-agent\n", correctSHA)
				return
			}
			w.Write(dummyBinary)
		}))
		defer server.Close()

		tempDir := t.TempDir()
		installDir := filepath.Join(tempDir, "bin")
		if runtime.GOOS == "darwin" {
			installDir = filepath.Join(tempDir, "HomeAgent.app", "Contents", "MacOS")
		}

		cmd := exec.Command("sh", installScript)
		cmd.Env = append(os.Environ(),
			"HOMEAGENT_DOWNLOAD_BASE_URL="+server.URL+"/downloads",
			"HOMEAGENT_INSTALL_DIR="+installDir,
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("install.sh without token should succeed for binary install, got: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "HomeAgent Agent binary installed successfully") {
			t.Fatalf("expected output to mention successful binary installation, got:\n%s", out)
		}
		destBinary := filepath.Join(installDir, "homeagent-agent")
		if _, statErr := os.Stat(destBinary); statErr != nil {
			t.Fatalf("expected binary to be installed, err: %v", statErr)
		}
	})
}

func sha256sumBytes(b []byte) [32]byte {
	return sha256.Sum256(b)
}


func TestInterfaceClassification(t *testing.T) {
	virtuals := []string{"utun0", "bridge0", "docker0", "veth123", "tailscale0", "virbr0", "cni0", "br-deadbeef", "br-miot"}
	for _, name := range virtuals {
		if !isVirtualInterface(name) {
			t.Errorf("expected %s to be recognized as virtual", name)
		}
	}

	nonVirtuals := []string{"eth0", "en0", "eno1", "enp3s0", "wlan0", "br-lan"}
	for _, name := range nonVirtuals {
		if isVirtualInterface(name) {
			t.Errorf("expected %s NOT to be recognized as virtual", name)
		}
	}

	wireless := []string{"wlan0", "wl0", "wi-fi", "wifi", "awdl0", "bluetooth0", "llw0"}
	for _, name := range wireless {
		if !isWirelessOrBluetoothInterface(name) {
			t.Errorf("expected %s to be recognized as wireless/bluetooth", name)
		}
	}

	wired := []string{"eth0", "en0", "eno1", "enp3s0"}
	for _, name := range wired {
		if isWirelessOrBluetoothInterface(name) {
			t.Errorf("expected %s NOT to be recognized as wireless", name)
		}
	}
}

func TestParseLinuxStableIPv6FiltersTemporaryAndDeprecated(t *testing.T) {
	output := `2: eth0    inet6 2001:db8:1::10/64 scope global dynamic
2: eth0    inet6 2001:db8:1::20/64 scope global temporary dynamic
2: eth0    inet6 2001:db8:1::30/64 scope global deprecated dynamic
2: eth0    inet6 fe80::1/64 scope link
8: br-deadbeef    inet6 2001:db8:1::40/64 scope global dynamic`

	got := parseLinuxStableIPv6(output)
	if len(got) != 1 || got[0] != "2001:db8:1::10" {
		t.Fatalf("expected only stable physical-interface IPv6, got %v", got)
	}
}

func TestParseLinuxStableIPv6FailsSafelyWithoutState(t *testing.T) {
	got := parseLinuxStableIPv6("2: eth0 inet6 2001:db8:1::10/64")
	if len(got) != 0 {
		t.Fatalf("expected address without scope/state metadata to be rejected, got %v", got)
	}
}

func TestParseWindowsStableIPv6FiltersRandomAndNonPreferred(t *testing.T) {
	output := `[
{"IPAddress":"2001:db8:1::10","InterfaceAlias":"Ethernet","AddressState":"Preferred","SuffixOrigin":"Link"},
{"IPAddress":"2001:db8:1::20","InterfaceAlias":"Ethernet","AddressState":"Preferred","SuffixOrigin":"Random"},
{"IPAddress":"2001:db8:1::30","InterfaceAlias":"Ethernet","AddressState":"Deprecated","SuffixOrigin":"Link"},
{"IPAddress":"2001:db8:1::40","InterfaceAlias":"vEthernet (Default Switch)","AddressState":"Preferred","SuffixOrigin":"Link"}
]`

	got := parseWindowsStableIPv6(output)
	if len(got) != 1 || got[0] != "2001:db8:1::10" {
		t.Fatalf("expected only stable preferred physical-interface IPv6, got %v", got)
	}
}

func TestParseWindowsStableIPv6SupportsRealNumericEnums(t *testing.T) {
	output := `[
{"IPAddress":"2001:db8:1234:10:457f:4d5d:2378:3f18","InterfaceAlias":"以太网","AddressState":4,"SuffixOrigin":5},
{"IPAddress":"2001:db8:1234:10:4013:ba9:3622:ccda","InterfaceAlias":"以太网","AddressState":4,"SuffixOrigin":4},
{"IPAddress":"fe80::c0a5:c90d:6963:edd1%22","InterfaceAlias":"以太网","AddressState":4,"SuffixOrigin":4}
]`

	got := parseWindowsStableIPv6(output)
	if len(got) != 1 || got[0] != "2001:db8:1234:10:4013:ba9:3622:ccda" {
		t.Fatalf("expected only the real stable Windows IPv6, got %v", got)
	}
}

func TestParseWindowsStableIPv6FailsSafelyWithoutStatus(t *testing.T) {
	got := parseWindowsStableIPv6(`{"IPAddress":"2001:db8:1::10","InterfaceAlias":"Ethernet"}`)
	if len(got) != 0 {
		t.Fatalf("expected address without status metadata to be rejected, got %v", got)
	}
}

func TestLocalMAC(t *testing.T) {
	// Should return a valid string or empty (if no physical interface in env) without panic
	mac := localMAC(nil)
	t.Logf("localMAC(nil) returned: %q", mac)

	// If a valid address is supplied, it should still function safely
	macWithAddr := localMAC([]string{"127.0.0.1", "::1"})
	t.Logf("localMAC with loopback returned: %q", macWithAddr)
}

func TestLocalDevice(t *testing.T) {
	dev, keyPath, err := localDevice("testuser", 22)
	if err != nil {
		t.Fatalf("localDevice failed: %v", err)
	}
	if dev.ID == "" {
		t.Errorf("expected non-empty device ID")
	}
	if dev.SSHUser != "testuser" || dev.SSHPort != 22 {
		t.Errorf("unexpected ssh config in device: %+v", dev)
	}
	if keyPath == "" {
		t.Errorf("expected non-empty keyPath")
	}
}

func TestSendDeviceFactsUsesDeviceCredentialAndFailsOver(t *testing.T) {
	var received deviceFactsPayload
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "dead.invalid" {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
		}
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/devices/dev-1/facts" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer device-secret" {
			t.Fatalf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}

	facts := deviceFactsPayload{
		Hostname: "host", MAC: "02:00:00:00:00:01", AgentVersion: "v0.4.4",
		OS: "linux", Arch: "amd64", SSHUser: "user", SSHPort: 22,
		Addresses: []string{"192.168.1.42"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	target, err := sendDeviceFacts(ctx, client, []string{"http://dead.invalid", "http://live.invalid"}, func() string { return "http://dead.invalid" }, "device-secret", "dev-1", facts)
	if err != nil {
		t.Fatal(err)
	}
	if target != "http://live.invalid" {
		t.Fatalf("target = %q, want live server", target)
	}
	if received.Hostname != facts.Hostname || received.MAC != facts.MAC || len(received.Addresses) != 1 {
		t.Fatalf("unexpected payload: %+v", received)
	}
}

func TestSendDeviceFactsFailsSafelyWhenServerRejectsUpdate(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("unauthorized"))}, nil
	})}
	_, err := sendDeviceFacts(context.Background(), client, []string{"http://server.invalid"}, nil, "bad-token", "dev-1", deviceFactsPayload{})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("expected safe HTTP 401 failure, got %v", err)
	}
}

func TestSendDeviceFactsFallsBackForLegacyServer(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		body, _ := io.ReadAll(r.Body)
		if requests == 1 {
			if !bytes.Contains(body, []byte("control_protocols")) {
				t.Fatalf("v1 facts missing capability: %s", body)
			}
			return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`invalid JSON request: json: unknown field "control_protocols"`))}, nil
		}
		if bytes.Contains(body, []byte("control_protocols")) {
			t.Fatalf("legacy retry retained capability: %s", body)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	facts := deviceFactsPayload{Hostname: "host", ControlProtocols: []int{1}}
	target, err := sendDeviceFacts(context.Background(), client, []string{"http://legacy.invalid"}, nil, "token", "dev", facts)
	if err != nil || target != "http://legacy.invalid" || requests != 2 {
		t.Fatalf("fallback target=%q requests=%d err=%v", target, requests, err)
	}
}

func TestStartDeviceFactsReporterSendsInitialSnapshot(t *testing.T) {
	received := make(chan deviceFactsPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/devices/dev-start/facts" {
			http.NotFound(w, r)
			return
		}
		var facts deviceFactsPayload
		if err := json.NewDecoder(r.Body).Decode(&facts); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		received <- facts
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startDeviceFactsReporter(ctx, []string{server.URL}, nil, "device-token", "dev-start", nil)

	select {
	case facts := <-received:
		if facts.Hostname == "" || facts.AgentVersion != version.Get() || facts.OS != runtime.GOOS || len(facts.ControlProtocols) != 1 || facts.ControlProtocols[0] != 1 {
			t.Fatalf("unexpected initial facts: %+v", facts)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("device facts reporter did not send its initial snapshot")
	}
}

func TestClaimAndDeviceConfigPersistence(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "device.json")

	// 1. Mock Claim Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/devices/claim" {
			http.NotFound(w, r)
			return
		}
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer test-claim-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":          true,
			"device_id":        "dev-claimed-12345",
			"device_token":     "dev_secret_token_9999",
			"admin_public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAdminMockKey...",
		})
	}))
	defer server.Close()

	// 2. Run claim command
	err := claim([]string{
		"--server", server.URL,
		"--claim-token", "test-claim-token",
		"--config", cfgFile,
		"--ssh-user", "tester",
	})
	if err != nil {
		t.Fatalf("claim failed: %v", err)
	}

	// 3. Verify device.json was written with 0600 permissions
	info, err := os.Stat(cfgFile)
	if err != nil {
		t.Fatalf("device.json not found: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("expected file mode 0600, got %o", perm)
		}
	}

	// 4. Verify config contents
	devCfg, err := loadDeviceConfig(cfgFile)
	if err != nil {
		t.Fatalf("loadDeviceConfig failed: %v", err)
	}
	if devCfg.DeviceID != "dev-claimed-12345" || devCfg.DeviceToken != "dev_secret_token_9999" || devCfg.ServerURL != server.URL {
		t.Fatalf("unexpected devCfg: %+v", devCfg)
	}
}

func TestClaimServerResolution(t *testing.T) {
	dir := t.TempDir()

	// 1. Mock Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":          true,
			"device_id":        "dev-resolution-1",
			"device_token":     "dev_tok_resolution",
			"admin_public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIResolutionKey...",
		})
	}))
	defer server.Close()

	// A. 测试 --server 显式命令行覆盖
	cfg1 := filepath.Join(dir, "device1.json")
	err := claim([]string{
		"--server", server.URL,
		"--claim-token", "tok-1",
		"--config", cfg1,
		"--ssh-user", "tester",
	})
	if err != nil {
		t.Fatalf("claim with --server failed: %v", err)
	}
	cfgData1, err := loadDeviceConfig(cfg1)
	if err != nil {
		t.Fatal(err)
	}
	if cfgData1.ServerURL != server.URL {
		t.Fatalf("expected ServerURL %s, got %s", server.URL, cfgData1.ServerURL)
	}

	// B. 测试 HOMEAGENT_SERVER 环境变量覆盖
	cfg2 := filepath.Join(dir, "device2.json")
	t.Setenv("HOMEAGENT_SERVER", server.URL)
	t.Setenv("HOMEAGENT_CLAIM_TOKEN", "tok-2")
	err = claim([]string{
		"--config", cfg2,
		"--ssh-user", "tester",
	})
	if err != nil {
		t.Fatalf("claim with HOMEAGENT_SERVER env failed: %v", err)
	}
	cfgData2, err := loadDeviceConfig(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if cfgData2.ServerURL != server.URL {
		t.Fatalf("expected ServerURL %s, got %s", server.URL, cfgData2.ServerURL)
	}
}

func TestSplitAndNormalizeURLs(t *testing.T) {
	tests := []struct {
		input []string
		want  []string
	}{
		{
			input: []string{"http://192.168.50.10:8888, http://192.168.50.20:8888/"},
			want:  []string{"http://192.168.50.10:8888", "http://192.168.50.20:8888"},
		},
		{
			input: []string{"192.168.50.10:8888; 192.168.50.20:8888", "http://192.168.50.10:8888"},
			want:  []string{"http://192.168.50.10:8888", "http://192.168.50.20:8888"},
		},
		{
			input: []string{"", "   ", "https://server.home:8888//"},
			want:  []string{"https://server.home:8888"},
		},
	}

	for _, tt := range tests {
		got := splitAndNormalizeURLs(tt.input...)
		if len(got) != len(tt.want) {
			t.Fatalf("splitAndNormalizeURLs(%v) = %v, want %v", tt.input, got, tt.want)
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitAndNormalizeURLs(%v)[%d] = %s, want %s", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestClaimMultiServerFailover(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "device.json")

	// 1. Dead Server (503)
	deadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer deadServer.Close()

	// 2. Live Claim Server
	liveServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/devices/claim" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":          true,
			"device_id":        "dev-failover-123",
			"device_token":     "dev_secret_token_live",
			"admin_public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAdminLiveMock...",
		})
	}))
	defer liveServer.Close()

	// Run claim with comma-separated dead and live servers
	err := claim([]string{
		"--server", deadServer.URL + "," + liveServer.URL,
		"--claim-token", "test-token",
		"--config", cfgFile,
		"--ssh-user", "tester",
	})
	if err != nil {
		t.Fatalf("claim with failover failed: %v", err)
	}

	devCfg, err := loadDeviceConfig(cfgFile)
	if err != nil {
		t.Fatalf("loadDeviceConfig failed: %v", err)
	}
	if devCfg.DeviceID != "dev-failover-123" {
		t.Fatalf("unexpected device id: %s", devCfg.DeviceID)
	}
	if devCfg.ServerURL != liveServer.URL {
		t.Fatalf("expected successful ServerURL to be %s, got %s", liveServer.URL, devCfg.ServerURL)
	}
	if len(devCfg.ServerURLs) != 2 {
		t.Fatalf("expected 2 ServerURLs persisted, got %v", devCfg.ServerURLs)
	}
}

func TestSendNetworkReportReusesRevisionAcrossTransportRetries(t *testing.T) {
	tempDir := t.TempDir()
	store := newRevisionStore(tempDir)
	var mu sync.Mutex
	var receivedRevisions []uint64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		rev := uint64(req["revision"].(float64))
		mu.Lock()
		receivedRevisions = append(receivedRevisions, rev)
		count := len(receivedRevisions)
		mu.Unlock()

		if count < 3 {
			http.Error(w, "temporary error", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"accepted_revision":` + fmt.Sprint(rev) + `,"changed":true}`))
	}))
	defer server.Close()

	cfg := networkReportConfig{
		ctx:          context.Background(),
		client:       server.Client(),
		serverURLs:   []string{server.URL},
		token:        "test-token",
		deviceID:     "dev-1",
		networkID:    "home",
		reportType:   reportTypeDeviceNetworkState,
		endpointPath: "/api/v1/devices/dev-1/network-state",
		buildPayload: func(rev uint64, observedAt time.Time) (any, error) {
			return map[string]any{"network_id": "home", "revision": rev, "observed_at": observedAt}, nil
		},
		store: store,
	}

	if err := sendNetworkReport(cfg); err != nil {
		t.Fatalf("sendNetworkReport failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(receivedRevisions) != 3 {
		t.Fatalf("expected 3 attempts, got %d", len(receivedRevisions))
	}
	for i, rev := range receivedRevisions {
		if rev != 1 {
			t.Errorf("attempt %d used revision %d, want 1", i+1, rev)
		}
	}
}

func TestSendNetworkReportStructured409CatchupOnce(t *testing.T) {
	tempDir := t.TempDir()
	store := newRevisionStore(tempDir)
	var mu sync.Mutex
	var receivedRevisions []uint64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		rev := uint64(req["revision"].(float64))
		mu.Lock()
		receivedRevisions = append(receivedRevisions, rev)
		count := len(receivedRevisions)
		mu.Unlock()

		if count == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":             "revision_conflict",
				"current_revision":  10,
				"received_revision": rev,
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"accepted_revision":` + fmt.Sprint(rev) + `,"changed":true}`))
	}))
	defer server.Close()

	cfg := networkReportConfig{
		ctx:          context.Background(),
		client:       server.Client(),
		serverURLs:   []string{server.URL},
		token:        "test-token",
		deviceID:     "dev-409",
		networkID:    "home",
		reportType:   reportTypeDeviceNetworkState,
		endpointPath: "/api/v1/devices/dev-409/network-state",
		buildPayload: func(rev uint64, observedAt time.Time) (any, error) {
			return map[string]any{"network_id": "home", "revision": rev, "observed_at": observedAt}, nil
		},
		store: store,
	}

	if err := sendNetworkReport(cfg); err != nil {
		t.Fatalf("sendNetworkReport failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(receivedRevisions) != 2 {
		t.Fatalf("expected 2 attempts (initial + catchup), got %d", len(receivedRevisions))
	}
	if receivedRevisions[0] != 1 {
		t.Errorf("initial revision = %d, want 1", receivedRevisions[0])
	}
	if receivedRevisions[1] != 11 {
		t.Errorf("catchup revision = %d, want 11", receivedRevisions[1])
	}

	cur, err := store.Current(revisionKey{ReportType: reportTypeDeviceNetworkState, DeviceID: "dev-409", NetworkID: "home"})
	if err != nil || cur != 11 {
		t.Fatalf("persisted current revision = %d, err = %v", cur, err)
	}
}

func TestSendNetworkReportRejectsInvalid409WithoutCatchup(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "plaintext 409", contentType: "text/plain", body: "conflict"},
		{name: "unknown error code", contentType: "application/json", body: `{"error":"other_error","current_revision":10,"received_revision":1}`},
		{name: "mismatched received revision", contentType: "application/json", body: `{"error":"revision_conflict","current_revision":10,"received_revision":99}`},
		{name: "current revision smaller than received", contentType: "application/json", body: `{"error":"revision_conflict","current_revision":0,"received_revision":1}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			store := newRevisionStore(tempDir)
			var attempts int

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()

			cfg := networkReportConfig{
				ctx:          ctx,
				client:       server.Client(),
				serverURLs:   []string{server.URL},
				token:        "test-token",
				deviceID:     "dev-invalid-409",
				networkID:    "home",
				reportType:   reportTypeDeviceNetworkState,
				endpointPath: "/api/v1/devices/dev-invalid-409/network-state",
				buildPayload: func(rev uint64, observedAt time.Time) (any, error) {
					return map[string]any{"network_id": "home", "revision": rev, "observed_at": observedAt}, nil
				},
				store: store,
			}

			_ = sendNetworkReport(cfg)

			cur, err := store.Current(revisionKey{ReportType: reportTypeDeviceNetworkState, DeviceID: "dev-invalid-409", NetworkID: "home"})
			if err != nil || cur != 1 {
				t.Fatalf("revision must remain 1 after invalid 409: cur=%d err=%v", cur, err)
			}
		})
	}
}

func TestSendNetworkReportMultiServerCatchupBounded(t *testing.T) {
	tempDir := t.TempDir()
	store := newRevisionStore(tempDir)
	var mu sync.Mutex
	serverARevisions := []uint64{}
	serverBRevisions := []uint64{}

	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		rev := uint64(req["revision"].(float64))
		mu.Lock()
		serverARevisions = append(serverARevisions, rev)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":             "revision_conflict",
			"current_revision":  5,
			"received_revision": rev,
		})
	}))
	defer serverA.Close()

	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		rev := uint64(req["revision"].(float64))
		mu.Lock()
		serverBRevisions = append(serverBRevisions, rev)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":             "revision_conflict",
			"current_revision":  100,
			"received_revision": rev,
		})
	}))
	defer serverB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	cfg := networkReportConfig{
		ctx:             ctx,
		client:          http.DefaultClient,
		serverURLs:      []string{serverA.URL, serverB.URL},
		getActiveServer: func() string { return serverA.URL },
		token:           "test-token",
		deviceID:        "dev-bounded",
		networkID:       "home",
		reportType:      reportTypeDeviceNetworkState,
		endpointPath:    "/api/v1/devices/dev-bounded/network-state",
		buildPayload: func(rev uint64, observedAt time.Time) (any, error) {
			return map[string]any{"network_id": "home", "revision": rev, "observed_at": observedAt}, nil
		},
		store: store,
	}

	_ = sendNetworkReport(cfg)

	mu.Lock()
	defer mu.Unlock()

	cur, err := store.Current(revisionKey{ReportType: reportTypeDeviceNetworkState, DeviceID: "dev-bounded", NetworkID: "home"})
	if err != nil || cur != 6 {
		t.Fatalf("revision must only advance once to 6 (max(1, 5) + 1), got cur=%d err=%v", cur, err)
	}
	if len(serverARevisions) < 2 {
		t.Fatalf("expected server A to be called at least twice (rev 1 and rev 6), got %v", serverARevisions)
	}
	if serverARevisions[0] != 1 || serverARevisions[1] != 6 {
		t.Errorf("unexpected server A revisions: %v", serverARevisions)
	}
}

func TestSendNetworkReportFailsSafeWhenStoreFails(t *testing.T) {
	tempDir := t.TempDir()
	store := newRevisionStore(tempDir)
	key := revisionKey{ReportType: reportTypeDeviceNetworkState, DeviceID: "dev-exhaust", NetworkID: "home"}
	path := store.pathFor(key)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	record := revisionRecord{SchemaVersion: 99, ReportType: key.ReportType, DeviceID: key.DeviceID, NetworkID: key.NetworkID, Revision: 1}
	b, _ := json.Marshal(record)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := networkReportConfig{
		ctx:          context.Background(),
		client:       server.Client(),
		serverURLs:   []string{server.URL},
		token:        "test-token",
		deviceID:     "dev-exhaust",
		networkID:    "home",
		reportType:   reportTypeDeviceNetworkState,
		endpointPath: "/api/v1/devices/dev-exhaust/network-state",
		buildPayload: func(rev uint64, observedAt time.Time) (any, error) {
			return map[string]any{"network_id": "home", "revision": rev, "observed_at": observedAt}, nil
		},
		store: store,
	}

	err := sendNetworkReport(cfg)
	if err == nil || !errors.Is(err, errUnknownRevisionSchema) {
		t.Fatalf("expected unknown schema error, got %v", err)
	}
	if requests != 0 {
		t.Fatalf("HTTP request must not be sent when store fails to allocate, requests=%d", requests)
	}
}
