package file

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"homeagent/internal/command"
)

func TestRepositoryLifecycleIdempotencyAndRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	req := command.CreateRequest{Kind: command.KindShutdown, DeviceID: "dev-1", RequestedBy: command.Actor{Type: "admin", ID: "admin"}, IdempotencyKey: "once", Request: json.RawMessage(`{"force":false}`), TimeoutPolicy: command.TimeoutPolicy{Accept: time.Second, Finish: time.Minute}}
	c, created, err := r.CreateOrGet(req)
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	same, created, err := r.CreateOrGet(req)
	if err != nil || created || same.ID != c.ID {
		t.Fatalf("idempotent create: %#v %v %v", same, created, err)
	}
	conflict := req
	conflict.Request = json.RawMessage(`{"force":true}`)
	if _, _, err = r.CreateOrGet(conflict); !errors.Is(err, command.ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
	c, err = r.Transition(c.ID, c.Revision, command.Transition{Status: command.StatusDispatching, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Transition(c.ID, c.Revision-1, command.Transition{Status: command.StatusDispatched}); !errors.Is(err, command.ErrConflict) {
		t.Fatalf("expected revision conflict, got %v", err)
	}
	if _, err = r.Transition(c.ID, c.Revision, command.Transition{Status: command.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	r2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r2.Get(c.ID)
	if err != nil || got.Status != command.StatusSucceeded {
		t.Fatalf("reload: %#v %v", got, err)
	}
}

func TestOpenRejectsUnsafePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"commands":[]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("expected unsafe permissions to be rejected")
	}
}

func TestInterruptNonTerminalPreservesTerminal(t *testing.T) {
	r, _ := Open(filepath.Join(t.TempDir(), "commands.json"))
	mk := func(device string) command.Command {
		c, _, e := r.CreateOrGet(command.CreateRequest{Kind: command.KindSSHKeys, DeviceID: device, RequestedBy: command.Actor{Type: "system", ID: "test"}})
		if e != nil {
			t.Fatal(e)
		}
		return c
	}
	pending := mk("a")
	terminal := mk("b")
	terminal, _ = r.Transition(terminal.ID, terminal.Revision, command.Transition{Status: command.StatusCanceled})
	n, e := r.InterruptNonTerminal(time.Now())
	if e != nil || n != 1 {
		t.Fatalf("interrupt: %d %v", n, e)
	}
	got, _ := r.Get(pending.ID)
	if got.Status != command.StatusInterrupted {
		t.Fatalf("got %s", got.Status)
	}
	got, _ = r.Get(terminal.ID)
	if got.Status != command.StatusCanceled {
		t.Fatalf("terminal changed: %s", got.Status)
	}
}

func TestListUsesStableCursor(t *testing.T) {
	r, _ := Open(filepath.Join(t.TempDir(), "commands.json"))
	for _, id := range []string{"a", "b", "c"} {
		if _, _, err := r.CreateOrGet(command.CreateRequest{Kind: command.KindSSHKeys, DeviceID: id, RequestedBy: command.Actor{Type: "admin", ID: "a"}}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := r.List(command.Filter{Limit: 2})
	if err != nil || len(first.Commands) != 2 || first.NextCursor == "" {
		t.Fatalf("first page: %#v %v", first, err)
	}
	second, err := r.List(command.Filter{Limit: 2, Cursor: first.NextCursor})
	if err != nil || len(second.Commands) != 1 {
		t.Fatalf("second page: %#v %v", second, err)
	}
	seen := map[command.ID]bool{}
	for _, c := range append(first.Commands, second.Commands...) {
		if seen[c.ID] {
			t.Fatalf("duplicate %s", c.ID)
		}
		seen[c.ID] = true
	}
}
