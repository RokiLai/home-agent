package sshsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"homeagent/internal/acl"
	"homeagent/internal/device"
	"homeagent/internal/registry"
)

func EnsureAdminKey(privatePath string) (string, error) {
	publicPath := privatePath + ".pub"
	if b, err := os.ReadFile(publicPath); err == nil {
		return string(bytes.TrimSpace(b)), nil
	}
	if err := os.MkdirAll(filepath.Dir(privatePath), 0700); err != nil {
		return "", err
	}
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-f", privatePath, "-N", "", "-C", "homeagent-admin")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ssh-keygen: %w: %s", err, bytes.TrimSpace(out))
	}
	if err := os.Chmod(privatePath, 0600); err != nil {
		return "", err
	}
	if err := os.Chmod(publicPath, 0644); err != nil {
		return "", err
	}
	b, err := os.ReadFile(publicPath)
	return string(bytes.TrimSpace(b)), err
}

type Controller struct {
	Registry                        *registry.Registry
	ACLPath, PrivateKey, KnownHosts string
	AdminPublicKey                  string
	Timeout                         time.Duration
	locks                           sync.Map
}

type Result struct {
	DeviceID string `json:"device_id"`
	Address  string `json:"address,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (c *Controller) SyncAll(ctx context.Context) []Result {
	all := c.Registry.List()
	results := make([]Result, len(all))
	var wg sync.WaitGroup
	for i, target := range all {
		wg.Add(1)
		go func(i int, d device.Device) { defer wg.Done(); results[i] = c.SyncDevice(ctx, d.ID) }(i, target)
	}
	wg.Wait()
	return results
}

func (c *Controller) SyncDevice(ctx context.Context, id string) Result {
	lockValue, _ := c.locks.LoadOrStore(id, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	target, err := c.Registry.Get(id)
	if err != nil {
		return Result{DeviceID: id, Error: err.Error()}
	}
	policy, err := acl.Load(c.ACLPath)
	if err != nil {
		return Result{DeviceID: id, Error: err.Error()}
	}
	allowed := policy.Resolve(id, c.Registry.List())
	payload := KeySet{Keys: make([]Key, 0, len(allowed)+1)}
	if c.AdminPublicKey != "" {
		payload.Keys = append(payload.Keys, Key{DeviceID: "homeagent-admin", PublicKey: c.AdminPublicKey})
	}
	for _, d := range allowed {
		payload.Keys = append(payload.Keys, Key{DeviceID: d.ID, PublicKey: d.PublicKey})
	}
	b, _ := json.Marshal(payload)
	address, err := c.probe(ctx, target)
	if err != nil {
		return Result{DeviceID: id, Error: err.Error()}
	}
	if err := c.ensureHostKey(ctx, address, target.SSHPort); err != nil {
		return Result{DeviceID: id, Address: address, Error: err.Error()}
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	host := net.JoinHostPort(address, strconv.Itoa(target.SSHPort))
	knownHostsOption, err := sshPathOption("UserKnownHostsFile", c.KnownHosts)
	if err != nil {
		return Result{DeviceID: id, Address: host, Error: err.Error()}
	}
	cmd := exec.CommandContext(commandCtx, "ssh", "-T", "-i", c.PrivateKey, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", knownHostsOption, "-p", strconv.Itoa(target.SSHPort), target.SSHUser+"@"+address, "homeagent-agent", "apply-keys")
	cmd.Stdin = bytes.NewReader(b)
	if out, err := cmd.CombinedOutput(); err != nil {
		return Result{DeviceID: id, Address: host, Error: fmt.Sprintf("apply keys: %v: %s", err, bytes.TrimSpace(out))}
	}
	return Result{DeviceID: id, Address: host}
}

func (c *Controller) probe(ctx context.Context, target device.Device) (string, error) {
	timeout := c.Timeout
	if timeout <= 0 || timeout > 3*time.Second {
		timeout = 3 * time.Second
	}
	d := net.Dialer{Timeout: timeout}
	for _, address := range device.FilterAndSortAddresses(target.Addresses) {
		conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(address, strconv.Itoa(target.SSHPort)))
		if err == nil {
			conn.Close()
			return address, nil
		}
	}
	return "", fmt.Errorf("no reachable SSH address for %s", target.ID)
}

func (c *Controller) ensureHostKey(ctx context.Context, address string, port int) error {
	if err := os.MkdirAll(filepath.Dir(c.KnownHosts), 0700); err != nil {
		return err
	}
	if _, err := os.Stat(c.KnownHosts); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(c.KnownHosts, nil, 0600); err != nil {
			return err
		}
	}
	lookup := exec.CommandContext(ctx, "ssh-keygen", "-F", knownHostName(address, port), "-f", c.KnownHosts)
	if lookup.Run() == nil {
		return nil
	}
	cmd := exec.CommandContext(ctx, "ssh-keyscan", "-T", "3", "-p", strconv.Itoa(port), address)
	out, err := cmd.Output()
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return fmt.Errorf("scan SSH host key: %w", err)
	}
	f, err := os.OpenFile(c.KnownHosts, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(out); err != nil {
		return err
	}
	return f.Sync()
}

func knownHostName(address string, port int) string {
	if port == 22 {
		return address
	}
	return net.JoinHostPort(address, strconv.Itoa(port))
}

func sshPathOption(name, path string) (string, error) {
	if strings.ContainsAny(path, "\r\n\x00") {
		return "", fmt.Errorf("invalid %s path", name)
	}
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(path)
	return name + `="` + escaped + `"`, nil
}
