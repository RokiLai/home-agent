package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"homeagent/internal/api"
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
	case "sync", "ssh-test":
		err = syncCommand(cfg, rest)
	default:
		usage()
	}
	if err != nil {
		fatal(err)
	}
}
func usage() {
	fmt.Fprintln(os.Stderr, "usage: homeagent-server <serve|devices|sync|ssh-test> [flags] [device-id]")
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
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	handler := (&api.Server{Registry: r, Token: c.token, AdminPublicKey: pub, Sync: syncer, Log: logger, DownloadsDir: c.downloads, ScriptsDir: c.scripts}).Handler()
	server := &http.Server{Addr: c.listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
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
	fmt.Fprintln(w, "ID\tHOSTNAME\tADDRESS\tOS")
	for _, d := range r.List() {
		address := ""
		if len(d.Addresses) > 0 {
			address = d.Addresses[0]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.ID, d.Hostname, address, d.OS)
	}
	return w.Flush()
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
