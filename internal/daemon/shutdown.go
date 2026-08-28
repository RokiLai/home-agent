// Package daemon 提供 Agent 守护进程核心运行逻辑与系统服务控制。
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ShutdownPayload 表示通过 SSE 下发给 Agent 的关机指令载荷。
type ShutdownPayload struct {
	Reason       string `json:"reason,omitempty"`
	DelaySeconds int    `json:"delay_seconds,omitempty"`
	Force        bool   `json:"force,omitempty"`
}

// CommandRunner 是底层命令执行函数的签名，便于在单元测试中注入模拟执行。
type CommandRunner func(name string, args ...string) error

var defaultCommandRunner CommandRunner = func(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Run()
}

// GetShutdownCommands 返回当前操作系统平台上按优先级排列的关机命令及参数列表。
func GetShutdownCommands(goos string, force bool) [][]string {
	switch strings.ToLower(goos) {
	case "windows":
		flag := "/s"
		if force {
			return [][]string{
				{"shutdown.exe", flag, "/f", "/t", "1", "/c", "HomeAgent remote shutdown"},
				{"shutdown", flag, "/f", "/t", "1"},
			}
		}
		return [][]string{
			{"shutdown.exe", flag, "/t", "1", "/c", "HomeAgent remote shutdown"},
			{"shutdown", flag, "/t", "1"},
		}
	case "darwin":
		return [][]string{
			{"shutdown", "-h", "now"},
			{"osascript", "-e", `tell application "System Events" to shut down`},
		}
	case "linux":
		fallthrough
	default:
		return [][]string{
			{"systemctl", "poweroff"},
			{"poweroff"},
			{"shutdown", "-h", "now"},
		}
	}
}

// ExecuteShutdown 按平台优先级执行系统关机命令，返回首个执行成功或最终失败的错误。
func ExecuteShutdown(ctx context.Context, goos string, force bool, runner CommandRunner) error {
	if runner == nil {
		runner = defaultCommandRunner
	}
	commands := GetShutdownCommands(goos, force)
	var lastErr error
	for _, cmdParts := range commands {
		if len(cmdParts) == 0 {
			continue
		}
		err := runner(cmdParts[0], cmdParts[1:]...)
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return fmt.Errorf("all shutdown commands failed for %s: %w", goos, lastErr)
}

// ScheduleShutdown 异步等待指定延迟后执行关机操作。
func ScheduleShutdown(ctx context.Context, payload ShutdownPayload, logger *slog.Logger, runner CommandRunner) {
	ScheduleShutdownWithResult(ctx, payload, logger, runner, nil)
}

// ScheduleShutdownWithResult reports whether the OS shutdown command was successfully started.
func ScheduleShutdownWithResult(ctx context.Context, payload ShutdownPayload, logger *slog.Logger, runner CommandRunner, done func(error)) {
	delay := time.Duration(payload.DelaySeconds) * time.Second
	if delay <= 0 {
		delay = 1 * time.Second
	}

	go func() {
		if logger != nil {
			logger.Info("shutdown_scheduled", "delay", delay, "reason", payload.Reason, "force", payload.Force)
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			if logger != nil {
				logger.Warn("shutdown_canceled_due_to_context", "error", ctx.Err())
			}
			if done != nil {
				done(ctx.Err())
			}
			return
		}

		err := ExecuteShutdown(context.Background(), runtime.GOOS, payload.Force, runner)
		if err != nil && logger != nil {
			logger.Error("execute_shutdown_failed", "error", err)
		}
		if done != nil {
			done(err)
		}
	}()
}
