package command_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"homeagent/internal/command"
	commandfile "homeagent/internal/command/file"
)

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

func TestLateAckIsAuditedWithoutChangingTerminalState(t *testing.T) {
	repo, err := commandfile.Open(filepath.Join(t.TempDir(), "commands.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := command.NewService(repo, nil)
	c, _, err := svc.Create(command.CreateRequest{Kind: command.KindShutdown, DeviceID: "dev", RequestedBy: command.Actor{Type: "admin", ID: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err = svc.Cancel(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Finish(c.ID, command.StatusSucceeded, json.RawMessage(`{"os_command_started":true}`), "", "")
	if !errors.Is(err, command.ErrLateAckAccepted) {
		t.Fatalf("expected late audit, got %v", err)
	}
	if got.Status != command.StatusCanceled || len(got.LateAcks) != 1 {
		t.Fatalf("late ack changed terminal state: %#v", got)
	}
	again, err := svc.Finish(c.ID, command.StatusSucceeded, json.RawMessage(`{"os_command_started":true}`), "", "")
	if !errors.Is(err, command.ErrLateAckAccepted) || len(again.LateAcks) != 1 {
		t.Fatalf("late replay not idempotent: %#v %v", again, err)
	}
	if _, err = svc.Finish(c.ID, command.StatusFailed, json.RawMessage(`{"os_command_started":false}`), "failed", "different"); !errors.Is(err, command.ErrInvalidTransition) {
		t.Fatalf("expected conflicting late ack rejection, got %v", err)
	}
}

func TestAcceptDeadlineTimeout(t *testing.T) {
	repo, _ := commandfile.Open(filepath.Join(t.TempDir(), "commands.json"))
	clock := &fixedClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	svc := command.NewService(repo, clock)
	c, _, err := svc.Create(command.CreateRequest{Kind: command.KindUpgrade, DeviceID: "dev", RequestedBy: command.Actor{Type: "admin", ID: "a"}, TimeoutPolicy: command.TimeoutPolicy{Accept: time.Second, Finish: time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	c, err = svc.StartDispatch(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	c, err = svc.DispatchResult(c.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(2 * time.Second)
	n, err := svc.Expire(10)
	if err != nil || n != 1 {
		t.Fatalf("expire: %d %v", n, err)
	}
	c, _ = svc.Get(c.ID)
	if c.Status != command.StatusTimedOut {
		t.Fatalf("status %s", c.Status)
	}
}

func TestUpdateProgress(t *testing.T) {
	repo, err := commandfile.Open(filepath.Join(t.TempDir(), "commands.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := command.NewService(repo, nil)
	c, _, err := svc.Create(command.CreateRequest{
		Kind:        command.KindUpgrade,
		DeviceID:    "dev-upg",
		RequestedBy: command.Actor{Type: "admin", ID: "a"},
	})
	if err != nil {
		t.Fatal(err)
	}

	progress := command.UpgradeProgress{
		Phase:           "downloading",
		Sequence:        1,
		OccurredAt:      time.Now().UTC(),
		DetailCode:      "bytes_read",
		ConfirmedDigest: "",
	}
	updated, err := svc.UpdateProgress(c.ID, progress)
	if err != nil {
		t.Fatalf("UpdateProgress failed: %v", err)
	}
	if updated.Progress == nil || updated.Progress.Phase != "downloading" || updated.Progress.Sequence != 1 {
		t.Fatalf("unexpected progress on command: %+v", updated.Progress)
	}
}
