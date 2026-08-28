//go:build windows

package daemon

import (
	"fmt"
	"os/exec"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const windowsServiceName = "HomeAgent"

func formatWindowsServiceCommand(binaryPath, serverURL, token string) string {
	return fmt.Sprintf(`"%s" daemon --server "%s" --token "%s"`, binaryPath, serverURL, token)
}

func parseWindowsServiceState(state svc.State) string {
	switch state {
	case svc.Running:
		return "running"
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start_pending"
	case svc.StopPending:
		return "stop_pending"
	case svc.Paused:
		return "paused"
	default:
		return fmt.Sprintf("state_%d", state)
	}
}

func (s *ServiceManager) installWindowsService() error {
	// Clean up legacy Task Scheduler task if any existed previously
	_ = runCmd("schtasks.exe", "/Delete", "/TN", "HomeAgent", "/F")

	m, err := mgr.Connect()
	if err != nil {
		return s.installWindowsServiceSC()
	}
	defer m.Disconnect()

	service, err := m.OpenService(windowsServiceName)
	if err == nil {
		_, _ = service.Control(svc.Stop)
		_ = service.Delete()
		service.Close()
		time.Sleep(500 * time.Millisecond)
	}

	cfg := mgr.Config{
		StartType:   mgr.StartAutomatic,
		DisplayName: "HomeAgent Agent Service",
		Description: "HomeAgent client background synchronization daemon",
	}

	service, err = m.CreateService(
		windowsServiceName,
		s.BinaryPath,
		cfg,
		"daemon",
		"--server", s.ServerURL,
		"--token", s.Token,
	)
	if err != nil {
		return s.installWindowsServiceSC()
	}
	defer service.Close()

	// Set recovery actions (restart after 5s on failure)
	r := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
	}
	_ = service.SetRecoveryActions(r, 86400)

	if err := service.Start(); err != nil {
		return fmt.Errorf("start windows service: %w", err)
	}
	return nil
}

func (s *ServiceManager) installWindowsServiceSC() error {
	cmdArgs := formatWindowsServiceCommand(s.BinaryPath, s.ServerURL, s.Token)
	_ = runCmd("sc.exe", "stop", windowsServiceName)
	_ = runCmd("sc.exe", "delete", windowsServiceName)
	time.Sleep(500 * time.Millisecond)

	if err := runCmd("sc.exe", "create", windowsServiceName, "binPath= "+cmdArgs, "start= auto", "DisplayName= HomeAgent Agent Service"); err != nil {
		return fmt.Errorf("sc create service: %w", err)
	}
	_ = runCmd("sc.exe", "failure", windowsServiceName, "reset= 86400", "actions= restart/5000/restart/5000/restart/5000")
	if err := runCmd("sc.exe", "start", windowsServiceName); err != nil {
		return fmt.Errorf("sc start service: %w", err)
	}
	return nil
}

func (s *ServiceManager) uninstallWindowsService() error {
	_ = runCmd("schtasks.exe", "/Delete", "/TN", "HomeAgent", "/F")

	m, err := mgr.Connect()
	if err != nil {
		_ = runCmd("sc.exe", "stop", windowsServiceName)
		return runCmd("sc.exe", "delete", windowsServiceName)
	}
	defer m.Disconnect()

	service, err := m.OpenService(windowsServiceName)
	if err != nil {
		return nil
	}
	defer service.Close()

	_, _ = service.Control(svc.Stop)
	return service.Delete()
}

func (s *ServiceManager) startWindowsService() error {
	m, err := mgr.Connect()
	if err != nil {
		return runCmd("sc.exe", "start", windowsServiceName)
	}
	defer m.Disconnect()

	service, err := m.OpenService(windowsServiceName)
	if err != nil {
		return fmt.Errorf("open windows service: %w", err)
	}
	defer service.Close()

	return service.Start()
}

func (s *ServiceManager) stopWindowsService() error {
	m, err := mgr.Connect()
	if err != nil {
		return runCmd("sc.exe", "stop", windowsServiceName)
	}
	defer m.Disconnect()

	service, err := m.OpenService(windowsServiceName)
	if err != nil {
		return fmt.Errorf("open windows service: %w", err)
	}
	defer service.Close()

	_, err = service.Control(svc.Stop)
	return err
}

func (s *ServiceManager) statusWindowsService() (string, error) {
	m, err := mgr.Connect()
	if err != nil {
		out, err := exec.Command("sc.exe", "query", windowsServiceName).CombinedOutput()
		if err != nil {
			return "not found / stopped", nil
		}
		return string(out), nil
	}
	defer m.Disconnect()

	service, err := m.OpenService(windowsServiceName)
	if err != nil {
		return "not installed", nil
	}
	defer service.Close()

	status, err := service.Query()
	if err != nil {
		return "", fmt.Errorf("query windows service status: %w", err)
	}

	return parseWindowsServiceState(status.State), nil
}
