package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"

	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"homeagent/internal/api"
	"homeagent/internal/broker"
	"homeagent/internal/ddns"
	"homeagent/internal/ddns/providers/cloudflare"
	"homeagent/internal/devicestate"
	"homeagent/internal/prefixstate"
	"homeagent/internal/registry"
	"homeagent/internal/sshsync"
)

type config struct {
	listen, dataDir, token, downloads, scripts string
	timeout                                    time.Duration
	sync                                       bool
}

func main() {
	if len(os.Args) < 2 {
		usage()
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
	case "ipv6":
		err = ipv6Command(cfg, rest)
	default:
		usage()
	}
	if err != nil {
		fatal(err)
	}
}
func usage() {
	fmt.Fprintln(os.Stderr, "usage: homeagent-server <serve|devices|rename|sync|ssh-test|ipv6> [flags] [device-id] [alias]")
	os.Exit(2)
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "homeagent-server:", err); os.Exit(1) }

func parseConfig(name string, args []string) (config, []string, error) {
	home, _ := os.UserHomeDir()
	defaultData := filepath.Join(home, "Library", "Application Support", "HomeAgent")
	if value := os.Getenv("HOMEAGENT_DATA_DIR"); value != "" {
		defaultData = value
	}
	c := config{listen: env("HOMEAGENT_LISTEN", ":8080"), dataDir: defaultData, token: os.Getenv("HOMEAGENT_JOIN_TOKEN"), downloads: os.Getenv("HOMEAGENT_DOWNLOADS_DIR"), timeout: 5 * time.Second, sync: true}
	c.scripts = env("HOMEAGENT_SCRIPTS_DIR", "scripts")
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&c.listen, "listen", c.listen, "listen address")
	fs.StringVar(&c.dataDir, "data-dir", c.dataDir, "data directory")
	fs.StringVar(&c.token, "join-token", c.token, "join token")
	fs.StringVar(&c.downloads, "downloads-dir", c.downloads, "agent binary directory")
	fs.StringVar(&c.scripts, "scripts-dir", c.scripts, "installer scripts directory")
	fs.DurationVar(&c.timeout, "ssh-timeout", c.timeout, "SSH timeout")
	fs.BoolVar(&c.sync, "sync", c.sync, "enable SSH synchronization")
	if err := fs.Parse(args); err != nil {
		return c, nil, err
	}
	if c.token == "" {
		return c, nil, errors.New("join token is required via --join-token or HOMEAGENT_JOIN_TOKEN")
	}
	return c, fs.Args(), nil
}
func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
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
			// Example or env-driven configuration
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

	handler := (&api.Server{
		Registry:           r,
		Broker:             eventBroker,
		ACLPath:            filepath.Join(c.dataDir, "acl.yaml"),
		Token:              c.token,
		AdminPublicKey:     pub,
		Sync:               syncer,
		Log:                logger,
		DownloadsDir:       c.downloads,
		ScriptsDir:         c.scripts,
		DeviceStateService: devStateSvc,
		PrefixStateService: prefixStateSvc,
		DDNSService:        ddnsSvc,
	}).Handler()
	server := &http.Server{Addr: c.listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 120 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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

func list(c config) error {
	r, _, _, err := components(c)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tHOSTNAME\tALIAS\tADDRESS\tOS\tSTATUS\tVERSION\tHASH\tLAST_SEEN")
	for _, d := range r.List() {
		address := ""
		if len(d.Addresses) > 0 {
			address = d.Addresses[0]
		}
		alias := d.Alias
		if alias == "" {
			alias = "-"
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
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.Hostname, alias, address, d.OS, status, versionStr, hashShort, lastSeen)
	}
	return w.Flush()
}

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
