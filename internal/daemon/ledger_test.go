package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCommandLedgerPersistsAndSuppressesDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openCommandLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	_, execute, err := l.begin("cmd_1", "shutdown", `{}`)
	if err != nil || !execute {
		t.Fatalf("begin: %v %v", execute, err)
	}
	if started, err := l.start("cmd_1"); err != nil || !started {
		t.Fatalf("start: %v %v", started, err)
	}
	ack := []byte(`{"command_id":"cmd_1","status":"succeeded"}`)
	if err = l.commit("cmd_1", ack); err != nil {
		t.Fatal(err)
	}
	reopened, err := openCommandLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	record, execute, err := reopened.begin("cmd_1", "shutdown", `{}`)
	var want, got any
	_ = json.Unmarshal(ack, &want)
	_ = json.Unmarshal(record.Ack, &got)
	if err != nil || execute || record.Stage != "result_committed" || !reflect.DeepEqual(got, want) {
		t.Fatalf("duplicate: %#v execute=%v err=%v", record, execute, err)
	}
	if err = reopened.confirm("cmd_1"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %o", info.Mode().Perm())
	}
}

func TestCommandLedgerPermissionPolicyIsPlatformAware(t *testing.T) {
	if err := validateCommandLedgerMode("windows", 0666); err != nil {
		t.Fatalf("Windows ACL-backed file must not be rejected by POSIX mode bits: %v", err)
	}
	if err := validateCommandLedgerMode("linux", 0666); err == nil {
		t.Fatal("POSIX ledger with group/other permissions must be rejected")
	}
	if err := validateCommandLedgerMode("darwin", 0600); err != nil {
		t.Fatalf("POSIX ledger with mode 0600 must be accepted: %v", err)
	}
}

func TestV1AckUsesCanonicalStatusAndCommandID(t *testing.T) {
	var received map[string]any
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})}
	ledger, _ := openCommandLedger("")
	d := &Daemon{cfg: Config{ServerURL: "http://server.invalid", DeviceID: "dev", Token: "token", HTTPClient: client}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), activeServerURL: "http://server.invalid", ledger: ledger}
	ctx := context.WithValue(context.Background(), commandIDContextKey{}, "cmd_1")
	_, _, _ = ledger.begin("cmd_1", "upgrade", `{}`)
	_, _ = ledger.start("cmd_1")
	if err := d.sendAck(ctx, "upgrade", "upgraded", 0, "", ""); err != nil {
		t.Fatal(err)
	}
	if received["command_id"] != "cmd_1" || received["status"] != "succeeded" || received["protocol"] != float64(1) {
		t.Fatalf("unexpected v1 ACK: %v", received)
	}
}
