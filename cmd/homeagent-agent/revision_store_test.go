package main

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestRevisionStorePersistsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	key := revisionKey{ReportType: reportTypeRouterPrefixes, DeviceID: "router-1", NetworkID: "home"}
	store := newRevisionStore(root)
	if got, err := store.Allocate(key); err != nil || got != 1 {
		t.Fatalf("first Allocate = %d, %v", got, err)
	}
	if got, err := store.Allocate(key); err != nil || got != 2 {
		t.Fatalf("second Allocate = %d, %v", got, err)
	}
	if got, err := newRevisionStore(root).Allocate(key); err != nil || got != 3 {
		t.Fatalf("Allocate after restart = %d, %v", got, err)
	}
}

func TestRevisionStoreKeysAreIsolatedAndConcurrent(t *testing.T) {
	store := newRevisionStore(t.TempDir())
	keys := []revisionKey{
		{ReportType: reportTypeDeviceNetworkState, DeviceID: "dev/one", NetworkID: "home"},
		{ReportType: reportTypeRouterPrefixes, DeviceID: "router", NetworkID: "home"},
	}
	var wg sync.WaitGroup
	for _, key := range keys {
		key := key
		for range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := store.Allocate(key); err != nil {
					t.Errorf("Allocate(%+v): %v", key, err)
				}
			}()
		}
	}
	wg.Wait()
	for _, key := range keys {
		got, err := store.Current(key)
		if err != nil || got != 20 {
			t.Fatalf("Current(%+v) = %d, %v", key, got, err)
		}
	}
}

func TestRevisionStoreCorruptFileIsQuarantinedBeforeRevisionOne(t *testing.T) {
	root := t.TempDir()
	store := newRevisionStore(root)
	key := revisionKey{ReportType: reportTypeRouterPrefixes, DeviceID: "router", NetworkID: "home"}
	path := store.pathFor(key)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := store.Allocate(key)
	if err != nil || got != 1 {
		t.Fatalf("Allocate corrupt = %d, %v", got, err)
	}
	matches, err := filepath.Glob(path + ".corrupt-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("quarantine matches = %v, %v", matches, err)
	}
	if b, err := os.ReadFile(matches[0]); err != nil || string(b) != "{" {
		t.Fatalf("quarantine content = %q, %v", b, err)
	}
}

func TestRevisionStoreRejectsUnknownSchemaAndOverflow(t *testing.T) {
	root := t.TempDir()
	store := newRevisionStore(root)
	key := revisionKey{ReportType: reportTypeDeviceNetworkState, DeviceID: "dev", NetworkID: "home"}
	path := store.pathFor(key)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	record := revisionRecord{SchemaVersion: 2, ReportType: key.ReportType, DeviceID: key.DeviceID, NetworkID: key.NetworkID, Revision: 1}
	b, _ := json.Marshal(record)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Allocate(key); !errors.Is(err, errUnknownRevisionSchema) {
		t.Fatalf("unknown schema error = %v", err)
	}
	record.SchemaVersion = revisionSchemaVersion
	record.Revision = math.MaxUint64
	b, _ = json.Marshal(record)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Allocate(key); !errors.Is(err, errRevisionExhausted) {
		t.Fatalf("overflow error = %v", err)
	}
}

func TestRevisionStoreAdvanceAfterConflict(t *testing.T) {
	store := newRevisionStore(t.TempDir())
	key := revisionKey{ReportType: reportTypeRouterPrefixes, DeviceID: "router", NetworkID: "home"}
	if got, err := store.Allocate(key); err != nil || got != 1 {
		t.Fatalf("Allocate = %d, %v", got, err)
	}
	if got, err := store.AdvanceAfterConflict(key, 57); err != nil || got != 58 {
		t.Fatalf("AdvanceAfterConflict = %d, %v", got, err)
	}
	if got, err := store.Current(key); err != nil || got != 58 {
		t.Fatalf("Current = %d, %v", got, err)
	}
}
