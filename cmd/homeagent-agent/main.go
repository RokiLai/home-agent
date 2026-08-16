package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"homeagent/internal/device"
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

func usage() { fmt.Fprintln(os.Stderr, "usage: homeagent-agent <join|info|apply-keys>"); os.Exit(2) }

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
		return "", fmt.Errorf("read machine ID: %w", err)
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

func localAddresses() []string {
	var values []string
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
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
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" && strings.EqualFold(os.Getenv("USERNAME"), "Administrator") {
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "ssh", "administrators_authorized_keys"), nil
	}
	return filepath.Join(home, ".ssh", "authorized_keys"), nil
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".authorized_keys-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		if err := applyWindowsACL(name); err != nil {
			return err
		}
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	ok = true
	return nil
}

func applyWindowsACL(path string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	user := os.Getenv("USERNAME")
	cmd := exec.Command("icacls.exe", path, "/inheritance:r", "/grant:r", user+":F", "SYSTEM:F")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("set Windows ACL: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
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
