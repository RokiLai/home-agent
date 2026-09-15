package fileshare

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUploadListOpenDeleteAndRestart(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	s, err := Open(dir, Options{Now: func() time.Time { return now }, QuotaBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	record, err := s.Upload("report.txt", "text/plain", "usr-1", int64(len("hello")), bytes.NewBufferString("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if record.Size != 5 || record.SHA256 != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("unexpected record: %+v", record)
	}
	if !record.ExpiresAt.Equal(now.Add(7 * 24 * time.Hour)) {
		t.Fatalf("expires_at=%v", record.ExpiresAt)
	}
	files, usage := s.List()
	if len(files) != 1 || usage.UsedBytes != 5 || usage.ReservedBytes != 0 {
		t.Fatalf("files=%+v usage=%+v", files, usage)
	}
	f, opened, err := s.OpenFile(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(f)
	_ = f.Close()
	if string(data) != "hello" || opened.ID != record.ID {
		t.Fatalf("download=%q record=%+v", data, opened)
	}

	restarted, err := Open(dir, Options{Now: func() time.Time { return now }, QuotaBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if got, usage := restarted.List(); len(got) != 1 || usage.UsedBytes != 5 {
		t.Fatalf("restart files=%+v usage=%+v", got, usage)
	}
	if err := restarted.Delete(record.ID); err != nil {
		t.Fatal(err)
	}
	if got, usage := restarted.List(); len(got) != 0 || usage.UsedBytes != 0 {
		t.Fatalf("after delete files=%+v usage=%+v", got, usage)
	}
}

func TestQuotaReservationAndInvalidNames(t *testing.T) {
	s, err := Open(t.TempDir(), Options{QuotaBytes: 4, DiskAvailable: func(string) (int64, error) { return 1 << 30, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upload("large.bin", "", "usr", 5, bytes.NewReader([]byte("12345"))); !errors.Is(err, ErrInsufficientStorage) {
		t.Fatalf("quota error=%v", err)
	}
	for _, name := range []string{"", "../secret", "a/b", "a\\b", ".", "..", "bad\x00name"} {
		if _, err := s.Upload(name, "", "usr", 1, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("name %q error=%v", name, err)
		}
	}
	if _, usage := s.List(); usage.UsedBytes != 0 || usage.ReservedBytes != 0 {
		t.Fatalf("leaked quota: %+v", usage)
	}
}

func TestSettingsOnlyAffectNewUploads(t *testing.T) {
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	s, err := Open(t.TempDir(), Options{Now: func() time.Time { return now }, QuotaBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := s.Upload("first", "", "usr", 1, bytes.NewReader([]byte("1")))
	settings := s.Settings()
	settings.RetentionDays = 30
	updated, err := s.UpdateSettings(settings)
	if err != nil || updated.Revision != settings.Revision+1 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	second, _ := s.Upload("second", "", "usr", 1, bytes.NewReader([]byte("2")))
	if !first.ExpiresAt.Equal(now.Add(7*24*time.Hour)) || !second.ExpiresAt.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("first=%v second=%v", first.ExpiresAt, second.ExpiresAt)
	}
	stale := settings
	stale.RetentionDays = 10
	if _, err := s.UpdateSettings(stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error=%v", err)
	}
}

func TestDownloadLinkExpiryRevocationAndFileCap(t *testing.T) {
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	s, err := Open(t.TempDir(), Options{Now: func() time.Time { return now }, QuotaBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	record, _ := s.Upload("shared.bin", "", "usr", 1, bytes.NewReader([]byte("x")))
	link, token, err := s.CreateLink(record.ID, "usr", 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || !link.ExpiresAt.Equal(record.ExpiresAt) {
		t.Fatalf("link=%+v token=%q", link, token)
	}
	if _, _, err := s.OpenPublicFile(record.ID, "wrong"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong token error=%v", err)
	}
	f, _, err := s.OpenPublicFile(record.ID, token)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := s.RevokeLink(record.ID, link.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.OpenPublicFile(record.ID, token); !errors.Is(err, ErrGone) {
		t.Fatalf("revoked error=%v", err)
	}
}

func TestCleanupExpiredAndPartialRecovery(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	s, err := Open(dir, Options{Now: func() time.Time { return now }, QuotaBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Upload("old", "", "usr", 1, bytes.NewReader([]byte("x")))
	if err := os.WriteFile(dir+"/partials/orphan.part", []byte("junk"), 0600); err != nil {
		t.Fatal(err)
	}
	now = now.Add(8 * 24 * time.Hour)
	if err := s.CleanupExpired(); err != nil {
		t.Fatal(err)
	}
	if files, _ := s.List(); len(files) != 0 {
		t.Fatalf("expired files remain: %+v", files)
	}
	if _, err := Open(dir, Options{Now: func() time.Time { return now }, QuotaBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir + "/partials/orphan.part"); !os.IsNotExist(err) {
		t.Fatalf("orphan partial remains: %v", err)
	}
}

func TestStartupRemovesUnreferencedObject(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir, Options{QuotaBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/objects/orphan.blob", []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(dir, Options{QuotaBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir + "/objects/orphan.blob"); !os.IsNotExist(err) {
		t.Fatalf("orphan object remains: %v", err)
	}
	if _, usage := restarted.List(); usage.UsedBytes != 0 {
		t.Fatalf("orphan affected usage: %+v", usage)
	}
}

func TestConcurrentUnknownLengthUploadsRespectQuota(t *testing.T) {
	s, err := Open(t.TempDir(), Options{QuotaBytes: 64, DiskAvailable: func(string) (int64, error) { return 1 << 30, nil }})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var succeeded int
	var mu sync.Mutex
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if _, uploadErr := s.Upload(fmt.Sprintf("%d.bin", i), "", "usr", 0, bytes.NewReader(make([]byte, 32))); uploadErr == nil {
				mu.Lock()
				succeeded++
				mu.Unlock()
			} else if !errors.Is(uploadErr, ErrInsufficientStorage) {
				t.Errorf("upload error=%v", uploadErr)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	_, usage := s.List()
	if succeeded > 2 || usage.UsedBytes > 64 || usage.ReservedBytes != 0 {
		t.Fatalf("succeeded=%d usage=%+v", succeeded, usage)
	}
}

func TestLinkTokenIsNotPersistedInPlaintext(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{QuotaBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	record, _ := s.Upload("safe", "", "usr", 1, bytes.NewReader([]byte("x")))
	_, token, err := s.CreateLink(record.ID, "usr", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(dir + "/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), token) {
		t.Fatal("plaintext token persisted")
	}
}

type zeroReader struct{ remaining int64 }

func (r *zeroReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	for i := range p {
		p[i] = 0
	}
	r.remaining -= int64(len(p))
	return len(p), nil
}

func BenchmarkUploadOneGiBStreaming(b *testing.B) {
	const size = int64(1 << 30)
	b.ReportAllocs()
	b.SetBytes(size)
	for i := 0; i < b.N; i++ {
		dir := b.TempDir()
		service, err := Open(dir, Options{QuotaBytes: 2 << 30, DiskAvailable: func(string) (int64, error) { return 4 << 30, nil }})
		if err != nil {
			b.Fatal(err)
		}
		runtime.GC()
		var baseline runtime.MemStats
		runtime.ReadMemStats(&baseline)
		var peak atomic.Uint64
		done := make(chan struct{})
		go func() {
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					var current runtime.MemStats
					runtime.ReadMemStats(&current)
					growth := uint64(0)
					if current.HeapInuse > baseline.HeapInuse {
						growth = current.HeapInuse - baseline.HeapInuse
					}
					for old := peak.Load(); growth > old && !peak.CompareAndSwap(old, growth); old = peak.Load() {
					}
				}
			}
		}()
		_, err = service.Upload("benchmark.bin", "application/octet-stream", "benchmark", size, &zeroReader{remaining: size})
		close(done)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(float64(peak.Load()), "peak_heap_growth_bytes")
	}
}
