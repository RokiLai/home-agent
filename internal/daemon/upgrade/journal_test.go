package upgrade

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestTLVEncodingAndDecoding(t *testing.T) {
	fields := []TLVField{
		{FieldID: 1, Type: TLVTypeUTF8, Value: []byte("cmd-123")},
		{FieldID: 2, Type: TLVTypeUTF8, Value: []byte("tx-456")},
		{FieldID: 3, Type: TLVTypeU64, Value: func() []byte {
			b := make([]byte, 8)
			binary.BigEndian.PutUint64(b, uint64(StateAccepted))
			return b
		}()},
		{FieldID: 4, Type: TLVTypeBool, Value: []byte{0x01}},
	}

	encoded, err := EncodeTLVFields(fields)
	if err != nil {
		t.Fatalf("EncodeTLVFields failed: %v", err)
	}

	decoded, err := DecodeTLVFields(encoded)
	if err != nil {
		t.Fatalf("DecodeTLVFields failed: %v", err)
	}

	if len(decoded) != len(fields) {
		t.Fatalf("decoded field count mismatch: got %d, want %d", len(decoded), len(fields))
	}
	if string(decoded[0].Value) != "cmd-123" || string(decoded[1].Value) != "tx-456" {
		t.Fatalf("decoded values mismatch: %+v", decoded)
	}

	// Negative case: duplicate field ID
	dupFields := []TLVField{
		{FieldID: 1, Type: TLVTypeUTF8, Value: []byte("cmd-1")},
		{FieldID: 1, Type: TLVTypeUTF8, Value: []byte("cmd-2")},
	}
	if _, err := EncodeTLVFields(dupFields); err == nil {
		t.Fatal("expected duplicate field error, got nil")
	}
}

func TestJournalAppendAndPlayback(t *testing.T) {
	tempDir := t.TempDir()
	j, err := OpenJournal(tempDir)
	if err != nil {
		t.Fatalf("OpenJournal failed: %v", err)
	}

	// 1. Write Transaction Created record
	f1 := []TLVField{
		{FieldID: 1, Type: TLVTypeUTF8, Value: []byte("cmd-test-1")},
		{FieldID: 2, Type: TLVTypeUTF8, Value: []byte("tx-test-1")},
		{FieldID: 3, Type: TLVTypeU64, Value: func() []byte {
			b := make([]byte, 8)
			binary.BigEndian.PutUint64(b, uint64(StateAccepted))
			return b
		}()},
		{FieldID: 19, Type: TLVTypeU64, Value: func() []byte {
			b := make([]byte, 8)
			binary.BigEndian.PutUint64(b, 42)
			return b
		}()},
	}
	if err := j.AppendRecord(TagTransactionCreated, f1); err != nil {
		t.Fatalf("AppendRecord failed: %v", err)
	}

	// 2. Write Phase Progress record
	f2 := []TLVField{
		{FieldID: 3, Type: TLVTypeU64, Value: func() []byte {
			b := make([]byte, 8)
			binary.BigEndian.PutUint64(b, uint64(StateDownloading))
			return b
		}()},
	}
	if err := j.AppendRecord(TagPhase, f2); err != nil {
		t.Fatalf("AppendRecord failed: %v", err)
	}

	// Check state in memory
	state := j.GetState()
	if state.CommandID != "cmd-test-1" || state.CurrentState != StateDownloading || state.LastJournalRevision != 2 {
		t.Fatalf("unexpected state after append: %+v", state)
	}

	// 3. Re-open journal and verify playback
	jReopen, err := OpenJournal(tempDir)
	if err != nil {
		t.Fatalf("re-opening OpenJournal failed: %v", err)
	}
	reopenState := jReopen.GetState()
	if reopenState.CommandID != "cmd-test-1" || reopenState.CurrentState != StateDownloading || reopenState.LastJournalRevision != 2 {
		t.Fatalf("unexpected state after playback: %+v", reopenState)
	}

	// 4. Test truncated write at EOF recovery
	journalFile := filepath.Join(tempDir, "transaction.journal")
	f, err := os.OpenFile(journalFile, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	// Append 3 bytes of garbage at EOF
	_, _ = f.Write([]byte{0x00, 0x01, 0x02})
	_ = f.Close()

	jTrunc, err := OpenJournal(tempDir)
	if err != nil {
		t.Fatalf("OpenJournal on truncated file failed: %v", err)
	}
	truncState := jTrunc.GetState()
	if truncState.LastJournalRevision != 2 {
		t.Fatalf("expected last revision 2 after truncated EOF recovery, got %d", truncState.LastJournalRevision)
	}
}
