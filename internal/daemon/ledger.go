package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

type ledgerRecord struct {
	CommandID string          `json:"command_id"`
	Kind      string          `json:"kind"`
	Stage     string          `json:"stage"`
	Ack       json.RawMessage `json:"ack,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	UpdatedAt time.Time       `json:"updated_at"`
}
type commandLedger struct {
	mu            sync.Mutex
	path          string
	SchemaVersion int                     `json:"schema_version"`
	Records       map[string]ledgerRecord `json:"records"`
}

func openCommandLedger(path string) (*commandLedger, error) {
	l := &commandLedger{path: path, SchemaVersion: 1, Records: map[string]ledgerRecord{}}
	if path == "" {
		return l, nil
	}
	b, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		return l, nil
	}
	if e != nil {
		return nil, e
	}
	if info, statErr := os.Stat(path); statErr != nil {
		return nil, statErr
	} else {
		if runtime.GOOS == "windows" {
			if err := applyWindowsACL(path); err != nil {
				return nil, fmt.Errorf("secure command ledger ACL: %w", err)
			}
		}
		if err := validateCommandLedgerMode(runtime.GOOS, info.Mode()); err != nil {
			return nil, err
		}
	}
	if e = json.Unmarshal(b, l); e != nil {
		return nil, e
	}
	if l.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported command ledger schema %d", l.SchemaVersion)
	}
	if l.Records == nil {
		l.Records = map[string]ledgerRecord{}
	}
	return l, nil
}
func (l *commandLedger) begin(id, kind, payload string) (ledgerRecord, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r, ok := l.Records[id]; ok {
		return r, false, nil
	}
	r := ledgerRecord{CommandID: id, Kind: kind, Stage: "prepared", Payload: json.RawMessage(append([]byte(nil), payload...)), UpdatedAt: time.Now().UTC()}
	l.Records[id] = r
	return r, true, l.writeLocked()
}
func (l *commandLedger) resetPrepared(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.Records[id]
	r.Stage = "prepared"
	r.UpdatedAt = time.Now().UTC()
	l.Records[id] = r
	return l.writeLocked()
}
func (l *commandLedger) interrupted() []ledgerRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]ledgerRecord, 0)
	for _, r := range l.Records {
		if r.Stage == "side_effect_started" {
			out = append(out, r)
		}
	}
	return out
}
func (l *commandLedger) start(id string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.Records[id]
	if !ok || r.Stage != "prepared" {
		return false, nil
	}
	r.Stage = "side_effect_started"
	r.UpdatedAt = time.Now().UTC()
	l.Records[id] = r
	return true, l.writeLocked()
}
func (l *commandLedger) commit(id string, ack []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.Records[id]
	r.CommandID = id
	r.Stage = "result_committed"
	r.Ack = append([]byte(nil), ack...)
	r.UpdatedAt = time.Now().UTC()
	l.Records[id] = r
	return l.writeLocked()
}
func (l *commandLedger) confirm(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.Records[id]
	r.Stage = "ack_confirmed"
	r.UpdatedAt = time.Now().UTC()
	l.Records[id] = r
	return l.writeLocked()
}
func (l *commandLedger) pendingAcks() []json.RawMessage {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]json.RawMessage, 0)
	for _, r := range l.Records {
		if r.Stage == "result_committed" && len(r.Ack) > 0 {
			out = append(out, append(json.RawMessage(nil), r.Ack...))
		}
	}
	return out
}
func (l *commandLedger) writeLocked() error {
	if l.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0700); err != nil {
		return err
	}
	b, e := json.MarshalIndent(l, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(l.path), ".command-ledger-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if runtime.GOOS == "windows" {
		if e = applyWindowsACL(name); e != nil {
			return fmt.Errorf("secure command ledger ACL: %w", e)
		}
	}
	return os.Rename(name, l.path)
}

func validateCommandLedgerMode(goos string, mode os.FileMode) error {
	if goos == "windows" {
		return nil
	}
	if mode.Perm()&0077 != 0 {
		return fmt.Errorf("unsafe command ledger permissions %o", mode.Perm())
	}
	return nil
}
