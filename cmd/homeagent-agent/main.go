// Package main 实现 homeagent-agent 客户端命令行程序，支持主机接入注册（join）、
// 守护进程常驻（daemon）、系统服务注册（service）、硬件信息探测（info）及本地公钥部署（apply-keys）。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"homeagent/internal/daemon"
	"homeagent/internal/device"
	"homeagent/internal/networkaddr"
	"homeagent/internal/prefixstate"
	"homeagent/internal/sshsync"
	"homeagent/internal/version"
	"homeagent/internal/wol"
)

func main() {
	if runServiceIfWindows() {
		return
	}
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "claim":
		err = claim(os.Args[2:])
	case "join":
		err = join(os.Args[2:])
	case "daemon":
		err = runDaemon(os.Args[2:])
	case "service":
		err = runService(os.Args[2:])
	case "info":
		err = info()
	case "version", "-v", "--version":
		fmt.Printf("homeagent-agent %s (%s/%s)\n", version.Get(), runtime.GOOS, runtime.GOARCH)
	case "apply-keys":
		err = applyKeys(os.Stdin)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "homeagent-agent:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: homeagent-agent <claim|join|daemon|service|info|version|apply-keys> [flags]")
	os.Exit(2)
}

// DeviceConfigFile 本地设备凭据持久化模型
type DeviceConfigFile struct {
	ServerURL   string   `json:"server_url"`
	ServerURLs  []string `json:"server_urls,omitempty"`
	DeviceID    string   `json:"device_id"`
	DeviceToken string   `json:"device_token"`
	SSHUser     string   `json:"ssh_user,omitempty"`
	SSHPort     int      `json:"ssh_port,omitempty"`
}

func isMachineOrServiceAccount(username string) bool {
	if strings.HasSuffix(username, "$") {
		return true
	}
	upper := strings.ToUpper(strings.TrimSpace(username))
	switch upper {
	case "SYSTEM", "LOCALSYSTEM", "LOCAL SYSTEM", "NETWORKSERVICE", "NETWORK SERVICE", "LOCALSERVICE", "LOCAL SERVICE":
		return true
	}
	return false
}

func splitAndNormalizeURLs(inputs ...string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, input := range inputs {
		for _, raw := range strings.FieldsFunc(input, func(r rune) bool {
			return r == ',' || r == ' ' || r == ';'
		}) {
			u := strings.TrimSpace(raw)
			if u == "" {
				continue
			}
			if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
				u = "http://" + u
			}
			u = strings.TrimRight(u, "/")
			if !seen[u] {
				seen[u] = true
				result = append(result, u)
			}
		}
	}
	return result
}

func getDeviceConfigPath() string {
	if custom := os.Getenv("HOMEAGENT_DEVICE_CONFIG"); custom != "" {
		return custom
	}
	if runtime.GOOS == "windows" {
		progData := os.Getenv("ProgramData")
		if progData == "" {
			progData = `C:\ProgramData`
		}
		return filepath.Join(progData, "HomeAgent", "device.json")
	}
	if runtime.GOOS == "darwin" {
		if os.Geteuid() == 0 {
			return "/Library/Application Support/HomeAgent/device.json"
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "HomeAgent", "device.json")
	}
	// Linux / Other POSIX
	if os.Geteuid() == 0 {
		return "/var/lib/homeagent/device.json"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "homeagent", "device.json")
}

func loadDeviceConfig(path string) (*DeviceConfigFile, error) {
	if path == "" {
		path = getDeviceConfigPath()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg DeviceConfigFile
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	if len(cfg.ServerURLs) == 0 && cfg.ServerURL != "" {
		cfg.ServerURLs = splitAndNormalizeURLs(cfg.ServerURL)
	}
	if cfg.ServerURL == "" && len(cfg.ServerURLs) > 0 {
		cfg.ServerURL = cfg.ServerURLs[0]
	}
	return &cfg, nil
}

func saveDeviceConfig(path string, cfg DeviceConfigFile) error {
	if path == "" {
		path = getDeviceConfigPath()
	}
	if len(cfg.ServerURLs) == 0 && cfg.ServerURL != "" {
		cfg.ServerURLs = splitAndNormalizeURLs(cfg.ServerURL)
	}
	if cfg.ServerURL == "" && len(cfg.ServerURLs) > 0 {
		cfg.ServerURL = cfg.ServerURLs[0]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// 严格模式 0600 仅允许当前身份读写
	tmpFile := path + ".tmp"
	if err := os.WriteFile(tmpFile, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmpFile, path)
}

// claim 使用短期 Claim Token 向服务端换取专属 Device Token 并持久化保存到本地设备状态文件
func claim(args []string) error {
	fs := flag.NewFlagSet("claim", flag.ContinueOnError)
	server := fs.String("server", envOrDefault("HOMEAGENT_SERVER", "https://homeagent.rokilai.online"), "HomeAgent server URL")
	claimToken := fs.String("claim-token", envOrDefault("HOMEAGENT_CLAIM_TOKEN", os.Getenv("HOMEAGENT_JOIN_TOKEN")), "Claim Token")
	configPath := fs.String("config", os.Getenv("HOMEAGENT_DEVICE_CONFIG"), "Path to device.json state file")
	sshUser := fs.String("ssh-user", "", "SSH user (defaults to current user)")
	sshPort := fs.Int("ssh-port", 22, "SSH port")
	if err := fs.Parse(args); err != nil {
		return err
	}
	serverURLs := splitAndNormalizeURLs(*server)
	if len(serverURLs) == 0 || *claimToken == "" {
		return errors.New("--claim-token (or HOMEAGENT_CLAIM_TOKEN) is required")
	}
	cfgFile := *configPath
	if cfgFile == "" {
		cfgFile = getDeviceConfigPath()
	}

	d, privateKey, err := localDevice(*sshUser, *sshPort)
	if err != nil {
		return err
	}
	if err := ensureDeviceKey(privateKey); err != nil {
		return err
	}
	pub, err := os.ReadFile(privateKey + ".pub")
	if err != nil {
		return err
	}
	d.PublicKey = strings.TrimSpace(string(pub))

	client := &http.Client{Timeout: 15 * time.Second}
	claimReqBody := map[string]any{
		"device": d,
	}
	b, _ := json.Marshal(claimReqBody)

	var claimResp struct {
		Success        bool   `json:"success"`
		DeviceID       string `json:"device_id"`
		DeviceToken    string `json:"device_token"`
		AdminPublicKey string `json:"admin_public_key"`
	}

	var lastErr error
	var successfulServer string

	for _, srv := range serverURLs {
		reqURL := srv + "/api/v1/devices/claim"
		req, err := http.NewRequest("POST", reqURL, bytes.NewReader(b))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Authorization", "Bearer "+*claimToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("claim to %s failed: %w", srv, err)
			continue
		}

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			lastErr = fmt.Errorf("claim to %s failed: HTTP %d: %s", srv, resp.StatusCode, bytes.TrimSpace(body))
			continue
		}

		decodeErr := json.NewDecoder(resp.Body).Decode(&claimResp)
		resp.Body.Close()
		if decodeErr != nil {
			lastErr = fmt.Errorf("decode claim response from %s: %w", srv, decodeErr)
			continue
		}

		successfulServer = srv
		lastErr = nil
		break
	}

	if lastErr != nil {
		return lastErr
	}

	if claimResp.AdminPublicKey != "" {
		if err := installAdminKey(claimResp.AdminPublicKey); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to install admin public key: %v\n", err)
		}
	}

	devCfg := DeviceConfigFile{
		ServerURL:   successfulServer,
		ServerURLs:  serverURLs,
		DeviceID:    claimResp.DeviceID,
		DeviceToken: claimResp.DeviceToken,
		SSHUser:     d.SSHUser,
		SSHPort:     d.SSHPort,
	}
	if err := saveDeviceConfig(cfgFile, devCfg); err != nil {
		return fmt.Errorf("save device config: %w", err)
	}

	fmt.Printf("device successfully claimed: ID=%s (persisted to %s)\n", claimResp.DeviceID, cfgFile)
	return nil
}

// join 向 HomeAgent 控制平面服务端注册本机设备、生成本地公私钥对并拉取安装管理员 SSH 凭据。
func join(args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	server := fs.String("server", os.Getenv("HOMEAGENT_SERVER"), "HomeAgent server URL")
	token := fs.String("token", os.Getenv("HOMEAGENT_JOIN_TOKEN"), "join token")
	sshUser := fs.String("ssh-user", "", "SSH user (defaults to current user)")
	sshPort := fs.Int("ssh-port", 22, "SSH port")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *server == "" || *token == "" {
		return errors.New("--server and --token (or environment equivalents) are required")
	}
	d, privateKey, err := localDevice(*sshUser, *sshPort)
	if err != nil {
		return err
	}
	if err := ensureDeviceKey(privateKey); err != nil {
		return err
	}
	pub, err := os.ReadFile(privateKey + ".pub")
	if err != nil {
		return err
	}
	d.PublicKey = strings.TrimSpace(string(pub))
	client := &http.Client{Timeout: 10 * time.Second}
	adminKey, err := getAdminKey(client, strings.TrimRight(*server, "/"), *token)
	if err != nil {
		return err
	}
	if err := installAdminKey(adminKey); err != nil {
		return err
	}
	b, _ := json.Marshal(d)
	req, _ := http.NewRequest("POST", strings.TrimRight(*server, "/")+"/api/v1/devices/register", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+*token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("register: HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	fmt.Println("registered", d.ID)
	return nil
}

// runDaemon 启动 Agent 守护进程，监听操作系统信号并维持与服务端的 SSE 长连接和 IPv6 地址变动监听。
func runDaemon(args []string) error {

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runDaemonWithContext(ctx, args, logger)
}

func runDaemonWithContext(ctx context.Context, args []string, logger *slog.Logger) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	server := fs.String("server", os.Getenv("HOMEAGENT_SERVER"), "HomeAgent server URL")
	token := fs.String("token", os.Getenv("HOMEAGENT_JOIN_TOKEN"), "join token or device token")
	deviceID := fs.String("device-id", "", "Device ID (defaults to auto-detected local device ID)")
	configPath := fs.String("config", os.Getenv("HOMEAGENT_DEVICE_CONFIG"), "Path to device.json state file")
	authKeys := fs.String("authorized-keys", "", "Path to authorized_keys file")
	githubKey := fs.String("github-key", os.Getenv("HOMEAGENT_GITHUB_KEY"), "Path to GitHub SSH private key")
	ghHosts := fs.String("gh-hosts", os.Getenv("HOMEAGENT_GH_HOSTS"), "Path to gh CLI hosts.yml")
	sshConfig := fs.String("ssh-config", os.Getenv("HOMEAGENT_SSH_CONFIG"), "Path to SSH config")
	networkID := fs.String("network-id", envOrDefault("HOMEAGENT_NETWORK_ID", "home"), "Network ID for IPv6/DDNS")
	iface := fs.String("interface", os.Getenv("HOMEAGENT_INTERFACE"), "Interface name to monitor (defaults to auto)")
	isRouter := fs.Bool("router", os.Getenv("HOMEAGENT_ROUTER") == "true", "Run as router agent reporting OpenWrt prefixes")
	ipv6Report := fs.Bool("ipv6-report", true, "Enable IPv6 / prefix reporting")
	sshUser := fs.String("ssh-user", os.Getenv("HOMEAGENT_SSH_USER"), "SSH user override")
	sshPort := fs.Int("ssh-port", 0, "SSH port override")

	if err := fs.Parse(args); err != nil {
		return err
	}

	var serverURLs []string
	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = getDeviceConfigPath()
	}
	var cfgSSHUser string
	var cfgSSHPort int
	if devCfg, err := loadDeviceConfig(cfgPath); err == nil && devCfg != nil {
		if len(devCfg.ServerURLs) > 0 {
			serverURLs = append(serverURLs, devCfg.ServerURLs...)
		} else if devCfg.ServerURL != "" {
			serverURLs = append(serverURLs, devCfg.ServerURL)
		}
		if *token == "" && devCfg.DeviceToken != "" {
			*token = devCfg.DeviceToken
		}
		if *deviceID == "" && devCfg.DeviceID != "" {
			*deviceID = devCfg.DeviceID
		}
		cfgSSHUser = devCfg.SSHUser
		cfgSSHPort = devCfg.SSHPort
	}

	if *server != "" {
		serverURLs = append(splitAndNormalizeURLs(*server), serverURLs...)
	}
	serverURLs = splitAndNormalizeURLs(serverURLs...)

	if len(serverURLs) == 0 || *token == "" {
		return errors.New("--server and --token (or configured device.json) are required")
	}

	devID := *deviceID
	if devID == "" {
		d, _, err := localDevice("", 22)
		if err != nil {
			return fmt.Errorf("detect local device: %w", err)
		}
		devID = d.ID
	}

	d, err := daemon.New(daemon.Config{
		ServerURLs:         serverURLs,
		Token:              *token,
		DeviceID:           devID,
		AuthorizedKeysPath: *authKeys,
		GitHubKeyPath:      *githubKey,
		GHHostsPath:        *ghHosts,
		SSHConfigPath:      *sshConfig,
		CommandLedgerPath:  filepath.Join(filepath.Dir(cfgPath), "command-ledger.json"),
		Logger:             logger,
	})
	if err != nil {
		return err
	}

	finalSSHUser := *sshUser
	if finalSSHUser == "" {
		finalSSHUser = cfgSSHUser
	}
	finalSSHPort := *sshPort
	if finalSSHPort <= 0 {
		finalSSHPort = cfgSSHPort
	}
	if finalSSHPort <= 0 {
		finalSSHPort = 22
	}

	revStore := newRevisionStore(filepath.Dir(cfgPath))
	if *ipv6Report {
		if *isRouter {
			startRouterPrefixReporter(ctx, serverURLs, d.GetActiveServerURL, *token, devID, *networkID, *iface, revStore, logger)
		} else {
			startIPv6Reporter(ctx, serverURLs, d.GetActiveServerURL, *token, devID, *networkID, *iface, revStore, logger)
		}
	}
	startDeviceFactsReporter(ctx, serverURLs, d.GetActiveServerURL, *token, devID, finalSSHUser, finalSSHPort, logger)

	return d.Run(ctx)
}

func envOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

type deviceFactsPayload struct {
	Hostname         string                `json:"hostname"`
	MAC              string                `json:"mac,omitempty"`
	AgentVersion     string                `json:"agent_version,omitempty"`
	OS               string                `json:"os"`
	Arch             string                `json:"arch"`
	SSHUser          string                `json:"ssh_user"`
	SSHPort          int                   `json:"ssh_port"`
	Addresses        []string              `json:"addresses"`
	ControlProtocols []int                 `json:"control_protocols,omitempty"`
	Runtime          *device.RuntimeFacts  `json:"runtime,omitempty"`
}

func collectDeviceFacts(sshUser string, port int) (deviceFactsPayload, error) {
	d, _, err := localDevice(sshUser, port)
	if err != nil {
		return deviceFactsPayload{}, err
	}
	return deviceFactsPayload{
		Hostname: d.Hostname, MAC: d.MAC, AgentVersion: d.AgentVersion,
		OS: d.OS, Arch: d.Arch, SSHUser: d.SSHUser, SSHPort: d.SSHPort,
		Addresses:        d.Addresses,
		ControlProtocols: []int{1},
		Runtime:          getSystemRuntimeFacts(),
	}, nil
}

func sendDeviceFacts(ctx context.Context, client *http.Client, serverURLs []string, getActiveServer func() string, token, deviceID string, facts deviceFactsPayload) (string, error) {
	target, _, err := sendDeviceFactsWithStatus(ctx, client, serverURLs, getActiveServer, token, deviceID, facts)
	return target, err
}

func sendDeviceFactsWithStatus(ctx context.Context, client *http.Client, serverURLs []string, getActiveServer func() string, token, deviceID string, facts deviceFactsPayload) (string, bool, error) {
	body, err := json.Marshal(facts)
	if err != nil {
		return "", false, err
	}
	active := ""
	if getActiveServer != nil {
		active = getActiveServer()
	}
	targets := make([]string, 0, len(serverURLs)+1)
	if active != "" {
		targets = append(targets, active)
	}
	for _, u := range serverURLs {
		if u != active {
			targets = append(targets, u)
		}
	}
	var lastErr error
	for _, target := range targets {
		reqURL := fmt.Sprintf("%s/api/v1/devices/%s/facts", strings.TrimRight(target, "/"), deviceID)
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, bytes.NewReader(body))
		if reqErr != nil {
			lastErr = reqErr
			continue
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, reqErr := client.Do(req)
		if reqErr != nil {
			lastErr = reqErr
			continue
		}
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode < 400 {
			return target, false, nil
		}
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
			// 降级重试：若旧 Server 拒绝 control_protocols 或 runtime 字段
			legacyFacts := facts
			modified := false
			if len(facts.ControlProtocols) > 0 {
				legacyFacts.ControlProtocols = nil
				modified = true
			}
			if facts.Runtime != nil {
				legacyFacts.Runtime = nil
				modified = true
			}
			if modified {
				legacyBody, _ := json.Marshal(legacyFacts)
				legacyReq, legacyErr := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, bytes.NewReader(legacyBody))
				if legacyErr == nil {
					legacyReq.Header.Set("Authorization", "Bearer "+token)
					legacyReq.Header.Set("Content-Type", "application/json")
					legacyResp, doErr := client.Do(legacyReq)
					if doErr == nil {
						_, _ = io.Copy(io.Discard, io.LimitReader(legacyResp.Body, 4096))
						legacyResp.Body.Close()
						if legacyResp.StatusCode < 400 {
							return target, true, nil
						}
						lastErr = fmt.Errorf("HTTP %d from %s", legacyResp.StatusCode, target)
						continue
					}
					lastErr = doErr
					continue
				}
			}
		}
		lastErr = fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, target, strings.TrimSpace(string(responseBody)))
	}
	if lastErr != nil {
		return "", false, lastErr
	}
	return "", false, errors.New("no server reachable for device facts")
}

func startDeviceFactsReporter(ctx context.Context, serverURLs []string, getActiveServer func() string, token, deviceID, sshUser string, sshPort int, logger *slog.Logger) {
	go func() {
		poll := time.NewTicker(30 * time.Second)
		heartbeat := time.NewTicker(10 * time.Minute)
		defer poll.Stop()
		defer heartbeat.Stop()
		client := &http.Client{Timeout: 5 * time.Second}
		var lastSuccessful []byte
		legacyServerMode := false

		report := func(force bool) {
			facts, err := collectDeviceFacts(sshUser, sshPort)
			if err != nil {
				if logger != nil {
					logger.Warn("failed_to_collect_device_facts", "error", err)
				}
				return
			}
			// 如果处于降级模式且非心跳周期，直接剥离新字段
			if legacyServerMode && !force {
				facts.Runtime = nil
				facts.ControlProtocols = nil
			}
			current, _ := json.Marshal(facts)
			if !force && bytes.Equal(current, lastSuccessful) {
				return
			}
			target, downgraded, err := sendDeviceFactsWithStatus(ctx, client, serverURLs, getActiveServer, token, deviceID, facts)
			if err != nil {
				if logger != nil {
					logger.Warn("failed_to_report_device_facts", "device_id", deviceID, "error", err)
				}
				return
			}
			if downgraded {
				if !legacyServerMode && logger != nil {
					logger.Warn("server_does_not_support_runtime_facts_entering_legacy_mode", "device_id", deviceID, "server", target)
				}
				legacyServerMode = true
			} else if legacyServerMode {
				if logger != nil {
					logger.Info("server_recovered_runtime_facts_support_exiting_legacy_mode", "device_id", deviceID, "server", target)
				}
				legacyServerMode = false
			}
			lastSuccessful = append(lastSuccessful[:0], current...)
			if logger != nil {
				logger.Info("device_facts_reported", "device_id", deviceID, "server", target, "addresses", len(facts.Addresses), "mac", facts.MAC, "legacy_mode", legacyServerMode)
			}
		}

		report(true)
		for {
			select {
			case <-ctx.Done():
				return
			case <-poll.C:
				report(false)
			case <-heartbeat.C:
				report(true)
			}
		}
	}()
}

type networkReportConfig struct {
	ctx             context.Context
	client          *http.Client
	serverURLs      []string
	getActiveServer func() string
	token           string
	deviceID        string
	networkID       string
	reportType      string
	endpointPath    string
	buildPayload    func(rev uint64, observedAt time.Time) (any, error)
	store           *revisionStore
	logger          *slog.Logger
}

func maxUint64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

func sendNetworkReport(cfg networkReportConfig) error {
	key := revisionKey{
		ReportType: cfg.reportType,
		DeviceID:   cfg.deviceID,
		NetworkID:  cfg.networkID,
	}
	rev, err := cfg.store.Allocate(key)
	if err != nil {
		if cfg.logger != nil {
			cfg.logger.Warn("failed_to_allocate_revision", "report_type", cfg.reportType, "error", err)
		}
		return err
	}

	observedAt := time.Now().UTC()
	payloadObj, err := cfg.buildPayload(rev, observedAt)
	if err != nil {
		return err
	}
	bodyBytes, err := json.Marshal(payloadObj)
	if err != nil {
		return err
	}

	backoff := 1 * time.Second
	recovered := false
	var sendErr error

	for attempt := 0; attempt < 5; attempt++ {
		active := ""
		if cfg.getActiveServer != nil {
			active = cfg.getActiveServer()
		}
		var targets []string
		if active != "" {
			targets = append(targets, active)
		}
		for _, u := range cfg.serverURLs {
			if u != active {
				targets = append(targets, u)
			}
		}
		if len(targets) == 0 {
			targets = []string{"http://127.0.0.1:8080"}
		}

		targetIdx := 0
		for targetIdx < len(targets) {
			targetURL := targets[targetIdx]
			targetIdx++

			reqURL := fmt.Sprintf("%s%s", strings.TrimRight(targetURL, "/"), cfg.endpointPath)
			req, err := http.NewRequestWithContext(cfg.ctx, http.MethodPut, reqURL, bytes.NewReader(bodyBytes))
			if err != nil {
				sendErr = err
				continue
			}
			req.Header.Set("Authorization", "Bearer "+cfg.token)
			req.Header.Set("Content-Type", "application/json")

			resp, err := cfg.client.Do(req)
			if err != nil {
				sendErr = err
				continue
			}

			respBody, _ := io.ReadAll(http.MaxBytesReader(nil, resp.Body, 64*1024))
			_ = resp.Body.Close()

			if resp.StatusCode < 400 {
				if cfg.logger != nil {
					cfg.logger.Info("network_report_succeeded", "report_type", cfg.reportType, "server", targetURL, "revision", rev)
				}
				return nil
			}

			if resp.StatusCode == http.StatusConflict {
				ct := resp.Header.Get("Content-Type")
				if strings.HasPrefix(ct, "application/json") && !recovered {
					var conflict struct {
						Error            string `json:"error"`
						CurrentRevision  uint64 `json:"current_revision"`
						ReceivedRevision uint64 `json:"received_revision"`
					}
					if err := json.Unmarshal(respBody, &conflict); err == nil {
						if (conflict.Error == "revision_conflict" || conflict.Error == "revision_content_mismatch") &&
							conflict.ReceivedRevision == rev &&
							conflict.CurrentRevision >= conflict.ReceivedRevision &&
							conflict.CurrentRevision > 0 &&
							maxUint64(rev, conflict.CurrentRevision) < math.MaxUint64 {

							nextRev, advErr := cfg.store.AdvanceAfterConflict(key, conflict.CurrentRevision)
							if advErr == nil {
								recovered = true
								rev = nextRev
								observedAt = time.Now().UTC()
								newPayload, pErr := cfg.buildPayload(rev, observedAt)
								if pErr == nil {
									bodyBytes, _ = json.Marshal(newPayload)
									targetIdx = 0
									continue
								}
							}
						}
					}
				}
			}

			sendErr = fmt.Errorf("HTTP %d from %s", resp.StatusCode, targetURL)
		}

		if cfg.logger != nil {
			cfg.logger.Warn("failed_to_send_network_report_retrying", "report_type", cfg.reportType, "error", sendErr, "retry_in", backoff)
		}

		select {
		case <-cfg.ctx.Done():
			return cfg.ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
		}
	}

	return sendErr
}

func startIPv6Reporter(ctx context.Context, serverURLs []string, getActiveServer func() string, token, deviceID, networkID, iface string, store *revisionStore, logger *slog.Logger) {
	if store == nil {
		store = newRevisionStore(os.TempDir())
	}
	client := &http.Client{Timeout: 5 * time.Second}

	sendReport := func(snapshot []networkaddr.ReportedIPv6Address) {
		mac := localMAC(localAddresses())
		cfg := networkReportConfig{
			ctx:             ctx,
			client:          client,
			serverURLs:      serverURLs,
			getActiveServer: getActiveServer,
			token:           token,
			deviceID:        deviceID,
			networkID:       networkID,
			reportType:      reportTypeDeviceNetworkState,
			endpointPath:    fmt.Sprintf("/api/v1/devices/%s/network-state", deviceID),
			buildPayload: func(rev uint64, observedAt time.Time) (any, error) {
				return map[string]any{
					"network_id":     networkID,
					"revision":       rev,
					"observed_at":    observedAt,
					"ipv6_addresses": snapshot,
					"mac":            mac,
				}, nil
			},
			store:  store,
			logger: logger,
		}
		go func() {
			_ = sendNetworkReport(cfg)
		}()
	}

	watcher := networkaddr.NewWatcher(networkaddr.WatcherConfig{
		Interface:         iface,
		DebounceDuration:  2 * time.Second,
		HeartbeatInterval: 10 * time.Minute,
		Logger:            logger,
		OnSnapshot: func(snapshot []networkaddr.ReportedIPv6Address, changed bool) {
			sendReport(snapshot)
		},
	})

	watcher.Start()
	go func() {
		<-ctx.Done()
		watcher.Stop()
	}()
}

func startRouterPrefixReporter(ctx context.Context, serverURLs []string, getActiveServer func() string, token, routerID, networkID, iface string, store *revisionStore, logger *slog.Logger) {
	if store == nil {
		store = newRevisionStore(os.TempDir())
	}
	client := &http.Client{Timeout: 5 * time.Second}
	provider := prefixstate.NewOpenWrtUbusProvider(iface)

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		var lastPrefixes []prefixstate.ReportedIPv6Prefix

		report := func(force bool) {
			pCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			prefixes, err := provider.GetPrefixes(pCtx)
			cancel()
			if err != nil {
				if logger != nil {
					logger.Warn("failed_to_query_openwrt_prefixes", "error", err)
				}
				return
			}

			changed := !prefixstate.PrefixesEqual(lastPrefixes, prefixes)
			if !changed && !force {
				return
			}
			lastPrefixes = prefixes

			mac := localMAC(localAddresses())
			cfg := networkReportConfig{
				ctx:             ctx,
				client:          client,
				serverURLs:      serverURLs,
				getActiveServer: getActiveServer,
				token:           token,
				deviceID:        routerID,
				networkID:       networkID,
				reportType:      reportTypeRouterPrefixes,
				endpointPath:    fmt.Sprintf("/api/v1/devices/%s/network-prefixes", routerID),
				buildPayload: func(rev uint64, observedAt time.Time) (any, error) {
					return map[string]any{
						"network_id":  networkID,
						"revision":    rev,
						"observed_at": observedAt,
						"prefixes":    prefixes,
						"mac":         mac,
					}, nil
				},
				store:  store,
				logger: logger,
			}
			go func() {
				_ = sendNetworkReport(cfg)
			}()
		}

		// Initial report
		report(true)

		heartbeat := time.NewTicker(10 * time.Minute)
		defer heartbeat.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				report(false)
			case <-heartbeat.C:
				report(true)
			}
		}
	}()
}

// runService 处理 "service" CLI 子命令，管理 Agent 守护服务在操作系统上的安装、卸载、启动、停止及状态查看。
func runService(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: homeagent-agent service <install|uninstall|start|stop|status> [flags]")
	}
	action := args[0]
	fs := flag.NewFlagSet("service", flag.ContinueOnError)
	server := fs.String("server", os.Getenv("HOMEAGENT_SERVER"), "HomeAgent server URL")
	token := fs.String("token", os.Getenv("HOMEAGENT_JOIN_TOKEN"), "join token")
	binary := fs.String("binary", "", "Path to homeagent-agent binary")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	if devCfg, err := loadDeviceConfig(""); err == nil && devCfg != nil {
		if *server == "" {
			if len(devCfg.ServerURLs) > 0 {
				*server = strings.Join(devCfg.ServerURLs, ",")
			} else if devCfg.ServerURL != "" {
				*server = devCfg.ServerURL
			}
		}
		if *token == "" && devCfg.DeviceToken != "" {
			*token = devCfg.DeviceToken
		}
	}

	mgr, err := daemon.NewServiceManager(*binary, *server, *token)
	if err != nil {
		return err
	}

	switch action {
	case "install":
		if *server == "" || *token == "" {
			return errors.New("--server and --token are required to install service")
		}
		if err := mgr.Install(); err != nil {
			return fmt.Errorf("install service: %w", err)
		}
		fmt.Println("service installed successfully")
		return nil
	case "uninstall":
		if err := mgr.Uninstall(); err != nil {
			return fmt.Errorf("uninstall service: %w", err)
		}
		fmt.Println("service uninstalled successfully")
		return nil
	case "start":
		if err := mgr.Start(); err != nil {
			return fmt.Errorf("start service: %w", err)
		}
		fmt.Println("service started")
		return nil
	case "stop":
		if err := mgr.Stop(); err != nil {
			return fmt.Errorf("stop service: %w", err)
		}
		fmt.Println("service stopped")
		return nil
	case "status":
		status, err := mgr.Status()
		if err != nil {
			return fmt.Errorf("service status: %w", err)
		}
		fmt.Println(status)
		return nil
	default:
		return fmt.Errorf("unknown service action: %s", action)
	}
}

// localDevice 探测宿主机环境，构造设备注册元数据对象及私钥存放路径。
func localDevice(sshUser string, port int) (device.Device, string, error) {
	host, err := os.Hostname()
	if err != nil {
		return device.Device{}, "", err
	}
	current, err := user.Current()
	if err != nil {
		return device.Device{}, "", err
	}
	if port <= 0 {
		port = 22
	}
	if sshUser == "" {
		sshUser = current.Username
		if i := strings.LastIndexAny(sshUser, "\\/"); i >= 0 {
			sshUser = sshUser[i+1:]
		}
		if isMachineOrServiceAccount(sshUser) {
			sshUser = ""
		}
	}
	machineID, err := readMachineID()
	if err != nil {
		return device.Device{}, "", err
	}
	home := current.HomeDir
	if value, err := os.UserHomeDir(); err == nil {
		home = value
	}
	addrs := localAddresses()
	mac := localMAC(addrs)
	return device.Device{
		ID:           device.GenerateID(host, machineID),
		Hostname:     host,
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		SSHUser:      sshUser,
		SSHPort:      port,
		Addresses:    addrs,
		MAC:          mac,
		AgentVersion: version.Get(),
	}, filepath.Join(home, ".ssh", "id_ed25519"), nil
}

// readMachineID 跨 Linux、macOS 和 Windows 平台提取持久稳定的硬件 Machine UUID。
func readMachineID() (string, error) {

	if runtime.GOOS == "linux" {
		b, err := os.ReadFile("/etc/machine-id")
		if err == nil && len(bytes.TrimSpace(b)) > 0 {
			return string(bytes.TrimSpace(b)), nil
		}
		b, err = os.ReadFile("/var/lib/dbus/machine-id")
		if err == nil && len(bytes.TrimSpace(b)) > 0 {
			return string(bytes.TrimSpace(b)), nil
		}
		// OpenWrt / router fallback: try MAC address
		if b, err := os.ReadFile("/sys/class/net/eth0/address"); err == nil && len(bytes.TrimSpace(b)) > 0 {
			return strings.TrimSpace(string(b)), nil
		}
		// Persistent file fallback
		statePath := "/etc/homeagent_machine_id"
		if b, err := os.ReadFile(statePath); err == nil && len(bytes.TrimSpace(b)) > 0 {
			return strings.TrimSpace(string(b)), nil
		}
		// Generate and persist
		genID := fmt.Sprintf("hw-%d", time.Now().UnixNano())
		_ = os.WriteFile(statePath, []byte(genID), 0600)
		return genID, nil
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice")
	} else {
		cmd = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "(Get-CimInstance Win32_ComputerSystemProduct).UUID")
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read machine ID: %w", err)
	}
	if runtime.GOOS == "darwin" {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "IOPlatformUUID") {
				parts := strings.Split(line, "\"")
				if len(parts) >= 4 {
					return parts[3], nil
				}
			}
		}
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", errors.New("empty machine ID")
	}
	return id, nil
}

func isVirtualInterface(name string) bool {
	lower := strings.ToLower(name)
	if lower == "br-lan" {
		return false
	}
	prefixes := []string{
		"utun", "bridge", "docker", "veth", "tailscale", "wg", "tun", "tap",
		"virbr", "vmnet", "vboxnet", "vethernet", "br-", "cni0", "flannel",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

func isWirelessOrBluetoothInterface(name string) bool {
	lower := strings.ToLower(name)
	prefixes := []string{"wlan", "wl", "wi-fi", "wifi", "bluetooth", "awdl", "llw"}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) || strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func localMAC(validAddresses []string) string {
	type candidate struct {
		mac  string
		rank int
	}
	var candidates []candidate
	addrSet := map[string]bool{}
	for _, a := range validAddresses {
		addrSet[a] = true
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if isVirtualInterface(iface.Name) {
			continue
		}
		if len(iface.HardwareAddr) != 6 {
			continue
		}
		_, norm, err := wol.ParseAndValidateMAC(iface.HardwareAddr.String())
		if err != nil {
			continue
		}

		rank := 10
		lowerName := strings.ToLower(iface.Name)

		matchesIP := false
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			host, _, err := net.SplitHostPort(a.String())
			if err != nil {
				host = strings.Split(a.String(), "/")[0]
			}
			if addrSet[host] {
				matchesIP = true
				break
			}
		}

		isWired := strings.HasPrefix(lowerName, "eth") ||
			strings.HasPrefix(lowerName, "en") ||
			strings.HasPrefix(lowerName, "eno") ||
			strings.HasPrefix(lowerName, "enp") ||
			strings.Contains(lowerName, "ethernet") ||
			strings.Contains(lowerName, "以太网")

		isWireless := isWirelessOrBluetoothInterface(iface.Name)

		if isWired && matchesIP && !isWireless {
			rank = 1
		} else if isWired && !isWireless {
			rank = 2
		} else if matchesIP && !isWireless {
			rank = 3
		} else if !isWireless {
			rank = 4
		} else if matchesIP {
			rank = 5
		}

		candidates = append(candidates, candidate{mac: norm, rank: rank})
	}

	if len(candidates) == 0 {
		return ""
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].rank < candidates[j].rank
	})

	return candidates[0].mac
}

func localAddresses() []string {
	var values []string
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if isVirtualInterface(iface.Name) {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			host, _, err := net.SplitHostPort(a.String())
			if err != nil {
				host = strings.Split(a.String(), "/")[0]
			}
			if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
				values = append(values, host)
			}
		}
	}
	values = append(values, stableIPv6Addresses()...)
	return device.FilterAndSortAddresses(values)
}

func ensureDeviceKey(path string) error {
	if _, err := os.Stat(path); err == nil {
		if _, err := os.Stat(path + ".pub"); err == nil {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-f", path, "-N", "")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ssh-keygen: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func getAdminKey(client *http.Client, server, token string) (string, error) {
	req, _ := http.NewRequest("GET", server+"/api/v1/bootstrap/admin-key", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("admin key: HTTP %d", resp.StatusCode)
	}
	var body struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	return body.PublicKey, nil
}

func installAdminKey(key string) error {
	path, err := authorizedKeysPath()
	if err != nil {
		return err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	updated, err := sshsync.UpdateManagedBlock(existing, []sshsync.Key{{DeviceID: "homeagent-admin", PublicKey: key}})
	if err != nil {
		return err
	}
	return atomicWrite(path, updated)
}

func applyKeys(r io.Reader) error {
	var set sshsync.KeySet
	dec := json.NewDecoder(io.LimitReader(r, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&set); err != nil {
		return fmt.Errorf("decode keys: %w", err)
	}
	path, err := authorizedKeysPath()
	if err != nil {
		return err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	updated, err := sshsync.UpdateManagedBlock(existing, set.Keys)
	if err != nil {
		return err
	}
	return atomicWrite(path, updated)
}

func authorizedKeysPath() (string, error) {
	return daemon.DefaultAuthorizedKeysPath()
}

func atomicWrite(path string, data []byte) error {
	return daemon.AtomicWrite(path, data)
}

func info() error {
	d, key, err := localDevice("", 22)
	if err != nil {
		return err
	}
	if b, err := os.ReadFile(key + ".pub"); err == nil {
		d.PublicKey = strings.TrimSpace(string(b))
	}
	return json.NewEncoder(os.Stdout).Encode(d)
}
