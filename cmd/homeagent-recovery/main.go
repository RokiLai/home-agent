// Command homeagent-recovery 是 macOS 下与主 App 分离的独立恢复器入口。
// 由 LaunchAgent (com.homeagent.recovery) 在开机或定时间隔调度执行，
// 负责校验升级事务日志、监控主进程健康并在收敛失败时安全回滚旧版本。
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"homeagent/internal/daemon/upgrade"
	"homeagent/internal/version"
)

func main() {
	var (
		journalDir = flag.String("journal-dir", "", "Path to upgrade journal directory")
		showInfo   = flag.Bool("info", false, "Print recovery binary info and exit")
	)
	flag.Parse()

	if *showInfo {
		fmt.Printf(`{"component":"homeagent-recovery","version":%q}`+"\n", version.Get())
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dir := *journalDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			logger.Error("failed_to_resolve_home_dir", "error", err)
			os.Exit(1)
		}
		dir = filepath.Join(home, "Library", "Application Support", "HomeAgent", "runtime", "transactions")
	}

	j, err := upgrade.OpenJournal(dir)
	if err != nil {
		logger.Error("failed_to_open_upgrade_journal", "dir", dir, "error", err)
		os.Exit(1)
	}

	state := j.GetState()
	logger.Info("recovery_cycle_inspected_state",
		"generation", state.Generation,
		"last_revision", state.LastJournalRevision,
		"command_id", state.CommandID,
		"current_state", state.CurrentState,
	)
}
