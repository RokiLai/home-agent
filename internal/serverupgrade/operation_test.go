package serverupgrade

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestOperationManagerPersistsAndConvergesAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server-upgrade.json")
	manager, err := NewOperationManager(path, "v0.6.14")
	if err != nil {
		t.Fatal(err)
	}
	op, reused, err := manager.Start("owner-1", "v0.6.15")
	if err != nil || reused || op.Status != OperationPrepared {
		t.Fatalf("unexpected start: %+v reused=%v err=%v", op, reused, err)
	}
	if err := manager.Run(context.Background(), op.ID, func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	runs := 0
	if err := manager.Run(context.Background(), op.ID, func(context.Context, string) error { runs++; return nil }); err == nil {
		t.Fatal("replayed operation must not run replacement again")
	}
	if runs != 0 {
		t.Fatalf("replayed runner executed %d times", runs)
	}
	if got, _ := manager.Get(op.ID); got.Status != OperationRestarting {
		t.Fatalf("status = %s, want restarting", got.Status)
	}
	restarted, err := NewOperationManager(path, "v0.6.15")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := restarted.Get(op.ID); got.Status != OperationSucceeded {
		t.Fatalf("restarted status = %s, want succeeded", got.Status)
	}
}

func TestOperationManagerRejectsConflictingTargetAndRecordsFailure(t *testing.T) {
	manager, err := NewOperationManager(filepath.Join(t.TempDir(), "server-upgrade.json"), "v0.6.14")
	if err != nil {
		t.Fatal(err)
	}
	op, _, _ := manager.Start("owner-1", "v0.6.15")
	if same, reused, err := manager.Start("owner-1", "v0.6.15"); err != nil || !reused || same.ID != op.ID {
		t.Fatalf("same target must reuse operation: %+v %v %v", same, reused, err)
	}
	if _, _, err := manager.Start("owner-1", "v0.6.16"); !errors.Is(err, ErrOperationInProgress) {
		t.Fatalf("different target error = %v", err)
	}
	if err := manager.Run(context.Background(), op.ID, func(context.Context, string) error { return errors.New("checksum mismatch") }); err == nil {
		t.Fatal("runner failure must be returned")
	}
	if got, _ := manager.Get(op.ID); got.Status != OperationFailed || got.ErrorCode != "upgrade_failed" {
		t.Fatalf("failure not persisted: %+v", got)
	}
}

func TestOperationManagerConvergesToRolledBackOnPreviousVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server-upgrade.json")
	manager, err := NewOperationManager(path, "v0.6.14")
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := manager.Start("owner-1", "v0.6.15")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Run(context.Background(), op.ID, func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}

	// 模拟升级失败被 supervisor 还原回旧版本拉起
	restarted, err := NewOperationManager(path, "v0.6.14")
	if err != nil {
		t.Fatal(err)
	}
	got, err := restarted.Get(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != OperationRolledBack || got.ErrorCode != "rollback_detected" {
		t.Fatalf("restarted status = %s, code = %s, want rolled_back", got.Status, got.ErrorCode)
	}

	// 模拟重启为未预期的第三方版本
	pathUnexpected := filepath.Join(t.TempDir(), "server-upgrade-unexpected.json")
	manager2, _ := NewOperationManager(pathUnexpected, "v0.6.14")
	op2, _, _ := manager2.Start("owner-1", "v0.6.15")
	_ = manager2.Run(context.Background(), op2.ID, func(context.Context, string) error { return nil })
	restarted2, _ := NewOperationManager(pathUnexpected, "v0.6.99")
	got2, _ := restarted2.Get(op2.ID)
	if got2.Status != OperationFailed || got2.ErrorCode != "version_mismatch" {
		t.Fatalf("unexpected version status = %s, code = %s, want failed", got2.Status, got2.ErrorCode)
	}
}
