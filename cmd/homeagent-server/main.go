// Package main 实现 homeagent-server 服务端核心程序，提供中央控制平面、
// REST/SSE 接口、Web 控制面板 UI、设备持久化注册表、SSH 密钥同步控制器及 DDNS 自动化服务。
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
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"homeagent/internal/alerting"
	"homeagent/internal/alerting/webhook"
	"homeagent/internal/api"
	"homeagent/internal/auth"
	"homeagent/internal/broker"
	"homeagent/internal/command"
	commandfile "homeagent/internal/command/file"
	"homeagent/internal/ddns"
	"homeagent/internal/ddns/providers/cloudflare"
	"homeagent/internal/devicestate"
	"homeagent/internal/githubsync"
	"homeagent/internal/health"
	"homeagent/internal/prefixstate"
	"homeagent/internal/registry"
	"homeagent/internal/sshsync"
	"homeagent/internal/version"
	"homeagent/internal/wol"
)

type config struct {
	listen, publicURL, dataDir, token, downloads, scripts string
	adminUser, adminPass                                  string
	timeout                                               time.Duration
	sync                                                  bool
	mac, broadcast                                        string
	burst, port                                           int
	interval                                              time.Duration
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	if os.Args[1] == "version" || os.Args[1] == "-v" || os.Args[1] == "--version" {
		fmt.Printf("homeagent-server %s (%s/%s)\n", version.Get(), runtime.GOOS, runtime.GOARCH)
		return
	}
	cfg, rest, err := parseConfig(os.Args[1], os.Args[2:])
	if err != nil {
		fatal(err)
	}
	switch os.Args[1] {
	case "serve":
		err = serve(cfg)
	case "devices":
		err = list(cfg)
	case "rename", "alias":
		err = renameCommand(cfg, rest)
	case "sync", "ssh-test":
		err = syncCommand(cfg, rest)
	case "wake", "wol":
		err = wakeCommand(cfg, rest)
	case "shutdown", "poweroff":
		err = shutdownCommand(cfg, rest)
	case "ipv6":
		err = ipv6Command(cfg, rest)
	case "upgrade":
		err = upgradeCommand(cfg, rest)
	default:
		usage()
	}
	if err != nil {
		fatal(err)
	}
}
func usage() {
	fmt.Fprintln(os.Stderr, "usage: homeagent-server <serve|devices|rename|sync|ssh-test|wake|shutdown|ipv6|upgrade|version> [flags] [device-id|alias] [alias]")
	os.Exit(2)
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "homeagent-server:", err); os.Exit(1) }

func parseConfig(name string, args []string) (config, []string, error) {
	home, _ := os.UserHomeDir()
	defaultData := filepath.Join(home, "Library", "Application Support", "HomeAgent")
	if value := os.Getenv("HOMEAGENT_DATA_DIR"); value != "" {
		defaultData = value
	}
	c := config{
		listen:    env("HOMEAGENT_LISTEN", ":8080"),
		publicURL: env("HOMEAGENT_PUBLIC_URL", ""),
		dataDir:   defaultData,
		token:     os.Getenv("HOMEAGENT_JOIN_TOKEN"),
		adminUser: env("HOMEAGENT_ADMIN_USERNAME", "admin"),
		adminPass: os.Getenv("HOMEAGENT_ADMIN_PASSWORD"),
		downloads: os.Getenv("HOMEAGENT_DOWNLOADS_DIR"),
		scripts:   env("HOMEAGENT_SCRIPTS_DIR", "scripts"),
		timeout:   5 * time.Second,
		sync:      true,
		burst:     3,
		port:      9,
		interval:  50 * time.Millisecond,
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&c.listen, "listen", c.listen, "listen address")
	fs.StringVar(&c.publicURL, "public-url", c.publicURL, "public server URL")
	fs.StringVar(&c.dataDir, "data-dir", c.dataDir, "data directory")
	fs.StringVar(&c.token, "join-token", c.token, "legacy join token (deprecated)")
	fs.StringVar(&c.adminUser, "admin-user", c.adminUser, "admin username")
	fs.StringVar(&c.adminPass, "admin-pass", c.adminPass, "admin password")
	fs.StringVar(&c.downloads, "downloads-dir", c.downloads, "agent binary directory")
	fs.StringVar(&c.scripts, "scripts-dir", c.scripts, "installer scripts directory")
	fs.DurationVar(&c.timeout, "ssh-timeout", c.timeout, "SSH timeout")
	fs.BoolVar(&c.sync, "sync", c.sync, "enable SSH synchronization")
	fs.StringVar(&c.mac, "mac", c.mac, "target MAC address for wake")
	fs.StringVar(&c.broadcast, "broadcast", c.broadcast, "broadcast address for wake")
	fs.IntVar(&c.burst, "burst", c.burst, "burst count for wake")
	fs.IntVar(&c.port, "port", c.port, "UDP port for wake")
	fs.DurationVar(&c.interval, "interval", c.interval, "burst interval for wake")
	if err := fs.Parse(args); err != nil {
		return c, nil, err
	}
	return c, fs.Args(), nil
}
func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func durationEnv(name string, fallback time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func components(c config) (*registry.Registry, *sshsync.Controller, string, error) {
	r, err := registry.Open(filepath.Join(c.dataDir, "devices.json"))
	if err != nil {
		return nil, nil, "", err
	}
	private := filepath.Join(c.dataDir, "keys", "admin_ed25519")
	pub, err := sshsync.EnsureAdminKey(private)
	if err != nil {
		return nil, nil, "", err
	}
	controller := &sshsync.Controller{Registry: r, ACLPath: filepath.Join(c.dataDir, "acl.yaml"), PrivateKey: private, KnownHosts: filepath.Join(c.dataDir, "ssh", "known_hosts"), Timeout: c.timeout}
	controller.AdminPublicKey = pub
	return r, controller, pub, nil
}

// serve 初始化后端各子系统（注册表、Broker、DDNS、GitHub 同步、管理员密钥、会话与认领凭据管理器）并启动 HTTP 服务监听。
func serve(c config) error {

	r, syncer, pub, err := components(c)
	if err != nil {
		return err
	}
	if !c.sync {
		syncer = nil
	}
	eventBroker := broker.New()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	commandRepo, err := commandfile.Open(filepath.Join(c.dataDir, "commands.json"))
	if err != nil {
		return fmt.Errorf("init command repository: %w", err)
	}
	commandService := command.NewService(commandRepo, nil)
	acceptTimeout := durationEnv("HOMEAGENT_COMMAND_ACCEPT_TIMEOUT", 15*time.Second)
	commandTimeouts := map[command.Kind]command.TimeoutPolicy{
		command.KindSSHKeys:      {Accept: acceptTimeout, Finish: durationEnv("HOMEAGENT_COMMAND_SSH_FINISH_TIMEOUT", time.Minute)},
		command.KindUpgrade:      {Accept: acceptTimeout, Finish: durationEnv("HOMEAGENT_COMMAND_UPGRADE_FINISH_TIMEOUT", 10*time.Minute)},
		command.KindShutdown:     {Accept: acceptTimeout, Finish: durationEnv("HOMEAGENT_COMMAND_SHUTDOWN_FINISH_TIMEOUT", 30*time.Second)},
		command.KindGitHubSync:   {Accept: acceptTimeout, Finish: durationEnv("HOMEAGENT_COMMAND_GITHUB_FINISH_TIMEOUT", 2*time.Minute)},
		command.KindGitHubRevoke: {Accept: acceptTimeout, Finish: durationEnv("HOMEAGENT_COMMAND_GITHUB_FINISH_TIMEOUT", 2*time.Minute)},
	}
	if interrupted, interruptErr := commandService.InterruptNonTerminal(); interruptErr != nil {
		return fmt.Errorf("interrupt incomplete commands: %w", interruptErr)
	} else if interrupted > 0 {
		logger.Info("commands_interrupted_on_startup", "count", interrupted)
	}

	// 初始化认证与会话子系统
	sessionMgr, err := auth.NewSessionManager(filepath.Join(c.dataDir, "auth.json"))
	if err != nil {
		return fmt.Errorf("init session manager: %w", err)
	}

	adminUser := c.adminUser
	if adminUser == "" {
		adminUser = "admin"
	}
	if c.adminPass != "" {
		created, err := sessionMgr.InitAdminBootstrap(adminUser, c.adminPass)
		if err != nil {
			logger.Warn("failed_to_bootstrap_admin_user", "error", err)
		} else if created {
			logger.Info("admin_user_bootstrap_initialized", "username", adminUser)
		} else {
			logger.Info("admin_user_already_exists_preserving_password", "username", adminUser)
		}
	} else if !sessionMgr.HasAdmin() {
		randPass, _ := auth.GenerateSecureToken("admin_", 8)
		_, _ = sessionMgr.InitAdminBootstrap(adminUser, randPass)
		logger.Info("admin_user_bootstrap_created", "username", adminUser, "password", randPass, "hint", "Please login and change your password in the Web Console")
	}

	enrollmentMgr, err := auth.NewEnrollmentManager(filepath.Join(c.dataDir, "enrollment.json"))
	if err != nil {
		return fmt.Errorf("init enrollment manager: %w", err)
	}

	rateLimiter := auth.NewRateLimiter(5, 15*time.Minute)

	devStateSvc := devicestate.NewService(nil)
	prefixStateSvc := prefixstate.NewService(nil)

	var ddnsSvc *ddns.Service
	cfToken := os.Getenv("HOMEAGENT_CLOUDFLARE_TOKEN")
	if cfToken != "" {
		cfZoneID := os.Getenv("HOMEAGENT_CLOUDFLARE_ZONE_ID")
		cfClient, err := cloudflare.NewClient(cloudflare.Config{
			APIToken: cfToken,
			ZoneID:   cfZoneID,
		})
		if err != nil {
			logger.Error("failed_to_init_cloudflare_client", "error", err)
		} else {
			ddnsCfg := ddns.Config{
				Enabled:  true,
				Networks: make(map[string]ddns.NetworkConfig),
				Devices:  make(map[string]ddns.DeviceConfig),
			}
			netID := env("HOMEAGENT_DDNS_NETWORK_ID", "home")
			routerID := os.Getenv("HOMEAGENT_DDNS_ROUTER_ID")
			if routerID != "" {
				ddnsCfg.Networks[netID] = ddns.NetworkConfig{
					RouterDeviceID: routerID,
					PrefixStateTTL: 15 * time.Minute,
				}
			}
			targetDevID := os.Getenv("HOMEAGENT_DDNS_DEVICE_ID")
			record := os.Getenv("HOMEAGENT_DDNS_RECORD")
			if targetDevID != "" && record != "" {
				ddnsCfg.Devices[targetDevID] = ddns.DeviceConfig{
					NetworkID: netID,
					Record:    record,
				}
			}
			ddnsSvc = ddns.NewService(ddnsCfg, devStateSvc, prefixStateSvc, cfClient, logger)
		}
	}

	ghSvc, err := githubsync.NewService(c.dataDir, nil, logger)
	if err != nil {
		logger.Warn("failed_to_initialize_github_sync_service", "error", err)
	}

	// 初始化健康评估子系统
	adapters := &serverHealthAdapters{
		reg:         r,
		broker:      eventBroker,
		syncer:      syncer,
		adminPub:    pub,
		devState:    devStateSvc,
		prefixState: prefixStateSvc,
		ddnsSvc:     ddnsSvc,
		cmdRepo:     commandRepo,
	}

	healthRepo, err := health.NewFileRepository(filepath.Join(c.dataDir, "health"))
	if err != nil {
		return fmt.Errorf("init health repository: %w", err)
	}
	healthSvc := health.NewService(health.ServiceConfig{
		Config:      health.DefaultEvaluatorConfig(),
		Repo:        healthRepo,
		FactsPort:   adapters,
		SSHPort:     adapters,
		DDNSPort:    adapters,
		CmdPort:     adapters,
		VersionPort: adapters,
	})

	// 初始化告警与通道分发子系统
	alertRepo, err := alerting.NewFileRepository(filepath.Join(c.dataDir, "alerting"))
	if err != nil {
		return fmt.Errorf("init alerting repository: %w", err)
	}
	alertingSvc := alerting.NewService(alerting.ServiceConfig{
		Repo:         alertRepo,
		NameResolver: &serverNameResolver{reg: r},
	})

	// 注册通用 Webhook 通道（若环境变量已配置）
	webhookURL := os.Getenv("HOMEAGENT_WEBHOOK_URL")
	webhookSecret := os.Getenv("HOMEAGENT_WEBHOOK_SECRET")
	if webhookURL != "" {
		if len([]byte(webhookSecret)) < 32 {
			logger.Error("webhook_alert_channel_disabled_invalid_secret", "hint", "HOMEAGENT_WEBHOOK_SECRET must be at least 32 bytes")
		} else {
			whChannel, whErr := webhook.NewChannel(webhook.Config{
				ID:        "default-webhook",
				URL:       webhookURL,
				Secret:    webhookSecret,
				Timeout:   5 * time.Second,
				AllowHTTP: strings.HasPrefix(webhookURL, "http://127.0.0.1") || strings.HasPrefix(webhookURL, "http://localhost"),
			})
			if whErr == nil {
				alertingSvc.RegisterChannel(whChannel)
				logger.Info("webhook_alert_channel_registered", "url", webhookURL)
			} else {
				logger.Warn("failed_to_register_webhook_channel", "error", whErr)
			}
		}
	}

	// 恢复未完成的异步重试任务（跨重启恢复）
	if err := alertingSvc.RecoverPendingRetries(context.Background()); err != nil {
		logger.Warn("failed_to_recover_pending_alert_retries", "error", err)
	}

	// 桥接健康事件到告警系统
	healthSvc.RegisterListener(func(ctx context.Context, events []health.HealthEvent) {
		alertingSvc.HandleHealthEvents(ctx, events)
	})

	handler := (&api.Server{
		Registry:           r,
		Broker:             eventBroker,
		SessionManager:     sessionMgr,
		EnrollmentManager:  enrollmentMgr,
		RateLimiter:        rateLimiter,
		ACLPath:            filepath.Join(c.dataDir, "acl.yaml"),
		Token:              c.token,
		AdminPublicKey:     pub,
		Sync:               syncer,
		GitHubSyncService:  ghSvc,
		Log:                logger,
		DownloadsDir:       c.downloads,
		ScriptsDir:         c.scripts,
		PublicURL:          c.publicURL,
		DeviceStateService: devStateSvc,
		PrefixStateService: prefixStateSvc,
		DDNSService:        ddnsSvc,
		Commands:           commandService,
		CommandTimeouts:    commandTimeouts,
		Health:             healthSvc,
		Alerting:           alertingSvc,
	}).Handler()
	server := &http.Server{Addr: c.listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 120 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	healthSvc.StartSweep(ctx, 1*time.Minute)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if expired, e := commandService.Expire(100); e != nil {
					logger.Error("command_timeout_sweep_failed", "error", e)
				} else if expired > 0 {
					logger.Info("commands_timed_out", "count", expired)
				}
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if syncer != nil {
		go syncer.SyncAll(context.Background())
	}
	if ddnsSvc != nil {
		ddnsSvc.StartBackgroundSweep(ctx, 1*time.Minute)
	}
	logger.Info("server_started", "listen", c.listen)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// list 在终端以表格格式打印所有已注册设备的状态概览。
func list(c config) error {
	r, _, _, err := components(c)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tHOSTNAME\tALIAS\tMAC\tAGENT_VER\tADDRESS\tOS\tSTATUS\tVERSION\tHASH\tLAST_SEEN")
	for _, d := range r.List() {
		address := ""
		if len(d.Addresses) > 0 {
			address = d.Addresses[0]
		}
		alias := d.Alias
		if alias == "" {
			alias = "-"
		}
		mac := d.MAC
		if mac == "" {
			mac = "-"
		}
		status := d.SyncStatus
		if status == "" {
			status = "pending"
		}
		hashShort := d.AppliedHash
		if len(hashShort) > 8 {
			hashShort = hashShort[:8]
		} else if hashShort == "" {
			hashShort = "-"
		}
		versionStr := strconv.FormatInt(d.AppliedVersion, 10)
		if d.AppliedVersion == 0 {
			versionStr = "-"
		}
		lastSeen := "-"
		if !d.LastSeenAt.IsZero() {
			lastSeen = d.LastSeenAt.Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.Hostname, alias, mac, address, d.OS, status, versionStr, hashShort, lastSeen)
	}
	return w.Flush()
}

// renameCommand 通过命令行修改指定设备的自定义别名。
func renameCommand(c config, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: homeagent-server rename <device-id> <new-alias>")
	}
	devID := args[0]
	alias := args[1]
	r, _, _, err := components(c)
	if err != nil {
		return err
	}
	updated, err := r.UpdateAlias(devID, alias)
	if err != nil {
		return fmt.Errorf("rename device %s: %w", devID, err)
	}
	fmt.Printf("device %s renamed to %q\n", updated.ID, updated.Alias)
	return nil
}

// syncCommand 通过出站 SSH 管道对单个或全量设备执行公钥同步。
func syncCommand(c config, args []string) error {
	_, controller, _, err := components(c)
	if err != nil {
		return err
	}
	if len(args) > 1 {
		return errors.New("only one device ID may be specified")
	}
	if len(args) == 1 {
		result := controller.SyncDevice(context.Background(), args[0])
		if result.Error != "" {
			return errors.New(result.Error)
		}
		fmt.Println(result.DeviceID, result.Address, "OK")
		return nil
	}
	failed := false
	for _, result := range controller.SyncAll(context.Background()) {
		if result.Error != "" {
			failed = true
			fmt.Println(result.DeviceID, "FAILED", strconv.Quote(result.Error))
		} else {
			fmt.Println(result.DeviceID, result.Address, "OK")
		}
	}
	if failed {
		return errors.New("one or more devices failed")
	}
	return nil
}

// wakeCommand 向目标设备（按 ID、别名、主机名或 MAC 查询）发送网络唤醒（WOL）魔术包。
func wakeCommand(c config, args []string) error {
	targetMAC := strings.TrimSpace(c.mac)
	broadcast := strings.TrimSpace(c.broadcast)
	burstCount := c.burst
	if burstCount <= 0 {
		burstCount = 3
	}
	interval := c.interval
	if interval <= 0 {
		interval = 50 * time.Millisecond
	}
	port := c.port
	if port <= 0 {
		port = 9
	}

	targetName := ""
	var targetIPs []string

	if len(args) > 0 {
		targetQuery := strings.TrimSpace(args[0])
		r, _, _, err := components(c)
		if err != nil {
			return err
		}
		// Search device by ID first
		d, err := r.Get(targetQuery)
		if err != nil {
			// Search by Alias or Hostname (case-insensitive)
			found := false
			for _, dev := range r.List() {
				if strings.EqualFold(dev.Alias, targetQuery) || strings.EqualFold(dev.Hostname, targetQuery) || strings.EqualFold(dev.ID, targetQuery) {
					d = dev
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("device %q not found", targetQuery)
			}
		}

		targetName = d.Alias
		if targetName == "" {
			targetName = d.Hostname
		}
		if targetName == "" {
			targetName = d.ID
		}
		targetIPs = d.Addresses

		if targetMAC == "" {
			targetMAC = d.MAC
		}
	}

	if targetMAC == "" {
		return errors.New("target MAC address is required via device argument or --mac")
	}

	_, normMAC, err := wol.ParseAndValidateMAC(targetMAC)
	if err != nil {
		return fmt.Errorf("invalid MAC address %q: %w", targetMAC, err)
	}

	opts := &wol.Options{
		BurstCount:    burstCount,
		BurstInterval: interval,
		Port:          port,
		TargetIPs:     targetIPs,
	}
	if broadcast != "" {
		opts.BroadcastAddrs = []string{broadcast}
	}

	if err := wol.Wake(normMAC, opts); err != nil {
		return fmt.Errorf("wake %s (%s): %w", targetName, normMAC, err)
	}

	if targetName != "" {
		fmt.Printf("[OK] Sent WOL magic packet (%d bursts) to %s (%s)\n", burstCount, targetName, normMAC)
	} else {
		fmt.Printf("[OK] Sent WOL magic packet (%d bursts) to %s\n", burstCount, normMAC)
	}
	return nil
}

// ipv6Command 打印目标设备的第一个有效全球单播 IPv6 地址。
func ipv6Command(c config, args []string) error {
	if len(args) < 1 {
		return errors.New("device ID is required")
	}
	devID := args[0]
	r, _, _, err := components(c)
	if err != nil {
		return err
	}
	d, err := r.Get(devID)
	if err != nil {
		return fmt.Errorf("get device %s: %w", devID, err)
	}
	for _, raw := range d.Addresses {
		ip := net.ParseIP(raw)
		if ip != nil && ip.To4() == nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
			fmt.Println(ip.String())
			return nil
		}
	}
	return fmt.Errorf("no valid IPv6 found for device %s", devID)
}

// upgradeCommand 通过 CLI 命令向指定的单个或全部设备分发自升级指令配置。
func upgradeCommand(c config, args []string) error {

	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	targetVer := fs.String("version", version.Get(), "target version")
	url := fs.String("url", "", "agent binary download URL")
	sha := fs.String("sha256", "", "SHA256 checksum of the new binary")
	force := fs.Bool("force", false, "force upgrade even if version matches")
	if err := fs.Parse(args); err != nil {
		return err
	}
	remaining := fs.Args()
	if len(remaining) < 1 {
		return errors.New("usage: homeagent-server upgrade <device-id|alias|all> [--version <ver>] [--url <url>] [--sha256 <sha256>] [--force]")
	}
	targetQuery := strings.TrimSpace(remaining[0])

	r, _, _, err := components(c)
	if err != nil {
		return err
	}

	s := &api.Server{
		Registry:     r,
		DownloadsDir: c.downloads,
	}

	if targetQuery == "all" {
		devices := r.List()
		if len(devices) == 0 {
			fmt.Println("No registered devices found.")
			return nil
		}
		count := 0
		for _, dev := range devices {
			req := api.UpgradeRequest{
				TargetVersion: *targetVer,
				URL:           *url,
				SHA256:        *sha,
				Force:         *force,
			}
			payload, err := s.ResolveUpgradePayload(dev, req, nil)
			if err != nil {
				fmt.Printf("[%s] SKIPPED: %v\n", dev.ID, err)
				continue
			}
			fmt.Printf("[%s (%s)] Target: %s, URL: %s, SHA256: %s\n", dev.ID, dev.Hostname, payload.TargetVersion, payload.URL, payload.SHA256)
			count++
		}
		fmt.Printf("[OK] Triggered upgrade configuration for %d devices.\n", count)
		return nil
	}

	// Search single device
	d, err := r.Get(targetQuery)
	if err != nil {
		found := false
		for _, dev := range r.List() {
			if strings.EqualFold(dev.Alias, targetQuery) || strings.EqualFold(dev.Hostname, targetQuery) || strings.EqualFold(dev.ID, targetQuery) {
				d = dev
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("device %q not found", targetQuery)
		}
	}

	req := api.UpgradeRequest{
		TargetVersion: *targetVer,
		URL:           *url,
		SHA256:        *sha,
		Force:         *force,
	}
	payload, err := s.ResolveUpgradePayload(d, req, nil)
	if err != nil {
		return fmt.Errorf("resolve upgrade for %s: %w", d.ID, err)
	}

	displayName := d.Alias
	if displayName == "" {
		displayName = d.Hostname
	}
	if displayName == "" {
		displayName = d.ID
	}

	fmt.Printf("[OK] Device: %s (%s), TargetVersion: %s, URL: %s, SHA256: %s\n", d.ID, displayName, payload.TargetVersion, payload.URL, payload.SHA256)
	return nil
}

// shutdownCommand 向指定的设备下发远程关机控制指令。
func shutdownCommand(c config, args []string) error {
	fs := flag.NewFlagSet("shutdown", flag.ContinueOnError)
	reason := fs.String("reason", "cli_command", "reason for shutdown")
	delay := fs.Duration("delay", 1*time.Second, "delay before shutdown")
	force := fs.Bool("force", false, "force shutdown")
	serverURL := fs.String("server", "", "server URL (defaults to http://127.0.0.1:<listen-port>)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	remaining := fs.Args()
	if len(remaining) < 1 {
		return errors.New("usage: homeagent-server shutdown <device-id|alias> [--reason <reason>] [--delay <duration>] [--force] [--server <url>]")
	}
	targetQuery := strings.TrimSpace(remaining[0])

	// 尝试通过本地 devices.json 将 alias / hostname 转换为精确 device-id
	targetID := targetQuery
	targetDisplayName := targetQuery
	if r, _, _, err := components(c); err == nil {
		if d, err := r.Get(targetQuery); err == nil {
			targetID = d.ID
			if d.Alias != "" {
				targetDisplayName = fmt.Sprintf("%s (%s)", d.ID, d.Alias)
			} else if d.Hostname != "" {
				targetDisplayName = fmt.Sprintf("%s (%s)", d.ID, d.Hostname)
			}
		} else {
			for _, dev := range r.List() {
				if strings.EqualFold(dev.Alias, targetQuery) || strings.EqualFold(dev.Hostname, targetQuery) {
					targetID = dev.ID
					targetDisplayName = fmt.Sprintf("%s (%s)", dev.ID, dev.Hostname)
					if dev.Alias != "" {
						targetDisplayName = fmt.Sprintf("%s (%s)", dev.ID, dev.Alias)
					}
					break
				}
			}
		}
	}

	srv := *serverURL
	if srv == "" {
		srv = os.Getenv("HOMEAGENT_SERVER")
	}
	if srv == "" {
		hostPort := c.listen
		if strings.HasPrefix(hostPort, ":") {
			hostPort = "127.0.0.1" + hostPort
		}
		srv = "http://" + hostPort
	}
	srv = strings.TrimRight(srv, "/")

	endpoint := fmt.Sprintf("%s/api/v1/devices/%s/shutdown", srv, targetID)
	reqBody := map[string]any{
		"reason":        *reason,
		"delay_seconds": int(delay.Seconds()),
		"force":         *force,
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(context.Background(), "POST", endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("create shutdown request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send shutdown request to %s: %w", srv, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("shutdown failed (%d): %s", resp.StatusCode, bytes.TrimSpace(body))
	}

	fmt.Printf("[OK] Remote shutdown command dispatched to %s\n", targetDisplayName)
	return nil
}
