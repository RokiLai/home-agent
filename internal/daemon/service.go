package daemon

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

const macOSBundleIdentifier = "com.homeagent.app"

type ServiceManager struct {
	BinaryPath string
	ServerURL  string
	Token      string
}

func NewServiceManager(binaryPath, serverURL, token string) (*ServiceManager, error) {
	if binaryPath == "" {
		p, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("detect executable: %w", err)
		}
		binaryPath = p
	}
	absBinary, err := filepath.Abs(binaryPath)
	if err != nil {
		return nil, err
	}
	return &ServiceManager{
		BinaryPath: absBinary,
		ServerURL:  serverURL,
		Token:      token,
	}, nil
}

func (s *ServiceManager) Install() error {
	switch runtime.GOOS {
	case "darwin":
		return s.installLaunchd()
	case "linux":
		if isOpenWrt() {
			return s.installProcd()
		}
		return s.installSystemd()
	case "windows":
		return s.installWindowsTask()
	default:
		return fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}
}

func (s *ServiceManager) Uninstall() error {
	switch runtime.GOOS {
	case "darwin":
		return s.uninstallLaunchd()
	case "linux":
		if isOpenWrt() {
			return s.uninstallProcd()
		}
		return s.uninstallSystemd()
	case "windows":
		return s.uninstallWindowsTask()
	default:
		return fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}
}

func (s *ServiceManager) Start() error {
	switch runtime.GOOS {
	case "darwin":
		plist := s.launchdPlistPath()
		uid := fmt.Sprintf("gui/%d", os.Getuid())
		if err := runCmd("launchctl", "bootstrap", uid, plist); err != nil {
			return runCmd("launchctl", "load", "-w", plist)
		}
		return nil
	case "linux":
		if isOpenWrt() {
			return runCmd("/etc/init.d/homeagent-agent", "start")
		}
		return runCmd("systemctl", "--user", "start", "homeagent-agent.service")
	case "windows":
		return runCmd("schtasks.exe", "/Run", "/TN", "HomeAgent")
	default:
		return fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}
}

func (s *ServiceManager) Stop() error {
	switch runtime.GOOS {
	case "darwin":
		plist := s.launchdPlistPath()
		uid := fmt.Sprintf("gui/%d", os.Getuid())
		_ = runCmd("launchctl", "bootout", uid+"/com.homeagent.agent")
		_ = runCmd("launchctl", "unload", "-w", plist)
		return nil
	case "linux":
		if isOpenWrt() {
			return runCmd("/etc/init.d/homeagent-agent", "stop")
		}
		return runCmd("systemctl", "--user", "stop", "homeagent-agent.service")
	case "windows":
		return runCmd("schtasks.exe", "/End", "/TN", "HomeAgent")
	default:
		return fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}
}

func (s *ServiceManager) Status() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("launchctl", "list", "com.homeagent.agent").CombinedOutput()
		if err != nil {
			return "stopped", nil
		}
		return "running:\n" + string(out), nil
	case "linux":
		if isOpenWrt() {
			out, _ := exec.Command("/etc/init.d/homeagent-agent", "status").CombinedOutput()
			return string(out), nil
		}
		out, err := exec.Command("systemctl", "--user", "status", "homeagent-agent.service").CombinedOutput()
		if err != nil {
			return "inactive / stopped", nil
		}
		return string(out), nil
	case "windows":
		out, err := exec.Command("schtasks.exe", "/Query", "/TN", "HomeAgent").CombinedOutput()
		if err != nil {
			return "not found / stopped", nil
		}
		return string(out), nil
	default:
		return "unknown", nil
	}
}

// macOS launchd
func (s *ServiceManager) launchdPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", "com.homeagent.agent.plist")
}

func (s *ServiceManager) installLaunchd() error {
	plistPath := s.launchdPlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
		return err
	}

	plistContent := s.launchdPlistContent()

	if err := os.WriteFile(plistPath, []byte(plistContent), 0644); err != nil {
		return err
	}
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = runCmd("launchctl", "bootout", uid+"/com.homeagent.agent")
	_ = runCmd("launchctl", "unload", "-w", plistPath)
	if err := runCmd("launchctl", "bootstrap", uid, plistPath); err != nil {
		return runCmd("launchctl", "load", "-w", plistPath)
	}
	return nil
}

func (s *ServiceManager) launchdPlistContent() string {
	escape := func(value string) string {
		var out bytes.Buffer
		_ = xml.EscapeText(&out, []byte(value))
		return out.String()
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.homeagent.agent</string>
    <key>AssociatedBundleIdentifiers</key>
    <array>
        <string>%s</string>
    </array>
    <key>LimitLoadToSessionType</key>
    <string>Aqua</string>
    <key>ProcessType</key>
    <string>Interactive</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>daemon</string>
        <string>--server</string>
        <string>%s</string>
        <string>--token</string>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/homeagent-agent.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/homeagent-agent.err</string>
</dict>
</plist>
`, macOSBundleIdentifier, escape(s.BinaryPath), escape(s.ServerURL), escape(s.Token))
}

func (s *ServiceManager) uninstallLaunchd() error {
	plistPath := s.launchdPlistPath()
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = runCmd("launchctl", "bootout", uid+"/com.homeagent.agent")
	_ = runCmd("launchctl", "unload", "-w", plistPath)
	return os.Remove(plistPath)
}

// Linux systemd
func (s *ServiceManager) systemdServicePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", "homeagent-agent.service")
}

func (s *ServiceManager) installSystemd() error {
	svcPath := s.systemdServicePath()
	if err := os.MkdirAll(filepath.Dir(svcPath), 0755); err != nil {
		return err
	}

	content := fmt.Sprintf(`[Unit]
Description=HomeAgent Client Daemon
After=network.target

[Service]
Type=simple
ExecStart=%s daemon --server "%s" --token "%s"
Restart=always
RestartSec=5s
Environment="GOMEMLIMIT=15MiB"

[Install]
WantedBy=default.target
`, s.BinaryPath, s.ServerURL, s.Token)

	if err := os.WriteFile(svcPath, []byte(content), 0644); err != nil {
		return err
	}
	_ = runCmd("systemctl", "--user", "daemon-reload")
	return runCmd("systemctl", "--user", "enable", "--now", "homeagent-agent.service")
}

func (s *ServiceManager) uninstallSystemd() error {
	svcPath := s.systemdServicePath()
	_ = runCmd("systemctl", "--user", "disable", "--now", "homeagent-agent.service")
	return os.Remove(svcPath)
}

// OpenWrt procd
func (s *ServiceManager) installProcd() error {
	initScript := "/etc/init.d/homeagent-agent"
	content := fmt.Sprintf(`#!/bin/sh /etc/rc.common
USE_PROCD=1
START=99
STOP=10

start_service() {
    procd_open_instance
    procd_set_param command %s daemon --server "%s" --token "%s"
    procd_set_param respawn 3600 5 0
    procd_set_param env GOMEMLIMIT=10MiB
    procd_set_param stdout 1
    procd_set_param stderr 1
    procd_close_instance
}
`, s.BinaryPath, s.ServerURL, s.Token)

	if err := os.WriteFile(initScript, []byte(content), 0755); err != nil {
		return err
	}
	_ = runCmd(initScript, "enable")
	return runCmd(initScript, "start")
}

func (s *ServiceManager) uninstallProcd() error {
	initScript := "/etc/init.d/homeagent-agent"
	_ = runCmd(initScript, "stop")
	_ = runCmd(initScript, "disable")
	return os.Remove(initScript)
}

// Windows Task Scheduler
func (s *ServiceManager) installWindowsTask() error {
	userObj, err := user.Current()
	username := ""
	if err == nil {
		username = userObj.Username
	}
	cmdArgs := fmt.Sprintf(`"%s" daemon --server "%s" --token "%s"`, s.BinaryPath, s.ServerURL, s.Token)
	args := []string{"/Create", "/TN", "HomeAgent", "/TR", cmdArgs, "/SC", "ONLOGON", "/RL", "HIGHEST", "/F"}
	if username != "" {
		args = append(args, "/RU", username)
	}
	if err := runCmd("schtasks.exe", args...); err != nil {
		return err
	}
	_ = runCmd("schtasks.exe", "/Run", "/TN", "HomeAgent")
	return nil
}

func (s *ServiceManager) uninstallWindowsTask() error {
	return runCmd("schtasks.exe", "/Delete", "/TN", "HomeAgent", "/F")
}

func isOpenWrt() bool {
	if _, err := os.Stat("/etc/openwrt_release"); err == nil {
		return true
	}
	if _, err := os.Stat("/etc/init.d/procd"); err == nil {
		return true
	}
	return false
}

func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("execute %s %s: %w: %s", name, strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
}
