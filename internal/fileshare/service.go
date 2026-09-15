// Package fileshare provides durable temporary file storage and expiring download links.
package fileshare

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	DefaultQuotaBytes    int64 = 20 * 1024 * 1024 * 1024
	DefaultRetentionDays       = 7
	DiskSafetyBytes      int64 = 512 * 1024 * 1024
)

var (
	ErrNotFound            = errors.New("file not found")
	ErrGone                = errors.New("download link expired or revoked")
	ErrConflict            = errors.New("revision conflict")
	ErrInvalidName         = errors.New("invalid file name")
	ErrInvalidSettings     = errors.New("invalid settings")
	ErrInvalidDuration     = errors.New("invalid link duration")
	ErrInsufficientStorage = errors.New("insufficient storage")
)

type FileStatus string

const (
	FileUploading    FileStatus = "uploading"
	FileReady        FileStatus = "ready"
	FileDeleting     FileStatus = "deleting"
	FileDeleteFailed FileStatus = "delete_failed"
)

type FileRecord struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Size        int64      `json:"size"`
	SHA256      string     `json:"sha256"`
	ContentType string     `json:"content_type"`
	UploadedBy  string     `json:"uploaded_by"`
	Status      FileStatus `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	Revision    uint64     `json:"revision"`
	DeleteError string     `json:"delete_error,omitempty"`
}

type Settings struct {
	RetentionDays int    `json:"retention_days"`
	Revision      uint64 `json:"revision"`
}

type DownloadLink struct {
	ID        string     `json:"id"`
	FileID    string     `json:"file_id"`
	TokenHash string     `json:"token_hash,omitempty"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

type Usage struct {
	QuotaBytes    int64 `json:"quota_bytes"`
	UsedBytes     int64 `json:"used_bytes"`
	ReservedBytes int64 `json:"reserved_bytes"`
}

type Options struct {
	QuotaBytes    int64
	Now           func() time.Time
	DiskAvailable func(string) (int64, error)
}

type diskState struct {
	Settings Settings                `json:"settings"`
	Files    map[string]FileRecord   `json:"files"`
	Links    map[string]DownloadLink `json:"links"`
}

type Service struct {
	mu            sync.RWMutex
	dir           string
	objectsDir    string
	partialsDir   string
	metadataPath  string
	quota         int64
	used          int64
	reserved      int64
	now           func() time.Time
	diskAvailable func(string) (int64, error)
	state         diskState
}

func Open(dir string, opts Options) (*Service, error) {
	if opts.QuotaBytes <= 0 {
		opts.QuotaBytes = DefaultQuotaBytes
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.DiskAvailable == nil {
		opts.DiskAvailable = availableBytes
	}
	s := &Service{
		dir: dir, objectsDir: filepath.Join(dir, "objects"), partialsDir: filepath.Join(dir, "partials"),
		metadataPath: filepath.Join(dir, "metadata.json"), quota: opts.QuotaBytes, now: opts.Now,
		diskAvailable: opts.DiskAvailable,
		state:         diskState{Settings: Settings{RetentionDays: DefaultRetentionDays, Revision: 1}, Files: map[string]FileRecord{}, Links: map[string]DownloadLink{}},
	}
	for _, path := range []string{s.dir, s.objectsDir, s.partialsDir} {
		if err := os.MkdirAll(path, 0700); err != nil {
			return nil, fmt.Errorf("create fileshare directory: %w", err)
		}
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	if err := s.recover(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) load() error {
	b, err := os.ReadFile(s.metadataPath)
	if errors.Is(err, os.ErrNotExist) {
		return s.persistLocked()
	}
	if err != nil {
		return fmt.Errorf("read fileshare metadata: %w", err)
	}
	if err := json.Unmarshal(b, &s.state); err != nil {
		return fmt.Errorf("decode fileshare metadata: %w", err)
	}
	if s.state.Files == nil {
		s.state.Files = map[string]FileRecord{}
	}
	if s.state.Links == nil {
		s.state.Links = map[string]DownloadLink{}
	}
	if s.state.Settings.RetentionDays < 1 || s.state.Settings.RetentionDays > 365 {
		return fmt.Errorf("decode fileshare metadata: %w", ErrInvalidSettings)
	}
	return nil
}

func (s *Service) recover() error {
	entries, err := os.ReadDir(s.partialsDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			_ = os.Remove(filepath.Join(s.partialsDir, entry.Name()))
		}
	}
	var used int64
	changed := false
	for id, record := range s.state.Files {
		info, err := os.Stat(s.objectPath(id))
		if err != nil || !info.Mode().IsRegular() {
			delete(s.state.Files, id)
			for linkID, link := range s.state.Links {
				if link.FileID == id {
					delete(s.state.Links, linkID)
				}
			}
			changed = true
			continue
		}
		record.Size = info.Size()
		record.Status = FileReady
		s.state.Files[id] = record
		used += info.Size()
	}
	objects, err := os.ReadDir(s.objectsDir)
	if err != nil {
		return err
	}
	for _, entry := range objects {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".blob") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".blob")
		if _, ok := s.state.Files[id]; !ok {
			_ = os.Remove(filepath.Join(s.objectsDir, entry.Name()))
		}
	}
	s.used = used
	if changed {
		return s.persistLocked()
	}
	return nil
}

func (s *Service) Upload(name, contentType, uploadedBy string, expectedSize int64, src io.Reader) (FileRecord, error) {
	if !validName(name) {
		return FileRecord{}, ErrInvalidName
	}
	if expectedSize < 0 || expectedSize > s.quota {
		return FileRecord{}, ErrInsufficientStorage
	}
	reserve := expectedSize
	s.mu.Lock()
	if s.used+s.reserved+reserve > s.quota {
		s.mu.Unlock()
		return FileRecord{}, ErrInsufficientStorage
	}
	if reserve > 0 {
		if available, err := s.diskAvailable(s.dir); err == nil && available < reserve+DiskSafetyBytes {
			s.mu.Unlock()
			return FileRecord{}, ErrInsufficientStorage
		}
	}
	s.reserved += reserve
	s.mu.Unlock()
	dynamicReserved := int64(0)
	defer func() {
		s.mu.Lock()
		s.reserved -= reserve + dynamicReserved
		s.mu.Unlock()
	}()

	id, err := randomID("file_", 18)
	if err != nil {
		return FileRecord{}, err
	}
	uploadID, err := randomID("upload_", 18)
	if err != nil {
		return FileRecord{}, err
	}
	partial := filepath.Join(s.partialsDir, uploadID+".part")
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return FileRecord{}, err
	}
	keep := false
	defer func() {
		_ = f.Close()
		if !keep {
			_ = os.Remove(partial)
		}
	}()
	h := sha256.New()
	var dst io.Writer = f
	if expectedSize == 0 {
		dst = &quotaWriter{service: s, dst: f, acquired: &dynamicReserved}
	}
	written, copyErr := io.Copy(io.MultiWriter(dst, h), io.LimitReader(src, s.quota+1))
	if copyErr != nil {
		return FileRecord{}, copyErr
	}
	if written > s.quota || (expectedSize > 0 && written != expectedSize) {
		return FileRecord{}, ErrInsufficientStorage
	}
	if err := f.Sync(); err != nil {
		return FileRecord{}, err
	}
	if err := f.Close(); err != nil {
		return FileRecord{}, err
	}

	now := s.now().UTC()
	record := FileRecord{ID: id, Name: name, Size: written, SHA256: hex.EncodeToString(h.Sum(nil)), ContentType: contentType, UploadedBy: uploadedBy, Status: FileReady, CreatedAt: now, Revision: 1}
	s.mu.Lock()
	defer s.mu.Unlock()
	heldReservation := reserve + dynamicReserved
	s.reserved -= heldReservation
	reserve = 0
	dynamicReserved = 0
	if s.used+written > s.quota {
		return FileRecord{}, ErrInsufficientStorage
	}
	record.ExpiresAt = now.Add(time.Duration(s.state.Settings.RetentionDays) * 24 * time.Hour)
	if err := os.Rename(partial, s.objectPath(id)); err != nil {
		return FileRecord{}, err
	}
	keep = true
	s.state.Files[id] = record
	s.used += written
	if err := s.persistLocked(); err != nil {
		delete(s.state.Files, id)
		s.used -= written
		_ = os.Remove(s.objectPath(id))
		keep = false
		return FileRecord{}, err
	}
	return record, nil
}

func (s *Service) List() ([]FileRecord, Usage) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	files := make([]FileRecord, 0, len(s.state.Files))
	for _, f := range s.state.Files {
		if f.Status == FileReady {
			files = append(files, f)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].CreatedAt.After(files[j].CreatedAt) })
	return files, Usage{QuotaBytes: s.quota, UsedBytes: s.used, ReservedBytes: s.reserved}
}

func (s *Service) OpenFile(id string) (*os.File, FileRecord, error) {
	s.mu.RLock()
	record, ok := s.state.Files[id]
	if !ok || record.Status != FileReady {
		s.mu.RUnlock()
		return nil, FileRecord{}, ErrNotFound
	}
	if !s.now().Before(record.ExpiresAt) {
		s.mu.RUnlock()
		return nil, FileRecord{}, ErrGone
	}
	f, err := os.Open(s.objectPath(id))
	s.mu.RUnlock()
	if err != nil {
		return nil, FileRecord{}, ErrNotFound
	}
	return f, record, nil
}

func (s *Service) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.state.Files[id]
	if !ok {
		return nil
	}
	record.Status = FileDeleting
	record.Revision++
	s.state.Files[id] = record
	for linkID, link := range s.state.Links {
		if link.FileID == id {
			delete(s.state.Links, linkID)
		}
	}
	if err := os.Remove(s.objectPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		record.Status = FileDeleteFailed
		record.DeleteError = err.Error()
		record.Revision++
		s.state.Files[id] = record
		_ = s.persistLocked()
		return err
	}
	delete(s.state.Files, id)
	s.used -= record.Size
	if s.used < 0 {
		s.used = 0
	}
	return s.persistLocked()
}

func (s *Service) Settings() Settings { s.mu.RLock(); defer s.mu.RUnlock(); return s.state.Settings }

func (s *Service) UpdateSettings(next Settings) (Settings, error) {
	if next.RetentionDays < 1 || next.RetentionDays > 365 {
		return Settings{}, ErrInvalidSettings
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if next.Revision != s.state.Settings.Revision {
		return Settings{}, ErrConflict
	}
	previous := s.state.Settings
	next.Revision++
	s.state.Settings = next
	if err := s.persistLocked(); err != nil {
		s.state.Settings = previous
		return Settings{}, err
	}
	return next, nil
}

func (s *Service) CreateLink(fileID, createdBy string, duration time.Duration) (DownloadLink, string, error) {
	allowed := map[time.Duration]bool{10 * time.Minute: true, time.Hour: true, 24 * time.Hour: true, 7 * 24 * time.Hour: true}
	if !allowed[duration] {
		return DownloadLink{}, "", ErrInvalidDuration
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.state.Files[fileID]
	if !ok || record.Status != FileReady {
		return DownloadLink{}, "", ErrNotFound
	}
	now := s.now().UTC()
	if !now.Before(record.ExpiresAt) {
		return DownloadLink{}, "", ErrGone
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return DownloadLink{}, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	id, err := randomID("link_", 18)
	if err != nil {
		return DownloadLink{}, "", err
	}
	expires := now.Add(duration)
	if record.ExpiresAt.Before(expires) {
		expires = record.ExpiresAt
	}
	link := DownloadLink{ID: id, FileID: fileID, TokenHash: tokenDigest(fileID, token), CreatedBy: createdBy, CreatedAt: now, ExpiresAt: expires}
	s.state.Links[id] = link
	if err := s.persistLocked(); err != nil {
		delete(s.state.Links, id)
		return DownloadLink{}, "", err
	}
	return publicLink(link), token, nil
}

func (s *Service) ListLinks(fileID string) ([]DownloadLink, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.state.Files[fileID]; !ok {
		return nil, ErrNotFound
	}
	links := []DownloadLink{}
	for _, link := range s.state.Links {
		if link.FileID == fileID {
			links = append(links, publicLink(link))
		}
	}
	sort.Slice(links, func(i, j int) bool { return links[i].CreatedAt.After(links[j].CreatedAt) })
	return links, nil
}

func (s *Service) RevokeLink(fileID, linkID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	link, ok := s.state.Links[linkID]
	if !ok || link.FileID != fileID {
		return ErrNotFound
	}
	if link.RevokedAt == nil {
		now := s.now().UTC()
		link.RevokedAt = &now
		s.state.Links[linkID] = link
	}
	return s.persistLocked()
}

func (s *Service) OpenPublicFile(fileID, token string) (*os.File, FileRecord, error) {
	digest := tokenDigest(fileID, token)
	s.mu.RLock()
	record, ok := s.state.Files[fileID]
	if !ok || record.Status != FileReady {
		s.mu.RUnlock()
		return nil, FileRecord{}, ErrNotFound
	}
	now := s.now()
	matched := false
	gone := false
	for _, link := range s.state.Links {
		if link.FileID != fileID || subtle.ConstantTimeCompare([]byte(link.TokenHash), []byte(digest)) != 1 {
			continue
		}
		matched = true
		gone = link.RevokedAt != nil || !now.Before(link.ExpiresAt) || !now.Before(record.ExpiresAt)
		break
	}
	if !matched {
		s.mu.RUnlock()
		return nil, FileRecord{}, ErrNotFound
	}
	if gone {
		s.mu.RUnlock()
		return nil, FileRecord{}, ErrGone
	}
	f, err := os.Open(s.objectPath(fileID))
	s.mu.RUnlock()
	if err != nil {
		return nil, FileRecord{}, ErrNotFound
	}
	return f, record, nil
}

func (s *Service) CleanupExpired() error {
	s.mu.RLock()
	now := s.now()
	ids := []string{}
	for id, record := range s.state.Files {
		if !now.Before(record.ExpiresAt) || record.Status == FileDeleteFailed {
			ids = append(ids, id)
		}
	}
	s.mu.RUnlock()
	var errs []error
	for _, id := range ids {
		if err := s.Delete(id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) objectPath(id string) string { return filepath.Join(s.objectsDir, id+".blob") }

func (s *Service) persistLocked() error {
	b, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.metadataPath + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0600); err != nil {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.metadataPath)
}

func validName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range name {
		if r == 0 || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func randomID(prefix string, n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenDigest(fileID, token string) string {
	sum := sha256.Sum256([]byte("homeagent-fileshare-link-v1\x00" + fileID + "\x00" + token))
	return hex.EncodeToString(sum[:])
}

func publicLink(link DownloadLink) DownloadLink { link.TokenHash = ""; return link }

type quotaWriter struct {
	service        *Service
	dst            io.Writer
	acquired       *int64
	sinceDiskCheck int64
}

func (w *quotaWriter) Write(p []byte) (int, error) {
	w.service.mu.Lock()
	need := int64(len(p))
	if w.service.used+w.service.reserved+need > w.service.quota {
		w.service.mu.Unlock()
		return 0, ErrInsufficientStorage
	}
	w.sinceDiskCheck += need
	if *w.acquired == 0 || w.sinceDiskCheck >= 16*1024*1024 {
		if available, err := w.service.diskAvailable(w.service.dir); err == nil && available < need+DiskSafetyBytes {
			w.service.mu.Unlock()
			return 0, ErrInsufficientStorage
		}
		w.sinceDiskCheck = 0
	}
	w.service.reserved += need
	*w.acquired += need
	w.service.mu.Unlock()

	n, err := w.dst.Write(p)
	if n < len(p) {
		unused := int64(len(p) - n)
		w.service.mu.Lock()
		w.service.reserved -= unused
		*w.acquired -= unused
		w.service.mu.Unlock()
	}
	return n, err
}
