package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Execute the real downloader against a local HTTP server, without adding a
// production endpoint override or passing enrollment credentials to GitHub.
func TestInstallerDownloaderBridge(t *testing.T) {
	if os.Getenv("HOMEAGENT_INSTALLER_BRIDGE") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	required := map[string]string{"--connect-timeout": "10", "--max-time": "120", "-A": "HomeAgent-Installer"}
	if strings.Contains(filepath.Base(os.Getenv("HOMEAGENT_INSTALLER_DOWNLOADER")), "wget") {
		required = map[string]string{"-T": "10", "-U": "HomeAgent-Installer"}
	}
	for flag, value := range required {
		found := false
		for i := 0; i+1 < len(args); i++ {
			if args[i] == flag && args[i+1] == value {
				found = true
				if os.Getenv("HOMEAGENT_INSTALLER_SHORT_TIMEOUT") == "1" && (flag == "--max-time" || flag == "-T") {
					args[i+1] = "1"
				}
			}
		}
		if !found {
			fmt.Fprintln(os.Stderr, "missing required downloader bound or user agent", flag)
			os.Exit(1)
		}
	}
	for i, arg := range args {
		if strings.HasPrefix(arg, "https://api.github.com/") || strings.HasPrefix(arg, "https://github.com/") {
			u, err := url.Parse(arg)
			if err != nil {
				panic(err)
			}
			args[i] = os.Getenv("HOMEAGENT_INSTALLER_HTTP") + u.RequestURI()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if os.Getenv("HOMEAGENT_INSTALLER_SHORT_TIMEOUT") == "1" {
		var shortCancel context.CancelFunc
		ctx, shortCancel = context.WithTimeout(ctx, 2*time.Second)
		defer shortCancel()
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-signals:
			cancel()
		case <-ctx.Done():
		}
	}()
	cmd := exec.CommandContext(ctx, os.Getenv("HOMEAGENT_INSTALLER_DOWNLOADER"), args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			os.Exit(e.ExitCode())
		}
		os.Exit(1)
	}
	os.Exit(0)
}

type installerCase struct {
	name                    string
	pages                   []string
	links                   map[int]string
	version, base, checksum string
	status                  int
	enroll                  bool
	redirect                bool
	timeout                 bool
	watchdog                bool
	checksumStatus          int
	claimExit, serviceExit  int
	wantTag, wantError      string
	wantPages               int
}

func installerReleaseCases(t *testing.T) []installerCase {
	t.Helper()
	fixtureBytes, err := os.ReadFile("testdata/安装发布响应.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Release map[string]any `json:"release"`
	}
	if err = json.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatal(err)
	}
	release := func(tag string, modify func(map[string]any)) string {
		b, _ := json.Marshal(fixture.Release)
		var r map[string]any
		_ = json.Unmarshal(b, &r)
		r["tag_name"] = tag
		if modify != nil {
			modify(r)
		}
		b, _ = json.Marshal(r)
		return string(b)
	}
	good := release("agent-v0.6.24", nil)
	array := func(items ...string) string { return "[" + strings.Join(items, ",") + "]" }
	full := make([]string, 100)
	for i := range full {
		full[i] = release("server-v9.0.0", nil)
	}
	full[30] = release("agent-v0.6.9", nil)
	cases := []installerCase{
		{name: "highest stable agent with nested escaped decoys", pages: []string{array(release("agent-v0.6.9", nil), good, release("server-v9.0.0", nil), release("agent-v9.0.0", func(r map[string]any) { r["draft"] = true }), release("agent-v8.0.0", func(r map[string]any) { r["prerelease"] = true }), release("agent-v7.0.0-rc.1", nil), release("agent-v0.6.2", func(r map[string]any) {
			r["body"] = "escaped \"tag_name\":\"agent-v999.0.0\"\\\n雪😀"
			r["author"] = map[string]any{"tag_name": "agent-v999.0.0"}
		}))}, wantTag: "agent-v0.6.24", wantPages: 1},
		{name: "full page without link", pages: []string{array(full...), array(good)}, wantTag: "agent-v0.6.24", wantPages: 2},
		{name: "empty terminal page", pages: []string{array(append(full[:99], good)...), "[]"}, wantTag: "agent-v0.6.24", wantPages: 2},
		{name: "link follows fixed next page", pages: []string{array(release("agent-v0.6.9", nil)), array(good)}, links: map[int]string{1: `<https://untrusted.invalid/anything>; rel="next"`}, wantTag: "agent-v0.6.24", wantPages: 2},
		{name: "explicit agent tag", version: "agent-v0.6.24", wantTag: "agent-v0.6.24"},
		{name: "legacy exact tag", version: "v0.6.15", wantTag: "v0.6.15"},
		{name: "explicit source overrides version", base: "/custom", version: "server-invalid", wantTag: "custom"},
		{name: "invalid version", version: "server-v0.6.24", wantError: "invalid"},
		{name: "multiline version", version: "agent-v0.6.24\ninvalid", wantError: "invalid"},
		{name: "trailing newline version", version: "agent-v0.6.24\n", wantError: "invalid"},
		{name: "leading zero", version: "agent-v00.6.24", wantError: "invalid"},
		{name: "injection", version: "agent-v0.6.24/../bad", wantError: "invalid"},
		{name: "no candidate", pages: []string{array(release("server-v9.0.0", nil))}, wantError: "no stable Agent", wantPages: 1},
		{name: "incomplete scan", pages: []string{array(full...)}, wantError: "incomplete", wantPages: 10},
		{name: "invalid utf8", pages: []string{array(strings.Replace(good, `"draft":false`, `"body":"`+string([]byte{0xff})+`","draft":false`, 1))}, wantError: "invalid", wantPages: 1},
		{name: "invalid json", pages: []string{`[{"tag_name":`}, wantError: "invalid", wantPages: 1},
		{name: "top level object", pages: []string{good}, wantError: "invalid", wantPages: 1},
		{name: "duplicate field", pages: []string{array(strings.Replace(good, `"draft":false`, `"draft":true,"draft":false`, 1))}, wantError: "invalid", wantPages: 1},
		{name: "escaped duplicate field", pages: []string{array(strings.Replace(good, `"draft":false`, `"dr\u0061ft":true,"draft":false`, 1))}, wantError: "invalid", wantPages: 1},
		{name: "wrong boolean type", pages: []string{array(release("agent-v0.6.24", func(r map[string]any) { r["draft"] = "false" }))}, wantError: "invalid", wantPages: 1},
		{name: "missing critical field", pages: []string{array(release("agent-v0.6.24", func(r map[string]any) { delete(r, "prerelease") }))}, wantError: "invalid", wantPages: 1},
		{name: "wrong assets type", pages: []string{array(release("agent-v0.6.24", func(r map[string]any) { r["assets"] = true }))}, wantError: "invalid", wantPages: 1},
		{name: "missing asset no fallback", pages: []string{array(release("agent-v0.6.23", nil), release("agent-v0.6.24", func(r map[string]any) { r["assets"] = []any{} }))}, wantError: "assets", wantPages: 1},
		{name: "missing binary", pages: []string{array(release("agent-v0.6.24", func(r map[string]any) {
			r["assets"] = []any{map[string]any{"name": "homeagent-agent-linux-amd64.sha256"}, map[string]any{"name": "homeagent-agent-windows-amd64.exe.sha256"}}
		}))}, wantError: "assets", wantPages: 1},
		{name: "missing checksum asset", pages: []string{array(release("agent-v0.6.24", func(r map[string]any) {
			r["assets"] = []any{map[string]any{"name": "homeagent-agent-linux-amd64"}, map[string]any{"name": "homeagent-agent-windows-amd64.exe"}}
		}))}, wantError: "assets", wantPages: 1},
		{name: "duplicate asset", pages: []string{array(release("agent-v0.6.24", func(r map[string]any) {
			r["assets"] = []any{map[string]any{"name": "homeagent-agent-linux-amd64"}, map[string]any{"name": "homeagent-agent-linux-amd64"}, map[string]any{"name": "homeagent-agent-linux-amd64.sha256"}, map[string]any{"name": "homeagent-agent-windows-amd64.exe"}, map[string]any{"name": "homeagent-agent-windows-amd64.exe"}, map[string]any{"name": "homeagent-agent-windows-amd64.exe.sha256"}}
		}))}, wantError: "assets", wantPages: 1},
		{name: "rate limited", status: 429, wantError: "", wantPages: 1},
		{name: "download missing", version: "agent-v0.6.24", status: 404, wantError: ""},
		{name: "abnormal redirect", base: "/custom", redirect: true},
		{name: "download timeout", base: "/custom", timeout: true},
		{name: "missing checksum", base: "/custom", checksumStatus: 404},
		{name: "plain digest", base: "/custom", checksum: "plain", wantTag: "custom"},
		{name: "binary checksum marker", base: "/custom", checksum: "star", wantTag: "custom"},
		{name: "blank extra checksum line", base: "/custom", checksum: "blank-line", wantError: "checksum"},
		{name: "malformed checksum", base: "/custom", checksum: "malformed", wantError: "checksum"},
		{name: "wrong checksum filename", base: "/custom", checksum: "wrong-name", wantError: "checksum"},
		{name: "multiple checksums", base: "/custom", checksum: "multiple", wantError: "checksum"},
		{name: "checksum mismatch", base: "/custom", checksum: "mismatch", wantError: "checksum"},
		{name: "successful enrollment", base: "/custom", enroll: true, wantTag: "custom"},
		{name: "claim failure", base: "/custom", claimExit: 7, wantError: ""},
		{name: "service failure", base: "/custom", serviceExit: 8, wantError: ""},
	}
	return cases
}

func TestInstallerReleaseContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX installer is covered on POSIX hosts")
	}
	for _, tool := range []string{"curl", "wget"} {
		t.Run(tool, func(t *testing.T) {
			downloader, err := exec.LookPath(tool)
			if err != nil {
				t.Fatalf("real %s is required for installer acceptance: %v", tool, err)
			}
			for _, tc := range installerReleaseCases(t) {
				t.Run(tc.name, func(t *testing.T) { runInstallerCase(t, tool, downloader, tc, nil) })
			}
		})
	}
}

func runInstallerCase(t *testing.T, tool, downloader string, tc installerCase, windowsPayload []byte) {
	t.Helper()
	temp := t.TempDir()
	bin := filepath.Join(temp, "bin")
	install := filepath.Join(temp, "installed")
	scratch := filepath.Join(temp, "scratch")
	for _, dir := range []string{bin, install, scratch} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	calls := filepath.Join(temp, "calls")
	payload := fmt.Sprintf("#!/bin/sh\necho \"$1\" >> '%s'\ncase \"$1\" in claim) exit %d;; service) exit %d;; esac\n", calls, tc.claimExit, tc.serviceExit)
	if windowsPayload != nil {
		payload = string(windowsPayload)
	}
	asset := "homeagent-agent-linux-amd64"
	if windowsPayload != nil {
		asset = "homeagent-agent-windows-amd64.exe"
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
	var mu sync.Mutex
	var requests []string
	pageCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, r.URL.RequestURI())
		if r.Header.Get("Authorization") != "" {
			t.Error("enrollment credential leaked to release/download request")
		}
		if strings.Contains(r.URL.Path, "/releases?") {
			t.Error("query incorrectly included in path")
		}
		if r.URL.Path == "/repos/RokiLai/home-agent/releases" {
			pageCount++
			if tc.status != 0 {
				w.WriteHeader(tc.status)
				return
			}
			if r.URL.Query().Get("per_page") != "100" || r.URL.Query().Get("page") != fmt.Sprint(pageCount) {
				t.Error("wrong fixed pagination", r.URL)
			}
			if r.UserAgent() == "" {
				t.Error("missing User-Agent")
			}
			if link := tc.links[pageCount]; link != "" {
				w.Header().Set("Link", link)
			}
			if len(tc.pages) == 0 {
				t.Error("unexpected release discovery")
				http.Error(w, "unexpected", 500)
				return
			}
			idx := pageCount - 1
			if idx >= len(tc.pages) {
				idx = len(tc.pages) - 1
			}
			fmt.Fprint(w, tc.pages[idx])
			return
		}
		if tc.status != 0 {
			w.WriteHeader(tc.status)
			return
		}
		if tc.timeout || tc.watchdog {
			delay := 1500 * time.Millisecond
			if tc.watchdog {
				delay = 3 * time.Second
			}
			if windowsPayload != nil {
				delay = 11 * time.Second
			}
			time.Sleep(delay)
		}
		if tc.redirect {
			w.Header().Set("Location", "file:///tmp/untrusted-agent")
			w.WriteHeader(302)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			if tc.checksumStatus != 0 {
				w.WriteHeader(tc.checksumStatus)
				return
			}
			switch tc.checksum {
			case "plain":
				fmt.Fprintln(w, digest)
			case "star":
				fmt.Fprintln(w, digest+" *"+asset)
			case "blank-line":
				fmt.Fprintln(w, digest+"\n")
			case "malformed":
				fmt.Fprintln(w, "not-a-digest")
			case "wrong-name":
				fmt.Fprintln(w, digest+"  other-agent")
			case "multiple":
				fmt.Fprintln(w, digest+"\n"+digest)
			case "mismatch":
				fmt.Fprintln(w, strings.Repeat("0", 64))
			default:
				fmt.Fprintln(w, digest+"  "+asset)
			}
			return
		}
		fmt.Fprint(w, payload)
	}))
	defer server.Close()
	if windowsPayload == nil {
		for _, name := range []string{"awk", "tr", "mktemp", "rm", "chmod", "dirname", "cat", "mkdir", "cp", "sh", "sleep"} {
			path, err := exec.LookPath(name)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Symlink(path, filepath.Join(bin, name)); err != nil {
				t.Fatal(err)
			}
		}
		hashTool := "sha256sum"
		hashPath, hashErr := exec.LookPath(hashTool)
		if hashErr != nil {
			hashTool = "shasum"
			hashPath, hashErr = exec.LookPath(hashTool)
		}
		if hashErr != nil {
			t.Fatal("a real SHA256 tool is required", hashErr)
		}
		if err := os.Symlink(hashPath, filepath.Join(bin, hashTool)); err != nil {
			t.Fatal(err)
		}
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		wrapper := fmt.Sprintf("#!/bin/sh\nexec '%s' -test.run '^TestInstallerDownloaderBridge$' -- \"$@\"\n", strings.ReplaceAll(exe, "'", "'\\''"))
		for name, content := range map[string]string{tool: wrapper, "uname": "#!/bin/sh\n[ \"$1\" = -s ] && echo Linux || echo x86_64\n"} {
			if err = os.WriteFile(filepath.Join(bin, name), []byte(content), 0755); err != nil {
				t.Fatal(err)
			}
		}
	}
	binaryName := "homeagent-agent"
	if windowsPayload != nil {
		binaryName += ".exe"
	}
	dest := filepath.Join(install, binaryName)
	if err := os.WriteFile(dest, []byte("existing"), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "../../scripts/install.sh")
	if tc.watchdog {
		data, err := os.ReadFile("../../scripts/install.sh")
		if err != nil {
			t.Fatal(err)
		}
		copyPath := filepath.Join(temp, "install.sh")
		if err = os.WriteFile(copyPath, []byte(strings.ReplaceAll(string(data), "sleep 120", "sleep 1")), 0600); err != nil {
			t.Fatal(err)
		}
		cmd = exec.Command("/bin/sh", copyPath)
	}
	// Supply a closed environment: inherited developer overrides must not bypass discovery.
	cmd.Env = []string{"PATH=" + bin, "LC_ALL=en_US.UTF-8", "HOME=" + temp, "TMPDIR=" + scratch, "HOMEAGENT_SERVER=https://private.example", "HOMEAGENT_INSTALL_DIR=" + install, "HOMEAGENT_AGENT_VERSION=" + tc.version, "HOMEAGENT_INSTALLER_BRIDGE=1", "HOMEAGENT_INSTALLER_DOWNLOADER=" + downloader, "HOMEAGENT_INSTALLER_HTTP=" + server.URL, "GORACE=atexit_sleep_ms=0"}
	if tc.timeout {
		cmd.Env = append(cmd.Env, "HOMEAGENT_INSTALLER_SHORT_TIMEOUT=1")
	}
	if tc.base != "" {
		cmd.Env = append(cmd.Env, "HOMEAGENT_DOWNLOAD_BASE_URL="+server.URL+tc.base)
	}
	if tc.enroll || tc.claimExit != 0 || tc.serviceExit != 0 {
		cmd.Env = append(cmd.Env, "HOMEAGENT_CLAIM_TOKEN=test-claim-secret")
	}
	if windowsPayload != nil {
		script, err := os.ReadFile("../../scripts/install.ps1")
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ReplaceAll(strings.ReplaceAll(string(script), "https://api.github.com", server.URL), "https://github.com", server.URL)
		quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
		scriptPath := filepath.Join(temp, "install.ps1")
		if err = os.WriteFile(scriptPath, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		driver := filepath.Join(temp, "driver.ps1")
		driverText := "if ($PSVersionTable.PSVersion.Major -ne 5 -or $PSVersionTable.PSVersion.Minor -ne 1) { throw 'Windows PowerShell 5.1 required' }\nfunction Get-Service { @{ Status='Running' } }\nfunction Set-Service {}\n. " + quote(scriptPath) + "\n"
		if err = os.WriteFile(driver, []byte(driverText), 0600); err != nil {
			t.Fatal(err)
		}
		cmd = exec.Command(downloader, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", driver)
		cmd.Env = append(os.Environ(), "TEMP="+scratch, "TMP="+scratch, "HOMEAGENT_SERVER=https://private.example", "HOMEAGENT_INSTALL_DIR="+install, "HOMEAGENT_AGENT_VERSION="+tc.version, "HOMEAGENT_DOWNLOAD_BASE_URL=", "HOMEAGENT_CLAIM_TOKEN=", "HOMEAGENT_JOIN_TOKEN=", "HOMEAGENT_GITHUB_REPO=", "PROCESSOR_ARCHITECTURE=AMD64", "HA_TEST_CALLS="+calls, fmt.Sprintf("HA_TEST_CLAIM_EXIT=%d", tc.claimExit), fmt.Sprintf("HA_TEST_SERVICE_EXIT=%d", tc.serviceExit))
		if tc.base != "" {
			cmd.Env = append(cmd.Env, "HOMEAGENT_DOWNLOAD_BASE_URL="+server.URL+tc.base)
		}
		if tc.enroll || tc.claimExit != 0 || tc.serviceExit != 0 {
			cmd.Env = append(cmd.Env, "HOMEAGENT_CLAIM_TOKEN=test-claim-secret")
		}
	}
	started := time.Now()
	out, runErr := cmd.CombinedOutput()
	if tc.watchdog && time.Since(started) > 2500*time.Millisecond {
		t.Error("wall-clock watchdog did not terminate stalled transfer promptly")
	}
	wantFailure := tc.wantTag == "" || tc.status != 0 || tc.claimExit != 0 || tc.serviceExit != 0
	if wantFailure != (runErr != nil) {
		t.Fatalf("wantFailure=%v err=%v output=%s", wantFailure, runErr, out)
	}
	if tc.wantError != "" && !strings.Contains(string(out), tc.wantError) {
		t.Fatalf("missing error %q: %s", tc.wantError, out)
	}
	mu.Lock()
	gotPages := pageCount
	gotRequests := append([]string(nil), requests...)
	mu.Unlock()
	if gotPages != tc.wantPages {
		t.Errorf("discovery pages=%d want %d; requests=%v", gotPages, tc.wantPages, gotRequests)
	}
	if tc.wantTag != "" {
		path := "/RokiLai/home-agent/releases/download/" + tc.wantTag + "/" + asset
		if tc.wantTag == "custom" {
			path = "/custom/" + asset
		}
		if !reflect.DeepEqual(gotRequests[len(gotRequests)-2:], []string{path, path + ".sha256"}) {
			t.Errorf("wrong asset requests: %v", gotRequests)
		}
	}
	installed, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	wantInstalled := !wantFailure || tc.claimExit != 0 || tc.serviceExit != 0
	if wantInstalled && string(installed) != payload {
		t.Error("installed content does not match verified artifact")
	}
	if !wantInstalled && string(installed) != "existing" {
		t.Error("failed installation changed existing binary")
	}
	data, _ := os.ReadFile(calls)
	wantCalls := ""
	if tc.claimExit != 0 {
		wantCalls = "claim\n"
	}
	if tc.enroll || tc.serviceExit != 0 {
		wantCalls = "claim\nservice\n"
	}
	if string(data) != wantCalls {
		t.Errorf("subcommand calls=%q want %q", data, wantCalls)
	}
	if wantFailure && strings.Contains(string(out), "successfully") {
		t.Error("failed operation reported success")
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Errorf("temporary files remain: %v %v", entries, err)
	}
}

// This test executes the complete Windows script with a real native .exe stub.
// It must be run on Windows PowerShell 5.1, not satisfied by source assertions.
func TestInstallerReleaseWindowsContract(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires a real Windows PowerShell 5.1 host")
	}
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "agent.go")
	binary := filepath.Join(dir, "agent.exe")
	program := `package main
import("os";"strconv")
func main(){f,e:=os.OpenFile(os.Getenv("HA_TEST_CALLS"),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600);if e!=nil{panic(e)};defer f.Close();f.WriteString(os.Args[1]+"\n");code:=0;if os.Args[1]=="claim"{code,_=strconv.Atoi(os.Getenv("HA_TEST_CLAIM_EXIT"))}else{code,_=strconv.Atoi(os.Getenv("HA_TEST_SERVICE_EXIT"))};os.Exit(code)}
`
	if err = os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", binary, source)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("stub build: %v %s", err, out)
	}
	payload, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range installerReleaseCases(t) {
		t.Run(tc.name, func(t *testing.T) { runInstallerCase(t, "powershell", powershell, tc, payload) })
	}
}

func TestInstallerWgetWallDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX watchdog")
	}
	wget, err := exec.LookPath("wget")
	if err != nil {
		t.Fatal(err)
	}
	runInstallerCase(t, "wget", wget, installerCase{name: "wall deadline", base: "/custom", watchdog: true}, nil)
}
