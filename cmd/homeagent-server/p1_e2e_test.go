package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"homeagent/internal/alerting"
	"homeagent/internal/alerting/webhook"
	"homeagent/internal/api"
	"homeagent/internal/auth"
	"homeagent/internal/broker"
	"homeagent/internal/command"
	commandfile "homeagent/internal/command/file"
	"homeagent/internal/device"
	"homeagent/internal/devicestate"
	"homeagent/internal/health"
	"homeagent/internal/prefixstate"
	"homeagent/internal/registry"
	"homeagent/internal/version"
)

// 1. 上一正式版本 Agent (无 runtime 字段) -> 候选 Server：旧 Facts 通过真实 HTTP 请求发送并正常接收，运行指标安全为空，不误报 degraded。
func TestP1_CrossVersion_LegacyAgentToCandidateServer(t *testing.T) {
	tempDir := t.TempDir()
	r, err := registry.Open(filepath.Join(tempDir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}

	devToken := "legacy-device-token-12345"
	d := device.Device{
		ID:              "dev-legacy-1",
		Hostname:        "legacy-node",
		AgentVersion:    "v0.5.4",
		OS:              "linux",
		Arch:            "amd64",
		SSHUser:         "root",
		SSHPort:         22,
		PublicKey:       "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILegacyMockKey12345678901234567890",
		DeviceTokenHash: auth.HashToken(devToken),
		LastSeenAt:      time.Now().UTC(),
	}
	if _, err := r.Save(d); err != nil {
		t.Fatal(err)
	}

	healthRepo, _ := health.NewFileRepository(filepath.Join(tempDir, "health"))
	adapters := &serverHealthAdapters{
		reg: r,
	}
	healthSvc := health.NewService(health.ServiceConfig{
		Config:    health.DefaultEvaluatorConfig(),
		Repo:      healthRepo,
		FactsPort: adapters,
	})

	server := &api.Server{
		Registry: r,
		Token:    "admin-token",
		Health:   healthSvc,
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	// 通过真实 HTTP 客户端发送不含 runtime 的 PUT /api/v1/devices/dev-legacy-1/facts
	legacyPayload := map[string]any{
		"hostname":      "legacy-node",
		"agent_version": "v0.5.4",
		"os":            "linux",
		"arch":          "amd64",
		"ssh_user":      "root",
		"ssh_port":      22,
		"addresses":     []string{"192.168.1.100"},
	}
	body, _ := json.Marshal(legacyPayload)

	req, _ := http.NewRequest("PUT", httpServer.URL+"/api/v1/devices/dev-legacy-1/facts", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+devToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("real http put facts failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 OK from server, got %d: %s", resp.StatusCode, string(respBytes))
	}

	// 评估健康状态：不包含 runtime 的旧 Agent 应当为 healthy，不因缺少运行指标而 degraded
	snap, err := healthSvc.EvaluateDevice(context.Background(), "dev-legacy-1")
	if err != nil {
		t.Fatalf("evaluate device failed: %v", err)
	}
	if snap.Status != health.StatusHealthy {
		t.Fatalf("legacy agent should be evaluated as healthy, got: %s", snap.Status)
	}
}

// TestP1_CrossVersion_v062FactsToCandidateServer verifies the frozen v0.6.2
// platform payloads over a real HTTP listener and candidate Registry.
func TestP1_CrossVersion_v062FactsToCandidateServer(t *testing.T) {
	tests := []struct {
		name    string
		os      string
		runtime map[string]any
	}{
		{name: "windows", os: "windows", runtime: map[string]any{"observed_at": time.Now().UTC(), "logical_cpu_count": 8, "disk_mount": "C:\\"}},
		{name: "darwin", os: "darwin", runtime: map[string]any{"observed_at": time.Now().UTC(), "logical_cpu_count": 8, "load_1": 1.25, "memory_total_bytes": uint64(16 << 30), "memory_available_bytes": uint64(8 << 30), "disk_mount": "/"}},
		{name: "linux", os: "linux", runtime: map[string]any{"observed_at": time.Now().UTC(), "uptime_seconds": 86400, "logical_cpu_count": 4, "load_1": 0.5, "memory_total_bytes": uint64(8 << 30), "memory_available_bytes": uint64(4 << 30), "disk_mount": "/"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			reg, err := registry.Open(filepath.Join(tempDir, "devices.json"))
			if err != nil {
				t.Fatal(err)
			}
			deviceID := "dev-v062-" + tt.name
			deviceToken := "token-v062-" + tt.name
			if _, err := reg.Save(device.Device{
				ID:              deviceID,
				Hostname:        "v062-" + tt.name,
				OS:              tt.os,
				Arch:            "amd64",
				SSHUser:         "user",
				SSHPort:         22,
				PublicKey:       "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILegacyMockKey12345678901234567890",
				DeviceTokenHash: auth.HashToken(deviceToken),
			}); err != nil {
				t.Fatal(err)
			}

			server := httptest.NewServer((&api.Server{Registry: reg, Token: "admin-token"}).Handler())
			defer server.Close()

			payload := map[string]any{
				"hostname": "v062-" + tt.name, "agent_version": "v0.6.2", "os": tt.os, "arch": "amd64",
				"ssh_user": "user", "ssh_port": 22, "addresses": []string{"192.0.2.10"}, "runtime": tt.runtime,
			}
			body, _ := json.Marshal(payload)
			req, _ := http.NewRequest(http.MethodPut, server.URL+"/api/v1/devices/"+deviceID+"/facts", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+deviceToken)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				responseBody, _ := io.ReadAll(resp.Body)
				t.Fatalf("v0.6.2 %s facts status=%d body=%s", tt.name, resp.StatusCode, responseBody)
			}

			saved, err := reg.Get(deviceID)
			if err != nil {
				t.Fatal(err)
			}
			wantCPU := tt.runtime["logical_cpu_count"].(int)
			if saved.AgentVersion != "v0.6.2" || saved.RuntimeFacts == nil || saved.RuntimeFacts.LogicalCPUCount != wantCPU {
				t.Fatalf("unexpected persisted v0.6.2 %s facts: %+v", tt.name, saved)
			}
			if tt.os != "linux" && saved.RuntimeFacts.UptimeSeconds != 0 {
				t.Fatalf("v0.6.2 %s uptime must remain unknown, got %d", tt.name, saved.RuntimeFacts.UptimeSeconds)
			}
		})
	}
}

// 2. 真实网络协议 Webhook 接收端端到端闭环验证：真实 TCP 监听、HMAC-SHA256 校验、firing 去重与 resolved 恢复
func TestP1_EndToEnd_HealthAlertingWebhookLifecycle(t *testing.T) {
	tempDir := t.TempDir()
	r, _ := registry.Open(filepath.Join(tempDir, "devices.json"))
	b := broker.New()

	baseTime := time.Now().UTC()
	mockNow := baseTime

	d := device.Device{
		ID:           "dev-e2e-1",
		Hostname:     "e2e-node",
		AgentVersion: "v0.6.0",
		OS:           "linux",
		Arch:         "amd64",
		SSHUser:      "root",
		SSHPort:      22,
		PublicKey:    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE2EMockKey12345678901234567890",
		LastSeenAt:   baseTime,
		RuntimeFacts: &device.RuntimeFacts{
			ObservedAt:           baseTime,
			DiskTotalBytes:       100 * 1024 * 1024 * 1024,
			DiskAvailableBytes:   50 * 1024 * 1024 * 1024,
			MemoryTotalBytes:     16 * 1024 * 1024 * 1024,
			MemoryAvailableBytes: 8 * 1024 * 1024 * 1024,
		},
	}
	_, _ = r.Save(d)

	secret := "test-secret-key-32-bytes-long-for-hmac-sha256"

	var mu sync.Mutex
	var deliveredEvents []string
	var deliveredNotifications []alerting.Notification

	// 真实 Webhook HTTP 接收服务器 (监听真实 TCP loopback)
	webhookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ev := req.Header.Get("X-HomeAgent-Event")
		sig := req.Header.Get("X-HomeAgent-Signature")
		ts := req.Header.Get("X-HomeAgent-Timestamp")
		bodyBytes, _ := io.ReadAll(req.Body)

		expectedSig := webhook.ComputeSignature(secret, ts, bodyBytes)
		if sig != expectedSig {
			http.Error(w, "unauthorized signature", http.StatusUnauthorized)
			return
		}

		var n alerting.Notification
		if err := json.Unmarshal(bodyBytes, &n); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}

		mu.Lock()
		deliveredEvents = append(deliveredEvents, ev)
		deliveredNotifications = append(deliveredNotifications, n)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"received"}`))
	}))
	defer webhookServer.Close()

	whChannel, err := webhook.NewChannel(webhook.Config{
		ID:        "wh-e2e-real",
		URL:       webhookServer.URL,
		Secret:    secret,
		Timeout:   2 * time.Second,
		AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	alertRepo, _ := alerting.NewFileRepository(filepath.Join(tempDir, "alerting"))
	alertingSvc := alerting.NewService(alerting.ServiceConfig{
		Repo:         alertRepo,
		NameResolver: &serverNameResolver{reg: r},
		BackoffSchedule: func(attemptNum int, retryAfter time.Duration) time.Duration {
			return 10 * time.Millisecond
		},
	})
	alertingSvc.RegisterChannel(whChannel)

	healthRepo, _ := health.NewFileRepository(filepath.Join(tempDir, "health"))
	adapters := &serverHealthAdapters{
		reg:    r,
		broker: b,
	}

	cfg := health.DefaultEvaluatorConfig()
	cfg.StaleAfter = 15 * time.Minute
	cfg.OfflineAfter = 30 * time.Minute
	cfg.OfflinePendingFor = 5 * time.Minute

	healthSvc := health.NewService(health.ServiceConfig{
		Config:    cfg,
		Repo:      healthRepo,
		FactsPort: adapters,
		Clock:     health.ClockFunc(func() time.Time { return mockNow }),
	})
	healthSvc.RegisterListener(func(ctx context.Context, events []health.HealthEvent) {
		alertingSvc.HandleHealthEvents(ctx, events)
	})

	ctx := context.Background()

	// Step 1: 初始评估 (Healthy)
	snap1, _ := healthSvc.EvaluateDevice(ctx, "dev-e2e-1")
	if snap1.Status != health.StatusHealthy {
		t.Fatalf("expected healthy, got %s", snap1.Status)
	}
	mu.Lock()
	if len(deliveredEvents) != 0 {
		t.Fatalf("healthy status should not trigger webhook alerts")
	}
	mu.Unlock()

	// Step 2: 模拟时间前进 40 分钟 -> 设备在 40m 前离线，触发 Offline
	mockNow = baseTime.Add(40 * time.Minute)

	snap2, _ := healthSvc.EvaluateDevice(ctx, "dev-e2e-1")
	if snap2.Status != health.StatusOffline {
		t.Fatalf("expected offline, got %s", snap2.Status)
	}

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	if len(deliveredEvents) != 1 || deliveredEvents[0] != "alert.firing" {
		t.Fatalf("expected 1 alert.firing notification, got %v", deliveredEvents)
	}
	if deliveredNotifications[0].Alert.ReasonCode != "device_offline" {
		t.Fatalf("expected device_offline alert reason, got %s", deliveredNotifications[0].Alert.ReasonCode)
	}
	if deliveredNotifications[0].Device.HealthStatus != health.StatusOffline {
		t.Fatalf("expected device health status to be offline, got: %s", deliveredNotifications[0].Device.HealthStatus)
	}
	mu.Unlock()

	// Step 3: 重复求值 -> 验证去重，不重复产生 firing
	snap3, _ := healthSvc.EvaluateDevice(ctx, "dev-e2e-1")
	if snap3.Status != health.StatusOffline {
		t.Fatalf("expected still offline, got %s", snap3.Status)
	}
	mu.Lock()
	if len(deliveredEvents) != 1 {
		t.Fatalf("duplicate evaluation must not re-dispatch firing alert, count: %d", len(deliveredEvents))
	}
	mu.Unlock()

	// Step 4: 设备重新上线上报 -> 恢复 Healthy 并触发 alert.resolved
	_ = r.TouchLastSeen("dev-e2e-1")
	mockNow = time.Now().UTC()

	snap4, _ := healthSvc.EvaluateDevice(ctx, "dev-e2e-1")
	if snap4.Status != health.StatusHealthy {
		t.Fatalf("expected recovered healthy, got %s", snap4.Status)
	}

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	if len(deliveredEvents) != 2 || deliveredEvents[1] != "alert.resolved" {
		t.Fatalf("expected 2nd event to be alert.resolved, got %v", deliveredEvents)
	}
	if deliveredNotifications[1].Device.HealthStatus != health.StatusHealthy {
		t.Fatalf("expected resolved notification to show healthy status, got: %s", deliveredNotifications[1].Device.HealthStatus)
	}
	mu.Unlock()
}

// 3. 服务端重启恢复测试：验证重启后持久化状态正确保留且不产生告警风暴
func TestP1_ServerRestart_PreservesFiringStateAndNoDuplicateFiring(t *testing.T) {
	tempDir := t.TempDir()
	alertDir := filepath.Join(tempDir, "alerting")
	repo1, _ := alerting.NewFileRepository(alertDir)

	now := time.Now().UTC()
	alert := alerting.Alert{
		ID:                   "alt_disk_123",
		Fingerprint:          alerting.ComputeFingerprint("dev-restart-1", "disk_space_low"),
		DeviceID:             "dev-restart-1",
		ReasonCode:           "disk_space_low",
		Severity:             health.SeverityWarning,
		State:                alerting.AlertFiring,
		OpenedAt:             now.Add(-1 * time.Hour),
		LastObservedAt:       now.Add(-10 * time.Minute),
		Summary:              "磁盘空间不足",
		FiringNotified:       true,
		NotificationRevision: 1,
	}
	_ = repo1.SaveAlert(context.Background(), alert)

	// 重启 Server：重新初始化 Repo 和 Service
	repo2, err := alerting.NewFileRepository(alertDir)
	if err != nil {
		t.Fatalf("failed to reload repo on restart: %v", err)
	}

	var mu sync.Mutex
	var delivered []string
	realReceiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		delivered = append(delivered, r.Header.Get("X-HomeAgent-Event"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer realReceiver.Close()

	wh, err := webhook.NewChannel(webhook.Config{
		ID:        "wh-test",
		URL:       realReceiver.URL,
		Secret:    "secret-32-bytes-long-key-for-test-restart",
		Timeout:   time.Second,
		AllowHTTP: true,
	})
	if err != nil {
		t.Fatalf("failed to create webhook channel: %v", err)
	}

	svc2 := alerting.NewService(alerting.ServiceConfig{
		Repo: repo2,
	})
	svc2.RegisterChannel(wh)
	_ = svc2.RecoverPendingRetries(context.Background())

	// 重启后收到相同的持续 health event (changed / opened)
	ev := health.HealthEvent{
		ID:         "hevt-1",
		DeviceID:   "dev-restart-1",
		Type:       "changed",
		ReasonCode: "disk_space_low",
		Severity:   health.SeverityWarning,
		OccurredAt: now,
	}

	svc2.HandleHealthEvents(context.Background(), []health.HealthEvent{ev})

	// 确认：重启后持续故障不会引发告警风暴或重复发送 firing
	mu.Lock()
	if len(delivered) != 0 {
		t.Fatalf("restarting server should not duplicate firing alert for already notified issue, got %v", delivered)
	}
	mu.Unlock()

	// 验证恢复事件仍能正确关联原 Alert ID
	evResolved := health.HealthEvent{
		ID:         "hevt-2",
		DeviceID:   "dev-restart-1",
		Type:       "resolved",
		ReasonCode: "disk_space_low",
		Severity:   health.SeverityWarning,
		OccurredAt: now.Add(5 * time.Minute),
	}
	svc2.HandleHealthEvents(context.Background(), []health.HealthEvent{evResolved})

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	if len(delivered) != 1 || delivered[0] != "alert.resolved" {
		t.Fatalf("expected resolved notification to be dispatched, got %v", delivered)
	}
	mu.Unlock()

	storedAlert, _ := repo2.GetAlert(context.Background(), "alt_disk_123")
	if storedAlert == nil || storedAlert.State != alerting.AlertResolved || storedAlert.ResolvedAt == nil {
		t.Fatalf("expected original alert to be marked resolved, got %+v", storedAlert)
	}
}

// 4. 关键反例测试：未授权 3xx 重定向安全拒绝、401 签名防伪、429 有限重试与错误分类
func TestP1_Webhook_SecurityAndRetryAntiPatterns(t *testing.T) {
	secret := "my-secret-key-at-least-32-bytes-length"

	// 反例 1: 3xx 重定向安全拒绝 (真实 HTTP 服务器返回 301 Location)
	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://unauthorized-domain.com/leaked", http.StatusMovedPermanently)
	}))
	defer redirectServer.Close()

	chRedir, _ := webhook.NewChannel(webhook.Config{
		ID:        "ch-redir",
		URL:       redirectServer.URL,
		Secret:    secret,
		Timeout:   time.Second,
		AllowHTTP: true,
	})
	resRedir := chRedir.Deliver(context.Background(), alerting.Notification{Event: "alert.firing"})
	if resRedir.StatusCode != http.StatusMovedPermanently || resRedir.Retryable || resRedir.ErrorCode != "redirect_rejected" {
		t.Fatalf("expected redirect to be safely rejected without following, got %+v", resRedir)
	}

	// 反例 2: 签名错误安全失败 (真实 HTTP 接收方校验签名失败返回 401)
	authFailServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized signature mismatch", http.StatusUnauthorized)
	}))
	defer authFailServer.Close()

	chAuth, _ := webhook.NewChannel(webhook.Config{
		ID:        "ch-auth",
		URL:       authFailServer.URL,
		Secret:    secret,
		Timeout:   time.Second,
		AllowHTTP: true,
	})
	resAuth := chAuth.Deliver(context.Background(), alerting.Notification{Event: "alert.firing"})
	if resAuth.StatusCode != http.StatusUnauthorized || resAuth.Retryable {
		t.Fatalf("expected 401 to be classified as non-retryable config error, got %+v", resAuth)
	}

	// 反例 3: 429 限流带 Retry-After 响应头正确分类为 Retryable (真实 HTTP 响应)
	rateLimitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("too many requests"))
	}))
	defer rateLimitServer.Close()

	chRate, _ := webhook.NewChannel(webhook.Config{
		ID:        "ch-rate",
		URL:       rateLimitServer.URL,
		Secret:    secret,
		Timeout:   time.Second,
		AllowHTTP: true,
	})
	resRate := chRate.Deliver(context.Background(), alerting.Notification{Event: "alert.firing"})
	if resRate.StatusCode != http.StatusTooManyRequests || !resRate.Retryable || resRate.RetryAfter != 30*time.Second {
		t.Fatalf("expected 429 with 30s Retry-After to be classified as Retryable, got %+v", resRate)
	}
}

func TestP1_RealBinaryUpgrade_v054_ToCandidate(t *testing.T) {
	testP1RealBinaryUpgradeToCandidate(t, "70e41fb", "v0.5.4")
}

// TestP1_RealBinaryUpgrade_v062_ToCandidate freezes the current task's
// maintainer-confirmed formal baseline at commit 38c5e0c / v0.6.2.
func TestP1_RealBinaryUpgrade_v062_ToCandidate(t *testing.T) {
	testP1RealBinaryUpgradeToCandidate(t, "38c5e0c", "v0.6.2")
}

// TestP1_RealBinaryUpgrade_v063_ToCandidate freezes the current task's
// maintainer-confirmed formal baseline at commit cf3932b / v0.6.3.
func TestP1_RealBinaryUpgrade_v063_ToCandidate(t *testing.T) {
	testP1RealBinaryUpgradeToCandidate(t, "cf3932b", "v0.6.3")
}

// testP1RealBinaryUpgradeToCandidate runs a real baseline process, dispatches
// the public upgrade command over HTTP, verifies atomic replacement, and then
// starts the candidate process to observe its autonomous Facts report.
func testP1RealBinaryUpgradeToCandidate(t *testing.T, baselineCommit, baselineVersion string) {
	if testing.Short() {
		t.Skip("skipping real binary upgrade test in short mode")
	}
	candidateVersion := version.Get()

	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, baselineVersion+"-src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 1. 从冻结的 Git 提交提取源码并编译真实基线二进制制品。
	checkCmd := exec.Command("git", "-C", "../..", "cat-file", "-e", baselineCommit+"^{commit}")
	checkCmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if err := checkCmd.Run(); err != nil {
		t.Skipf("skipping real binary upgrade test: baseline commit %s not present in repository", baselineCommit)
	}

	archiveCmd := exec.Command("git", "-C", "../..", "archive", baselineCommit)
	archiveCmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	tarCmd := exec.Command("tar", "-x", "-C", srcDir)
	r, w := io.Pipe()
	archiveCmd.Stdout = w
	tarCmd.Stdin = r
	if err := archiveCmd.Start(); err != nil {
		t.Fatalf("git archive failed: %v", err)
	}
	if err := tarCmd.Start(); err != nil {
		t.Fatalf("tar start failed: %v", err)
	}
	_ = archiveCmd.Wait()
	_ = w.Close()
	if err := tarCmd.Wait(); err != nil {
		t.Fatalf("tar extract failed: %v", err)
	}

	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	targetExe := filepath.Join(binDir, "homeagent-agent")
	buildBaseline := exec.Command("go", "build", "-o", targetExe, "./cmd/homeagent-agent")
	buildBaseline.Dir = srcDir
	if out, err := buildBaseline.CombinedOutput(); err != nil {
		t.Fatalf("build %s binary failed: %v, output: %s", baselineVersion, err, string(out))
	}

	// 验证初始二进制执行 info 输出确实为冻结的基线版本。
	baselineInfo, err := exec.Command(targetExe, "info").CombinedOutput()
	if err != nil {
		t.Fatalf("%s info command failed: %v, output: %s", baselineVersion, err, string(baselineInfo))
	}
	if !strings.Contains(string(baselineInfo), `"agent_version":"`+baselineVersion+`"`) {
		t.Fatalf("expected %s in info output, got: %s", baselineVersion, string(baselineInfo))
	}
	var devInfo map[string]any
	if err := json.Unmarshal(baselineInfo, &devInfo); err != nil {
		t.Fatalf("unmarshal %s info failed: %v", baselineVersion, err)
	}
	devID := devInfo["id"].(string)
	pubKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKxGyiG2Z6ev9ku+gjk4d2qt70STlaT1pT1lAFuw7MXQ"
	if pk, ok := devInfo["public_key"].(string); ok && pk != "" {
		pubKey = pk
	}

	// 2. 编译当前分支的候选版本 Agent 二进制并启动本地 HTTP 文件服务
	candDir := filepath.Join(tempDir, "candidate")
	if err := os.MkdirAll(candDir, 0755); err != nil {
		t.Fatal(err)
	}
	candidateBin := filepath.Join(candDir, "homeagent-agent")
	buildCandidate := exec.Command("go", "build", "-o", candidateBin, "homeagent/cmd/homeagent-agent")
	if out, err := buildCandidate.CombinedOutput(); err != nil {
		t.Fatalf("build %s candidate binary failed: %v, output: %s", candidateVersion, err, string(out))
	}

	candidateBytes, err := os.ReadFile(candidateBin)
	if err != nil {
		t.Fatal(err)
	}
	hasher := sha256.New()
	hasher.Write(candidateBytes)
	candidateSHA := hex.EncodeToString(hasher.Sum(nil))

	downloadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(candidateBytes)
	}))
	defer downloadServer.Close()

	// 3. 启动 Server 控制平面（集成 Registry, Broker, Command Engine, Health Service）
	reg, _ := registry.Open(filepath.Join(tempDir, "devices.json"))
	devToken := "dev-token-secret-12345"
	if _, err := reg.Save(device.Device{
		ID:               devID,
		Hostname:         devInfo["hostname"].(string),
		AgentVersion:     baselineVersion,
		OS:               devInfo["os"].(string),
		Arch:             devInfo["arch"].(string),
		SSHUser:          "root",
		SSHPort:          22,
		PublicKey:        pubKey,
		DeviceTokenHash:  auth.HashToken(devToken),
		ControlProtocols: []int{1},
		LastSeenAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("reg.Save device failed: %v", err)
	}

	cmdRepo, _ := commandfile.Open(filepath.Join(tempDir, "commands.json"))
	cmdSvc := command.NewService(cmdRepo, nil)
	b := broker.New()
	healthRepo, _ := health.NewFileRepository(filepath.Join(tempDir, "health"))
	adapters := &serverHealthAdapters{reg: reg}
	healthSvc := health.NewService(health.ServiceConfig{
		Config:    health.DefaultEvaluatorConfig(),
		Repo:      healthRepo,
		FactsPort: adapters,
	})
	apiServer := &api.Server{
		Registry: reg,
		Broker:   b,
		Token:    "admin-token",
		Commands: cmdSvc,
		Health:   healthSvc,
	}
	server := httptest.NewServer(apiServer.Handler())
	defer server.Close()

	cfgPath := filepath.Join(tempDir, "device.json")
	authKeysPath := filepath.Join(tempDir, "authorized_keys")
	_ = os.WriteFile(authKeysPath, []byte(""), 0600)
	devCfgData := map[string]any{
		"server_url":   server.URL,
		"device_id":    devID,
		"device_token": devToken,
	}
	cfgBytes, _ := json.MarshalIndent(devCfgData, "", "  ")
	_ = os.WriteFile(cfgPath, cfgBytes, 0600)

	// 4. 以独立操作系统子进程形式启动真实基线 Agent daemon 守护进程。
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	agentCmd1 := exec.CommandContext(ctx1, targetExe, "daemon",
		"--server="+server.URL,
		"--token="+devToken,
		"--device-id="+devID,
		"--config="+cfgPath,
		"--authorized-keys="+authKeysPath,
		"--ipv6-report=false",
	)
	if err := agentCmd1.Start(); err != nil {
		t.Fatalf("start %s agent daemon failed: %v", baselineVersion, err)
	}

	// 等待基线 Agent 建立 SSE 长连接。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b.IsConnected(devID) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !b.IsConnected(devID) {
		t.Fatalf("timed out waiting for %s agent to establish SSE connection to server", baselineVersion)
	}

	// 5. 服务端通过真实管理 API 发起升级指令 (POST /api/v1/devices/{id}/upgrade)
	upgradeReq := map[string]any{
		"target_version": candidateVersion,
		"url":            downloadServer.URL + "/homeagent-agent",
		"sha256":         candidateSHA,
		"force":          true,
	}
	reqBody, _ := json.Marshal(upgradeReq)
	req, _ := http.NewRequest("POST", server.URL+"/api/v1/devices/"+devID+"/upgrade", bytes.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("dispatch upgrade request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 OK from upgrade API, got %d: %s", resp.StatusCode, string(body))
	}

	// 6. 等待基线 Agent 进程自行完成下载、冒烟、就地替换并退出。
	doneChan := make(chan error, 1)
	go func() {
		doneChan <- agentCmd1.Wait()
	}()
	select {
	case err := <-doneChan:
		if err != nil {
			t.Logf("agent %s process exited: %v", baselineVersion, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s agent to finish self-upgrade and exit", baselineVersion)
	}

	// 7. 验证磁盘上的 targetExe 文件已完成原子就地替换，执行 info 验证版本跃迁为当前候选版本
	upgradedInfo, err := exec.Command(targetExe, "info").CombinedOutput()
	if err != nil {
		t.Fatalf("upgraded binary info execution failed: %v, output: %s", err, string(upgradedInfo))
	}
	if !strings.Contains(string(upgradedInfo), `"agent_version":"`+candidateVersion+`"`) {
		t.Fatalf("expected upgraded binary on disk to report %s, got: %s", candidateVersion, string(upgradedInfo))
	}

	// 8. 启动就地替换后的候选 Agent 守护进程，验证其自主采集并上报 RuntimeFacts
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	agentCmd2 := exec.CommandContext(ctx2, targetExe, "daemon",
		"--server="+server.URL,
		"--token="+devToken,
		"--device-id="+devID,
		"--config="+cfgPath,
		"--authorized-keys="+authKeysPath,
		"--ipv6-report=false",
	)
	if err := agentCmd2.Start(); err != nil {
		t.Fatalf("start upgraded %s agent daemon failed: %v", candidateVersion, err)
	}

	// 等待并断言服务端自动接收到候选 Agent 上报的 RuntimeFacts
	var updatedDev device.Device
	factDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(factDeadline) {
		d, err := reg.Get(devID)
		if err == nil && d.AgentVersion == candidateVersion && d.RuntimeFacts != nil {
			updatedDev = d
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel2()
	_ = agentCmd2.Wait()

	if updatedDev.RuntimeFacts == nil {
		t.Fatalf("timed out waiting for upgraded %s agent to automatically report RuntimeFacts to server", candidateVersion)
	}
	if updatedDev.RuntimeFacts.MemoryTotalBytes == 0 {
		t.Fatalf("expected non-zero MemoryTotalBytes reported by real %s agent, got %+v", candidateVersion, updatedDev.RuntimeFacts)
	}

	// 9. 服务端对升级并上报真实 RuntimeFacts 的设备执行健康评估
	snap, err := healthSvc.EvaluateDevice(context.Background(), devID)
	if err != nil {
		t.Fatalf("EvaluateDevice failed on upgraded device: %v", err)
	}
	if snap.Status != health.StatusHealthy {
		t.Fatalf("expected evaluated status Healthy for upgraded agent, got %s (reasons: %+v)", snap.Status, snap.Reasons)
	}
}

// 4. 根因回归链：旧行为 (无持久化重启 -> 409 -> LastSeenAt 不刷新 -> 16m 后 ddns_prefix_stale)
//    新行为 (持久化递增 -> 200 -> LastSeenAt 刷新 -> 保持 Healthy)
//    恢复行为 (本地回退 -> 409 结构化恢复 -> 有界追赶成功 -> 保持 Healthy)
func TestP1_RootCauseRegression_PersistentRevisionsAndDDNSHealth(t *testing.T) {
	tempDir := t.TempDir()
	reg, err := registry.Open(filepath.Join(tempDir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}

	routerID := "router-root-cause"
	routerToken := "router-tok-123"
	nodeID := "node-root-cause"
	nodeToken := "node-tok-123"

	t0 := time.Now().UTC()
	mockNow := t0

	if _, err := reg.Save(device.Device{
		ID:              routerID,
		Hostname:        "openwrt-router",
		OS:              "linux",
		Arch:            "mipsle",
		AgentVersion:    "v0.6.3",
		SSHUser:         "root",
		SSHPort:         22,
		PublicKey:       "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIRouterKey1234567890",
		DeviceTokenHash: auth.HashToken(routerToken),
		LastSeenAt:      t0,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := reg.Save(device.Device{
		ID:              nodeID,
		Hostname:        "windows-pc",
		OS:              "windows",
		Arch:            "amd64",
		AgentVersion:    "v0.6.3",
		SSHUser:         "root",
		SSHPort:         22,
		PublicKey:       "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINodeKey1234567890",
		DeviceTokenHash: auth.HashToken(nodeToken),
		LastSeenAt:      t0,
	}); err != nil {
		t.Fatal(err)
	}

	prefixStore := prefixstate.NewMemoryStore()
	prefixSvc := prefixstate.NewService(prefixStore)
	devStateSvc := devicestate.NewService(nil)
	healthRepo, _ := health.NewFileRepository(filepath.Join(tempDir, "health"))
	adapters := &serverHealthAdapters{
		reg:         reg,
		devState:    devStateSvc,
		prefixState: prefixSvc,
	}

	healthSvc := health.NewService(health.ServiceConfig{
		Config:    health.DefaultEvaluatorConfig(),
		Repo:      healthRepo,
		FactsPort: adapters,
		DDNSPort:  adapters,
		Clock:     health.ClockFunc(func() time.Time { return mockNow }),
	})

	sm, _ := auth.NewSessionManager("")
	apiServer := &api.Server{
		Registry:           reg,
		SessionManager:     sm,
		Token:              "admin-token",
		DeviceStateService: devStateSvc,
		PrefixStateService: prefixSvc,
		Health:             healthSvc,
	}
	server := httptest.NewServer(apiServer.Handler())
	defer server.Close()

	// 初始状态：普通节点上报 IPv6 地址 2001:db8:1::10
	nodePayload := map[string]any{
		"network_id":  "home",
		"revision":    1,
		"observed_at": t0,
		"ipv6_addresses": []map[string]any{
			{"address": "2001:db8:1::10", "interface": "Ethernet", "temporary": false, "deprecated": false},
		},
	}
	nodeBody, _ := json.Marshal(nodePayload)
	reqNode, _ := http.NewRequest(http.MethodPut, server.URL+"/api/v1/devices/"+nodeID+"/network-state", bytes.NewReader(nodeBody))
	reqNode.Header.Set("Authorization", "Bearer "+nodeToken)
	reqNode.Header.Set("Content-Type", "application/json")
	respNode, err := http.DefaultClient.Do(reqNode)
	if err != nil || respNode.StatusCode != http.StatusOK {
		t.Fatalf("node put network-state failed: resp=%v err=%v", respNode, err)
	}
	_ = respNode.Body.Close()

	// Step 1 (旧行为因果链验证):
	// 1.1 路由器以 revision 9 首次上报前缀 2001:db8:1::/64
	p1Payload := map[string]any{
		"network_id":  "home",
		"revision":    9,
		"observed_at": t0,
		"prefixes": []map[string]any{
			{"prefix": "2001:db8:1::/64"},
		},
	}
	p1Body, _ := json.Marshal(p1Payload)
	reqP1, _ := http.NewRequest(http.MethodPut, server.URL+"/api/v1/devices/"+routerID+"/network-prefixes", bytes.NewReader(p1Body))
	reqP1.Header.Set("Authorization", "Bearer "+routerToken)
	reqP1.Header.Set("Content-Type", "application/json")
	respP1, err := http.DefaultClient.Do(reqP1)
	if err != nil || respP1.StatusCode != http.StatusOK {
		t.Fatalf("router put network-prefixes rev 9 failed: resp=%v err=%v", respP1, err)
	}
	_ = respP1.Body.Close()

	// 1.2 模拟时间推进：前缀状态在 16 分钟前记录
	ps, _ := prefixSvc.GetByNetwork("home")
	ps.LastSeenAt = time.Now().UTC().Add(-16 * time.Minute)
	_ = prefixStore.Save(*ps)
	mockNow = time.Now().UTC()
	_ = reg.TouchLastSeen(nodeID)
	_ = reg.TouchLastSeen(routerID)

	// 1.3 模拟 Agent 进程重启（旧版未持久化 revision 从 1 重新开始）发送心跳
	pOldPayload := map[string]any{
		"network_id":  "home",
		"revision":    1,
		"observed_at": time.Now().UTC(),
		"prefixes": []map[string]any{
			{"prefix": "2001:db8:1::/64"},
		},
	}
	pOldBody, _ := json.Marshal(pOldPayload)
	reqOld, _ := http.NewRequest(http.MethodPut, server.URL+"/api/v1/devices/"+routerID+"/network-prefixes", bytes.NewReader(pOldBody))
	reqOld.Header.Set("Authorization", "Bearer "+routerToken)
	reqOld.Header.Set("Content-Type", "application/json")
	respOld, err := http.DefaultClient.Do(reqOld)
	if err != nil {
		t.Fatal(err)
	}
	_ = respOld.Body.Close()
	if respOld.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 conflict for low revision 1, got %d", respOld.StatusCode)
	}

	// 1.4 验证因心跳被 409 拒绝且 LastSeenAt 未刷新，DDNS 设备触发 ddns_prefix_stale 导致 degraded
	mockNow = time.Now().UTC()
	snap1, err := healthSvc.EvaluateDevice(context.Background(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if snap1.Status != health.StatusDegraded {
		t.Fatalf("expected degraded status after 16m without accepted prefix refresh, got %s (reasons: %+v)", snap1.Status, snap1.Reasons)
	}
	hasStaleReason := false
	for _, r := range snap1.Reasons {
		if r.Code == "ddns_prefix_stale" && r.State == health.RuleFiring {
			hasStaleReason = true
			break
		}
	}
	if !hasStaleReason {
		t.Fatalf("expected firing ddns_prefix_stale reason, got reasons: %+v", snap1.Reasons)
	}

	// Step 2 (新行为验证：持久化递增成功刷新 LastSeenAt 并恢复 Healthy):
	// 持久化递增上报 revision 10
	pNewPayload := map[string]any{
		"network_id":  "home",
		"revision":    10,
		"observed_at": time.Now().UTC(),
		"prefixes": []map[string]any{
			{"prefix": "2001:db8:1::/64"},
		},
	}
	pNewBody, _ := json.Marshal(pNewPayload)
	reqNew, _ := http.NewRequest(http.MethodPut, server.URL+"/api/v1/devices/"+routerID+"/network-prefixes", bytes.NewReader(pNewBody))
	reqNew.Header.Set("Authorization", "Bearer "+routerToken)
	reqNew.Header.Set("Content-Type", "application/json")
	respNew, err := http.DefaultClient.Do(reqNew)
	if err != nil || respNew.StatusCode != http.StatusOK {
		t.Fatalf("router put network-prefixes rev 10 failed: resp=%v err=%v", respNew, err)
	}
	_ = respNew.Body.Close()

	// 评估健康状态 -> 恢复 Healthy
	snap2, err := healthSvc.EvaluateDevice(context.Background(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if snap2.Status != health.StatusHealthy {
		t.Fatalf("expected healthy status after accepted revision 10 refresh, got %s (reasons: %+v)", snap2.Status, snap2.Reasons)
	}

	// Step 3 (恢复行为验证：本地回退收到 409 后自动追赶成功):
	// 再次模拟状态过期
	ps2, _ := prefixSvc.GetByNetwork("home")
	ps2.LastSeenAt = time.Now().UTC().Add(-16 * time.Minute)
	_ = prefixStore.Save(*ps2)

	// 发送旧 revision 1 -> 得到 409 current_revision: 10
	reqConflict, _ := http.NewRequest(http.MethodPut, server.URL+"/api/v1/devices/"+routerID+"/network-prefixes", bytes.NewReader(pOldBody))
	reqConflict.Header.Set("Authorization", "Bearer "+routerToken)
	reqConflict.Header.Set("Content-Type", "application/json")
	respConflict, err := http.DefaultClient.Do(reqConflict)
	if err != nil || respConflict.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 conflict, got status=%d err=%v", respConflict.StatusCode, err)
	}
	var conflictData map[string]any
	_ = json.NewDecoder(respConflict.Body).Decode(&conflictData)
	_ = respConflict.Body.Close()

	if conflictData["error"] != "revision_conflict" || conflictData["current_revision"] != float64(10) {
		t.Fatalf("unexpected 409 conflict body: %+v", conflictData)
	}

	// 模拟追赶计算 next_revision = max(1, 10) + 1 = 11 并上报
	catchupPayload := map[string]any{
		"network_id":  "home",
		"revision":    11,
		"observed_at": time.Now().UTC(),
		"prefixes": []map[string]any{
			{"prefix": "2001:db8:1::/64"},
		},
	}
	catchupBody, _ := json.Marshal(catchupPayload)
	reqCatchup, _ := http.NewRequest(http.MethodPut, server.URL+"/api/v1/devices/"+routerID+"/network-prefixes", bytes.NewReader(catchupBody))
	reqCatchup.Header.Set("Authorization", "Bearer "+routerToken)
	reqCatchup.Header.Set("Content-Type", "application/json")
	respCatchup, err := http.DefaultClient.Do(reqCatchup)
	if err != nil || respCatchup.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK after catchup revision 11, got status=%d err=%v", respCatchup.StatusCode, err)
	}
	_ = respCatchup.Body.Close()

	snap3, err := healthSvc.EvaluateDevice(context.Background(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if snap3.Status != health.StatusHealthy {
		t.Fatalf("expected healthy status after catchup, got %s", snap3.Status)
	}
}

// 5. 跨版本双向兼容矩阵测试：v0.6.3 Agent -> v0.6.4 Server (结构化 409 安全返回)
func TestP1_CrossVersion_v063AgentToCandidateServer(t *testing.T) {
	tempDir := t.TempDir()
	reg, err := registry.Open(filepath.Join(tempDir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}

	routerID := "router-v063"
	routerToken := "tok-v063"
	if _, err := reg.Save(device.Device{
		ID:              routerID,
		Hostname:        "router-v063",
		OS:              "linux",
		Arch:            "mipsle",
		AgentVersion:    "v0.6.3",
		SSHUser:         "root",
		SSHPort:         22,
		PublicKey:       "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIRouterKeyv063",
		DeviceTokenHash: auth.HashToken(routerToken),
		LastSeenAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	prefixSvc := prefixstate.NewService(nil)
	apiServer := &api.Server{
		Registry:           reg,
		Token:              "admin-token",
		PrefixStateService: prefixSvc,
	}
	server := httptest.NewServer(apiServer.Handler())
	defer server.Close()

	// v0.6.3 Agent 先上报 revision 5
	p1Payload := `{"network_id":"home","revision":5,"observed_at":"2026-08-27T00:00:00Z","prefixes":[{"prefix":"2001:db8:1::/64"}]}`
	req1, _ := http.NewRequest(http.MethodPut, server.URL+"/api/v1/devices/"+routerID+"/network-prefixes", strings.NewReader(p1Payload))
	req1.Header.Set("Authorization", "Bearer "+routerToken)
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil || resp1.StatusCode != http.StatusOK {
		t.Fatalf("v0.6.3 first report failed: %v, %v", resp1, err)
	}
	_ = resp1.Body.Close()

	// v0.6.3 Agent 重启后发送 revision 1 -> 得到 409 JSON，旧 Agent 将其作为普通失败重试，不发生崩溃
	p2Payload := `{"network_id":"home","revision":1,"observed_at":"2026-08-27T00:01:00Z","prefixes":[{"prefix":"2001:db8:1::/64"}]}`
	req2, _ := http.NewRequest(http.MethodPut, server.URL+"/api/v1/devices/"+routerID+"/network-prefixes", strings.NewReader(p2Payload))
	req2.Header.Set("Authorization", "Bearer "+routerToken)
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 from candidate server, got %d", resp2.StatusCode)
	}
	ct := resp2.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("expected application/json content-type, got %s", ct)
	}
}
