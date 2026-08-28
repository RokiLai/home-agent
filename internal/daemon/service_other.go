//go:build !windows

package daemon

import "fmt"

func formatWindowsServiceCommand(binaryPath, serverURL, token string) string {
	return fmt.Sprintf(`"%s" daemon --server "%s" --token "%s"`, binaryPath, serverURL, token)
}

func (s *ServiceManager) installWindowsService() error {
	return fmt.Errorf("windows service is only supported on windows")
}

func (s *ServiceManager) uninstallWindowsService() error {
	return fmt.Errorf("windows service is only supported on windows")
}

func (s *ServiceManager) startWindowsService() error {
	return fmt.Errorf("windows service is only supported on windows")
}

func (s *ServiceManager) stopWindowsService() error {
	return fmt.Errorf("windows service is only supported on windows")
}

func (s *ServiceManager) statusWindowsService() (string, error) {
	return "", fmt.Errorf("windows service is only supported on windows")
}
