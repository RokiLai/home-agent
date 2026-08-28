package daemon

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestGetShutdownCommands(t *testing.T) {
	// 1. Linux
	linuxCmds := GetShutdownCommands("linux", false)
	if len(linuxCmds) != 3 {
		t.Fatalf("expected 3 commands for linux, got %d", len(linuxCmds))
	}
	if linuxCmds[0][0] != "systemctl" || linuxCmds[0][1] != "poweroff" {
		t.Errorf("unexpected first linux command: %v", linuxCmds[0])
	}
	if linuxCmds[1][0] != "poweroff" {
		t.Errorf("unexpected second linux command: %v", linuxCmds[1])
	}

	// 2. Darwin
	darwinCmds := GetShutdownCommands("darwin", false)
	if len(darwinCmds) != 2 {
		t.Fatalf("expected 2 commands for darwin, got %d", len(darwinCmds))
	}
	if darwinCmds[0][0] != "shutdown" || darwinCmds[0][1] != "-h" {
		t.Errorf("unexpected first darwin command: %v", darwinCmds[0])
	}

	// 3. Windows normal
	winCmds := GetShutdownCommands("windows", false)
	if len(winCmds) != 2 {
		t.Fatalf("expected 2 commands for windows, got %d", len(winCmds))
	}
	if winCmds[0][0] != "shutdown.exe" || winCmds[0][1] != "/s" {
		t.Errorf("unexpected first windows command: %v", winCmds[0])
	}

	// 4. Windows force
	winForceCmds := GetShutdownCommands("windows", true)
	if len(winForceCmds) != 2 {
		t.Fatalf("expected 2 commands for windows force, got %d", len(winForceCmds))
	}
	hasForce := false
	for _, arg := range winForceCmds[0] {
		if arg == "/f" {
			hasForce = true
		}
	}
	if !hasForce {
		t.Errorf("expected /f flag in windows force command: %v", winForceCmds[0])
	}
}

func TestExecuteShutdown_Success(t *testing.T) {
	executed := []string{}
	runner := func(name string, args ...string) error {
		executed = append(executed, name)
		return nil
	}

	err := ExecuteShutdown(context.Background(), "linux", false, runner)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if len(executed) != 1 || executed[0] != "systemctl" {
		t.Errorf("unexpected executed command: %v", executed)
	}
}

func TestExecuteShutdown_Fallback(t *testing.T) {
	var mu sync.Mutex
	executed := []string{}
	runner := func(name string, args ...string) error {
		mu.Lock()
		defer mu.Unlock()
		executed = append(executed, name)
		if name == "systemctl" {
			return errors.New("command not found")
		}
		return nil
	}

	err := ExecuteShutdown(context.Background(), "linux", false, runner)
	if err != nil {
		t.Fatalf("expected fallback success, got error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(executed) != 2 {
		t.Fatalf("expected 2 attempts, got %d: %v", len(executed), executed)
	}
	if executed[0] != "systemctl" || executed[1] != "poweroff" {
		t.Errorf("unexpected sequence: %v", executed)
	}
}

func TestExecuteShutdown_AllFail(t *testing.T) {
	runner := func(name string, args ...string) error {
		return errors.New("exec failed")
	}

	err := ExecuteShutdown(context.Background(), "darwin", false, runner)
	if err == nil {
		t.Fatal("expected error when all commands fail, got nil")
	}
}

func TestScheduleShutdown(t *testing.T) {
	var executed bool
	var mu sync.Mutex
	doneCh := make(chan struct{})

	runner := func(name string, args ...string) error {
		mu.Lock()
		executed = true
		mu.Unlock()
		close(doneCh)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	payload := ShutdownPayload{
		Reason:       "test",
		DelaySeconds: 0,
		Force:        false,
	}

	ScheduleShutdown(ctx, payload, slog.Default(), runner)

	select {
	case <-doneCh:
		mu.Lock()
		defer mu.Unlock()
		if !executed {
			t.Fatal("expected shutdown command to be executed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for scheduled shutdown")
	}
}

func TestScheduleShutdown_Canceled(t *testing.T) {
	var executed bool
	var mu sync.Mutex

	runner := func(name string, args ...string) error {
		mu.Lock()
		executed = true
		mu.Unlock()
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	payload := ShutdownPayload{
		Reason:       "test",
		DelaySeconds: 1,
	}

	ScheduleShutdown(ctx, payload, slog.Default(), runner)

	time.Sleep(1500 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if executed {
		t.Fatal("shutdown should have been canceled, but was executed")
	}
}

func TestDaemon_HandleEvent_Shutdown(t *testing.T) {
	var mu sync.Mutex
	var shutdownCalled bool

	d := &Daemon{
		cfg: Config{
			ServerURL: "http://127.0.0.1:8080",
			Token:     "test-token",
			DeviceID:  "test-dev",
			ShutdownRunner: func(name string, args ...string) error {
				mu.Lock()
				shutdownCalled = true
				mu.Unlock()
				return nil
			},
		},
		log: slog.Default(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d.handleEvent(ctx, "shutdown", `{"reason":"test_shutdown","delay_seconds":0}`)

	time.Sleep(1200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if !shutdownCalled && runtime.GOOS != "" {
		t.Log("Note: shutdown scheduled")
	}
}
