package ui

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"homeagent/internal/device"
)

func TestIdempotencyKeyBrowserCompatibility(t *testing.T) {
	cmd := exec.Command("node", "--test", "testdata/idempotency.test.mjs")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("idempotency JavaScript tests failed: %v\n%s", err, output)
	}
}

func TestDeviceListResponseRendersDOMWithoutUncaughtErrors(t *testing.T) {
	cmd := exec.Command("node", "--test", "testdata/device-rendering.test.mjs")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("device rendering JavaScript tests failed: %v\n%s", err, output)
	}
}

func TestLogoutResponseRendersVisibleLoginPanel(t *testing.T) {
	cmd := exec.Command("node", "--test", "testdata/auth-logout.test.mjs")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("logout JavaScript DOM test failed: %v\n%s", err, output)
	}
}

func TestUpgradeAllFrontendTracksFinalOutcomes(t *testing.T) {
	cmd := exec.Command("node", "--test", "testdata/upgrade-results.test.mjs")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("upgrade results JavaScript tests failed: %v\n%s", err, output)
	}
}

func TestMobileResponsiveContractAndRendering(t *testing.T) {
	cmd := exec.Command("node", "--test", "testdata/mobile-responsive.test.mjs")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mobile responsive JavaScript DOM tests failed: %v\n%s", err, output)
	}
}

func TestMultiUserAndRBACDOMTests(t *testing.T) {
	cmd := exec.Command("node", "--test", "testdata/multi-user-rbac.test.mjs")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("multi-user and RBAC DOM tests failed: %v\n%s", err, output)
	}
}

func TestFrontendSyntaxAndScopeIntegrity(t *testing.T) {
	cmd := exec.Command("node", "--test", "testdata/frontend-syntax.test.mjs")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("frontend syntax and scope JavaScript tests failed: %v\n%s", err, output)
	}
}

func TestBrowserLayoutAndAccessibility(t *testing.T) {
	var authMu sync.Mutex
	isLoggedIn := false

	mux := http.NewServeMux()
	uiHandler := Handler()

	mux.HandleFunc("/api/v1/auth/me", func(w http.ResponseWriter, r *http.Request) {
		authMu.Lock()
		defer authMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !isLoggedIn {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.Write([]byte(`{"username":"admin"}`))
	})

	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		authMu.Lock()
		isLoggedIn = true
		authMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"username":"admin"}`))
	})

	mux.HandleFunc("/api/v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		authMu.Lock()
		isLoggedIn = false
		authMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
	})

	mux.HandleFunc("/api/v1/devices", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mockDevices := map[string]interface{}{
			"server_hash": "a1b2c3d4e5f6",
			"devices": []map[string]interface{}{
				{
					"id":                  "dev-test-1",
					"hostname":            "MacBook-Pro-Test",
					"alias":               "开发主力机",
					"mac":                 "02:00:00:00:00:01",
					"agent_version":       "v0.7.0",
					"os":                  "darwin",
					"arch":                "arm64",
					"ssh_user":            "admin",
					"ssh_port":            22,
					"addresses":           []string{"192.168.1.100", "192.168.1.101", "2001:db8:8207::1"},
					"ddns_domain":         "mac.home.internal",
					"sync_status":         "synced",
					"applied_hash":        "a1b2c3d4e5f6",
					"sync_updated_at":     time.Now().Format(time.RFC3339),
					"github_sync_enabled": true,
					"github_status":       "synced",
					"connected":           true,
					"health": map[string]interface{}{
						"status":  "healthy",
						"reasons": []interface{}{},
						"facts": map[string]interface{}{
							"observed_at":            time.Now().Format(time.RFC3339),
							"uptime_seconds":         7200,
							"logical_cpu_count":      8,
							"memory_total_bytes":     17179869184,
							"memory_available_bytes": 4294967296,
							"disk_total_bytes":       107374182400,
							"disk_available_bytes":   53687091200,
							"disk_mount":             "/",
						},
					},
				},
				{
					"id":                  "dev-test-2",
					"hostname":            "Debian-Server-NAS",
					"alias":               "存储服务器",
					"mac":                 "aa:bb:cc:dd:ee:ff",
					"agent_version":       "v0.7.0",
					"os":                  "linux",
					"arch":                "amd64",
					"ssh_user":            "root",
					"ssh_port":            2222,
					"addresses":           []string{"192.168.1.200"},
					"sync_status":         "synced",
					"applied_hash":        "a1b2c3d4e5f6",
					"sync_updated_at":     time.Now().Format(time.RFC3339),
					"github_sync_enabled": false,
					"github_status":       "disconnected",
					"connected":           true,
					"health": map[string]interface{}{
						"status": "degraded",
						"facts": map[string]interface{}{
							"observed_at":       time.Now().Format(time.RFC3339),
							"uptime_seconds":    3600,
							"logical_cpu_count": 4,
						},
						"reasons": []map[string]interface{}{
							{
								"code":     "disk_warning",
								"severity": "warning",
								"summary":  "磁盘剩余空间低于 15%",
							},
						},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(mockDevices)
	})

	mux.HandleFunc("/api/v1/onboarding/claim-token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"token":      "claim-token-123456",
			"expires_at": time.Now().Add(15 * time.Minute).Format(time.RFC3339),
		})
	})

	mux.HandleFunc("/api/v1/github/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"connected": true,
			"user": map[string]interface{}{
				"login":      "octocat",
				"avatar_url": "https://avatars.githubusercontent.com/u/583231",
			},
			"token_preview":        "ghp_test...",
			"synced_devices_count": 1,
			"total_devices_count":  2,
		})
	})

	mux.HandleFunc("/api/v1/commands", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"commands": []map[string]interface{}{
				{
					"id":            "cmd-001",
					"device_id":     "dev-test-1",
					"kind":          "ssh_keys",
					"status":        "succeeded",
					"created_at":    time.Now().Format(time.RFC3339),
					"error_message": "",
				},
				{
					"id":            "cmd-002",
					"device_id":     "dev-test-2",
					"kind":          "upgrade",
					"status":        "succeeded",
					"created_at":    time.Now().Format(time.RFC3339),
					"error_message": "",
				},
			},
		})
	})

	mux.HandleFunc("/api/v1/batch/sync", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"accepted":true}`))
	})

	mux.HandleFunc("/api/v1/batch/upgrade", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"accepted":true}`))
	})

	mux.HandleFunc("/api/v1/devices/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/health/events") {
			w.Write([]byte(`{"events":[]}`))
			return
		}
		w.Write([]byte(`{}`))
	})

	// Fallback to embedded UI
	mux.Handle("/static/", http.StripPrefix("/static/", uiHandler))
	mux.Handle("/", uiHandler)

	server := httptest.NewServer(mux)
	defer server.Close()

	cmd := exec.Command("node", "--test", "testdata/browser-layout.test.mjs")
	cmd.Env = append(os.Environ(), "TEST_SERVER_URL="+server.URL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser layout and accessibility tests failed: %v\n%s", err, output)
	}
}

func TestGetIndexHTML(t *testing.T) {
	content, err := GetIndexHTML()
	if err != nil {
		t.Fatalf("GetIndexHTML failed: %v", err)
	}

	html := string(content)
	requiredStrings := []string{
		"HomeAgent",
		"pageDashboard",
		"pageDevices",
		"pageOnboarding",
		"pageGithub",
		"pageSettings",
		"app.js",
		"style.css",
	}

	for _, s := range requiredStrings {
		if !strings.Contains(html, s) {
			t.Errorf("GetIndexHTML missing required content %q", s)
		}
	}

	// 验证设备列表筛选 Pill 契约：仅保留 all, healthy, degraded, synced, pending
	for _, expectedFilter := range []string{
		`data-filter="all"`,
		`data-filter="healthy"`,
		`data-filter="degraded"`,
		`data-filter="synced"`,
		`data-filter="pending"`,
	} {
		if !strings.Contains(html, expectedFilter) {
			t.Errorf("GetIndexHTML expected filter pill %s to be present", expectedFilter)
		}
	}

	// 负例断言：不得包含已移除的瞬时连接态筛选按钮
	for _, disallowedFilter := range []string{
		`data-filter="offline"`,
		`data-filter="online"`,
	} {
		if strings.Contains(html, disallowedFilter) {
			t.Errorf("GetIndexHTML contains disallowed filter pill %s", disallowedFilter)
		}
	}
}

func TestHandler(t *testing.T) {
	h := Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for /, got %d", rec.Code)
	}

	recCSS := httptest.NewRecorder()
	reqCSS := httptest.NewRequest("GET", "/style.css", nil)
	h.ServeHTTP(recCSS, reqCSS)
	if recCSS.Code != http.StatusOK {
		t.Errorf("expected 200 OK for /style.css, got %d", recCSS.Code)
	}
	cssBody := recCSS.Body.String()
	for _, expectedCSS := range []string{"@media (max-width: 640px)", "repeat(4, 1fr)", "safe-area-inset-bottom"} {
		if !strings.Contains(cssBody, expectedCSS) {
			t.Errorf("expected style.css to contain %q", expectedCSS)
		}
	}

	for _, path := range []string{
		"/app.js",
		"/js/app.js",
		"/js/state.js",
		"/js/utils.js",
		"/js/api.js",
		"/js/auth.js",
		"/js/router.js",
		"/js/onboarding.js",
		"/js/settings.js",
		"/js/modals.js",
		"/js/github.js",
		"/js/devices/render.js",
		"/js/devices/actions.js",
		"/js/idempotency.mjs",
	} {
		recJS := httptest.NewRecorder()
		reqJS := httptest.NewRequest("GET", path, nil)
		h.ServeHTTP(recJS, reqJS)
		if recJS.Code != http.StatusOK {
			t.Errorf("expected 200 OK for %s, got %d", path, recJS.Code)
		}
	}
}

// TestFrontendBackendFieldContracts 校验前后端全字段数据契约：
// 1. 通过 reflect 提取 device.Device 所有的 JSON 标签字段；
// 2. 检查前端所有 JS 模块中所有引用的 d.<property> 字段必须合法存在于后端模型；
// 3. 严格禁止出现未定义的野字段（如 d.version、d.hash）；
// 4. 验证所有核心业务字段在前端均有对应的处理或渲染绑定。
func TestFrontendBackendFieldContracts(t *testing.T) {
	var jsBuilder strings.Builder
	err := fs.WalkDir(staticFS, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".js") {
			content, err := staticFS.ReadFile(path)
			if err != nil {
				return err
			}
			jsBuilder.Write(content)
			jsBuilder.WriteString("\n")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to walk staticFS: %v", err)
	}
	jsContent := jsBuilder.String()

	// 1. 反射收集 Device 结构体的全部 JSON 字段
	devType := reflect.TypeOf(device.Device{})
	validFields := make(map[string]bool)
	for i := 0; i < devType.NumField(); i++ {
		field := devType.Field(i)
		jsonTag := field.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}
		parts := strings.Split(jsonTag, ",")
		fieldName := parts[0]
		validFields[fieldName] = true
	}

	// 允许已知的扩展 DTO 字段（如 connected 和 ddns_domain 别名）
	validFields["connected"] = true
	validFields["ddns_domain"] = true
	validFields["health"] = true
	validFields["status"] = true // MVP legacy status compatibility check

	// 2. 提取前端代码中所有形如 `d.<property>` 的访问
	propRegex := regexp.MustCompile(`\bd\.([a-zA-Z0-9_]+)`)
	matches := propRegex.FindAllStringSubmatch(jsContent, -1)

	accessedFields := make(map[string]int)
	for _, m := range matches {
		if len(m) > 1 {
			prop := m[1]
			accessedFields[prop]++
			if !validFields[prop] {
				t.Errorf("contract violation: frontend accesses unregistered property d.%s (not present in device.Device)", prop)
			}
		}
	}

	// 3. 严格拦截已知的历史笔误字段
	disallowedFields := []string{"version", "hash"}
	for _, df := range disallowedFields {
		pattern := `\bd\.` + df + `\b`
		if matched, _ := regexp.MatchString(pattern, jsContent); matched {
			t.Errorf("disallowed legacy/typo field d.%s found in frontend! Must use d.applied_%s instead", df, df)
		}
	}

	// 4. 断言核心业务字段必须在前端被正常处理或绑定
	essentialFields := []string{
		"id",
		"hostname",
		"alias",
		"mac",
		"agent_version",
		"os",
		"arch",
		"ssh_user",
		"ssh_port",
		"addresses",
		"updated_at",
		"sync_status",
		"applied_hash",
		"github_sync_enabled",
		"github_status",
		"connected",
	}

	for _, ef := range essentialFields {
		if accessedFields[ef] == 0 {
			t.Errorf("missing essential contract field: d.%s is not referenced or handled in frontend", ef)
		}
	}

	// 5. 校验前端辅助函数的存在性与安全性
	for _, fn := range []string{"function getOSInfo", "function getOSIcon", "function sanitizeHost"} {
		if !strings.Contains(jsContent, fn) {
			t.Errorf("expected frontend to define helper function %q", fn)
		}
	}
}

func TestSyncHashPresentation(t *testing.T) {
	index, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	render, err := staticFS.ReadFile("static/js/devices/render.js")
	if err != nil {
		t.Fatal(err)
	}
	actions, err := staticFS.ReadFile("static/js/devices/actions.js")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(index), "服务端配置 Hash") {
		t.Fatal("dashboard must label the server configuration hash")
	}
	if strings.Contains(string(index), "最新同步版本") || strings.Contains(string(render), "同步版本 / Hash") {
		t.Fatal("sync version must not be presented in the web UI")
	}
	for _, expected := range []string{"state.serverHash", "同步 Hash", "d.applied_hash", "上次同步"} {
		if !strings.Contains(string(render), expected) {
			t.Fatalf("device rendering missing %q", expected)
		}
	}
	if !strings.Contains(string(render), "formatRelativeTime(d.sync_updated_at)") {
		t.Fatal("device sync time must come from sync_updated_at without an online-time fallback")
	}
	if !strings.Contains(string(actions), "data.server_hash") {
		t.Fatal("device fetch must store the server hash returned by the API")
	}
}

// TestESMModuleImportsResolve 静态分析所有 ESM 模块的 import / export from 路径，
// 确保所有相对路径引用在嵌入的文件系统中真实有效，杜绝运行时 404。
func TestESMModuleImportsResolve(t *testing.T) {
	importRegex := regexp.MustCompile(`(?:import|export).*?from\s+['"]([^'"]+)['"]|(?:import\s+['"]([^'"]+)['"])`)

	err := fs.WalkDir(staticFS, "static", func(filePath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || (!strings.HasSuffix(filePath, ".js") && !strings.HasSuffix(filePath, ".mjs")) {
			return nil
		}

		content, err := staticFS.ReadFile(filePath)
		if err != nil {
			t.Fatalf("failed to read %s: %v", filePath, err)
		}

		dir := path.Dir(filePath)
		matches := importRegex.FindAllStringSubmatch(string(content), -1)
		for _, m := range matches {
			relImport := m[1]
			if relImport == "" {
				relImport = m[2]
			}
			if relImport == "" {
				continue
			}

			// 只校验相对路径导入（如 ./state.js, ../utils.js）
			if strings.HasPrefix(relImport, ".") {
				targetPath := path.Clean(path.Join(dir, relImport))
				if _, err := staticFS.Open(targetPath); err != nil {
					t.Errorf("broken ESM import in %s: cannot resolve %q (resolved to %q): %v", filePath, relImport, targetPath, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}
}
