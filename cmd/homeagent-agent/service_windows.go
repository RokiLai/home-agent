//go:build windows

package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/svc"
)

const windowsServiceName = "HomeAgent"

// runServiceIfWindows 检测当前进程是否由 Windows 服务控制管理器（SCM）拉起启动；
// 若是则运行 Windows 服务事件处理循环。
func runServiceIfWindows() bool {
	isSvc, err := svc.IsWindowsService()
	if err != nil || !isSvc {
		return false
	}

	err = svc.Run(windowsServiceName, &agentWindowsService{})
	if err != nil {
		slog.Error("failed to run windows service", "error", err)
	}
	return true
}

// agentWindowsService 实现 golang.org/x/sys/windows/svc.Handler 接口，处理 Windows 服务事件。
type agentWindowsService struct{}

// Execute 处理 Windows 服务的生命周期状态流转与 SCM 控制指令。
func (s *agentWindowsService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {

	changes <- svc.Status{State: svc.StartPending}

	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	logDir := filepath.Join(base, "HomeAgent")
	_ = os.MkdirAll(logDir, 0755)
	logFile, err := os.OpenFile(filepath.Join(logDir, "homeagent-service.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	var logger *slog.Logger
	if err == nil {
		defer logFile.Close()
		logger = slog.New(slog.NewJSONHandler(logFile, nil))
	} else {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}

	logger.Info("starting HomeAgent windows service")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	daemonArgs := os.Args[1:]
	if len(daemonArgs) > 0 && daemonArgs[0] == "daemon" {
		daemonArgs = daemonArgs[1:]
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runDaemonWithContext(ctx, daemonArgs, logger)
	}()

	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	logger.Info("HomeAgent windows service running")

	for {
		select {
		case req := <-r:
			switch req.Cmd {
			case svc.Interrogate:
				changes <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				logger.Info("received service stop/shutdown signal")
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				<-errCh
				changes <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		case err := <-errCh:
			if err != nil {
				logger.Error("HomeAgent windows service daemon stopped with error", "error", err)
				changes <- svc.Status{State: svc.Stopped}
				return true, 1
			}
			changes <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}
