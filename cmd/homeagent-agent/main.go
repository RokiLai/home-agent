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
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"homeagent/internal/daemon"
	"homeagent/internal/device"
	"homeagent/internal/networkaddr"
	"homeagent/internal/prefixstate"
	"homeagent/internal/sshsync"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "join":
		err = join(os.Args[2:])
	case "daemon":
		err = runDaemon(os.Args[2:])
	case "service":
		err = runService(os.Args[2:])
	case "info":
		err = info()
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
	fmt.Fprintln(os.Stderr, "usage: homeagent-agent <join|daemon|service|info|apply-keys> [flags]")
	os.Exit(2)
}

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

func runDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	server := fs.String("server", os.Getenv("HOMEAGENT_SERVER"), "HomeAgent server URL")
	token := fs.String("token", os.Getenv("HOMEAGENT_JOIN_TOKEN"), "join token")
	deviceID := fs.String("device-id", "", "Device ID (defaults to auto-detected local device ID)")
	authKeys := fs.String("authorized-keys", "", "Path to authorized_keys file")
	networkID := fs.String("network-id", envOrDefault("HOMEAGENT_NETWORK_ID", "home"), "Network ID for IPv6/DDNS")
	iface := fs.String("interface", os.Getenv("HOMEAGENT_INTERFACE"), "Interface name to monitor (defaults to auto)")
	isRouter := fs.Bool("router", os.Getenv("HOMEAGENT_ROUTER") == "true", "Run as router agent reporting OpenWrt prefixes")
	ipv6Report := fs.Bool("ipv6-report", true, "Enable IPv6 / prefix reporting")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *server == "" || *token == "" {
		return errors.New("--server and --token (or environment equivalents) are required")
	}

	devID := *deviceID
	if devID == "" {
		d, _, err := localDevice("", 22)
		if err != nil {
			return fmt.Errorf("detect local device: %w", err)
		}
		devID = d.ID
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	d, err := daemon.New(daemon.Config{
		ServerURL:          *server,
		Token:              *token,
		DeviceID:           devID,
		AuthorizedKeysPath: *authKeys,
		Logger:             logger,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *ipv6Report {
		if *isRouter {
			startRouterPrefixReporter(ctx, *server, *token, devID, *networkID, *iface, logger)
		} else {
			startIPv6Reporter(ctx, *server, *token, devID, *networkID, *iface, logger)
		}
	}

	return d.Run(ctx)
}

func envOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func startIPv6Reporter(ctx context.Context, serverURL, token, deviceID, networkID, iface string, logger *slog.Logger) {
	var revision atomic.Uint64
	client := &http.Client{Timeout: 10 * time.Second}

	sendReport := func(snapshot []networkaddr.ReportedIPv6Address) {
		rev := revision.Add(1)
		payload := map[string]any{
			"network_id":     networkID,
			"revision":       rev,
			"observed_at":    time.Now().UTC(),
			"ipv6_addresses": snapshot,
		}
		bodyBytes, _ := json.Marshal(payload)
		reqURL := fmt.Sprintf("%s/api/v1/devices/%s/network-state", strings.TrimRight(serverURL, "/"), deviceID)

		go func() {
			backoff := 1 * time.Second
			for attempt := 0; attempt < 5; attempt++ {
				req, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, bytes.NewReader(bodyBytes))
				if err != nil {
					return
				}
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")

				resp, err := client.Do(req)
				if err == nil {
					resp.Body.Close()
					if resp.StatusCode < 400 {
						if logger != nil {
							logger.Info("ipv6_state_reported", "device_id", deviceID, "revision", rev, "addresses", len(snapshot))
						}
						return
					}
					if logger != nil {
						logger.Warn("server_rejected_ipv6_state", "status", resp.StatusCode)
					}
				} else if logger != nil {
					logger.Warn("failed_to_report_ipv6_state_retrying", "device_id", deviceID, "error", err, "retry_in", backoff)
				}

				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
					backoff *= 2
				}
			}
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


func startRouterPrefixReporter(ctx context.Context, serverURL, token, routerID, networkID, iface string, logger *slog.Logger) {
	var revision atomic.Uint64
	client := &http.Client{Timeout: 10 * time.Second}
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

			rev := revision.Add(1)
			payload := map[string]any{
				"network_id":  networkID,
				"revision":    rev,
				"observed_at": time.Now().UTC(),
				"prefixes":    prefixes,
			}
			bodyBytes, _ := json.Marshal(payload)
			reqURL := fmt.Sprintf("%s/api/v1/devices/%s/network-prefixes", strings.TrimRight(serverURL, "/"), routerID)

			go func() {
				backoff := 1 * time.Second
				for attempt := 0; attempt < 5; attempt++ {
					req, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, bytes.NewReader(bodyBytes))
					if err != nil {
						return
					}
					req.Header.Set("Authorization", "Bearer "+token)
					req.Header.Set("Content-Type", "application/json")

					resp, err := client.Do(req)
					if err == nil {
						resp.Body.Close()
						if resp.StatusCode < 400 {
							if logger != nil {
								logger.Info("router_prefixes_reported", "router_id", routerID, "network_id", networkID, "revision", rev, "prefixes", len(prefixes))
							}
							return
						}
						if logger != nil {
							logger.Warn("server_rejected_router_prefixes", "status", resp.StatusCode)
						}
					} else if logger != nil {
						logger.Warn("failed_to_report_router_prefixes_retrying", "router_id", routerID, "error", err, "retry_in", backoff)
					}

					select {
					case <-ctx.Done():
						return
					case <-time.After(backoff):
						backoff *= 2
					}
				}
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

func localDevice(sshUser string, port int) (device.Device, string, error) {
	host, err := os.Hostname()
	if err != nil {
		return device.Device{}, "", err
	}
	current, err := user.Current()
	if err != nil {
		return device.Device{}, "", err
	}
	if sshUser == "" {
		sshUser = current.Username
		if i := strings.LastIndexAny(sshUser, "\\/"); i >= 0 {
			sshUser = sshUser[i+1:]
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
	return device.Device{ID: device.GenerateID(host, machineID), Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH, SSHUser: sshUser, SSHPort: port, Addresses: localAddresses()}, filepath.Join(home, ".ssh", "id_ed25519"), nil
}

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
			values = append(values, host)
		}
	}
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
