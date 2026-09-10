package servernetwork

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateStore_SaveAndLoad(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFileStateStore(tempDir)

	now := time.Date(2026, 9, 10, 15, 30, 0, 0, time.UTC)
	record := "server.example.com"
	rs := &RecordState{
		Record:           record,
		ConfigVersion:    1,
		DesiredAddress:   "240e:390:1::1",
		DesiredVersion:   2,
		ConfirmedAddress: "240e:390:1::1",
		ConfirmedVersion: 2,
		Status:           StatusSynced,
		LastSuccessTime:  &now,
		UpdatedAt:        now,
	}

	state := &PersistedState{
		Records: map[string]*RecordState{
			record: rs,
		},
	}

	if err := store.Save(state); err != nil {
		t.Fatalf("failed to save state: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("failed to load state: %v", err)
	}

	loadedRecord, ok := loaded.Records[record]
	if !ok {
		t.Fatalf("record %s not found in loaded state", record)
	}

	if loadedRecord.Status != StatusSynced {
		t.Errorf("expected status %s, got %s", StatusSynced, loadedRecord.Status)
	}
	if loadedRecord.ConfirmedAddress != "240e:390:1::1" {
		t.Errorf("expected confirmed address 240e:390:1::1, got %s", loadedRecord.ConfirmedAddress)
	}
	if loadedRecord.ConfirmedVersion != 2 {
		t.Errorf("expected confirmed version 2, got %d", loadedRecord.ConfirmedVersion)
	}
}

func TestStateStore_LoadNonExistent(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFileStateStore(tempDir)

	state, err := store.Load()
	if err != nil {
		t.Fatalf("expected no error loading non-existent state, got %v", err)
	}
	if state == nil || state.Records == nil {
		t.Fatalf("expected empty non-nil state")
	}
	if len(state.Records) != 0 {
		t.Errorf("expected 0 records, got %d", len(state.Records))
	}
}

func TestStateStore_AtomicSaveCorruptionResilience(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFileStateStore(tempDir)

	statePath := filepath.Join(tempDir, "server_network_state.json")
	// Write corrupted data
	if err := os.WriteFile(statePath, []byte("invalid-json"), 0600); err != nil {
		t.Fatalf("failed to write corrupted file: %v", err)
	}

	state, err := store.Load()
	if err != nil {
		t.Fatalf("expected error handling or fallback on corrupted file, got: %v", err)
	}
	if state == nil || state.Records == nil {
		t.Fatalf("expected fallback empty state on corruption")
	}
}

func TestStateStore_SaveNilOrEmpty(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFileStateStore(tempDir)

	if err := store.Save(nil); err != nil {
		t.Fatalf("expected save nil state to succeed, got %v", err)
	}

	loaded, err := store.Load()
	if err != nil || loaded == nil {
		t.Fatalf("expected valid empty loaded state, got %v", err)
	}
}

