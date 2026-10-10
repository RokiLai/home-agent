// Package fileaccess manages durable, revocable credentials scoped to file operations.
package fileaccess

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var (
	ErrUnauthorized = errors.New("invalid file credential")
	ErrUnavailable  = errors.New("file credential service unavailable")
	ErrNotFound     = errors.New("file credential not found")
	ErrInvalid      = errors.New("invalid file credential request")
	ErrLimit        = errors.New("file credential limit reached")
)

type Status string

const (
	Active         Status = "active"
	Expired        Status = "expired"
	Revoked        Status = "revoked"
	AccountInvalid Status = "account_invalid"
)

type User struct {
	ID      string
	Version uint64
	Active  bool
}
type Record struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at"`
	Permanent bool       `json:"permanent,omitempty"`
	RevokedAt *time.Time `json:"revoked_at"`
	Status    Status     `json:"status"`
	Scopes    []string   `json:"scopes"`
}
type stored struct {
	Record
	UserID  string `json:"user_id"`
	Version uint64 `json:"issued_version"`
	Hash    string `json:"token_hash"`
}
type diskState struct {
	Schema int               `json:"schema_version"`
	Tokens map[string]stored `json:"tokens"`
}
type Service struct {
	mu     sync.RWMutex
	path   string
	tokens map[string]stored
	lookup func(string) (User, error)
	now    func() time.Time
}

func Open(path string, lookup func(string) (User, error), now func() time.Time) (*Service, error) {
	if lookup == nil {
		return nil, ErrUnavailable
	}
	if now == nil {
		now = time.Now
	}
	s := &Service{path: path, tokens: map[string]stored{}, lookup: lookup, now: now}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	var state diskState
	if json.Unmarshal(b, &state) != nil || (state.Schema != 1 && state.Schema != 2) || state.Tokens == nil {
		return nil, ErrUnavailable
	}
	for id, r := range state.Tokens {
		hash, e := hex.DecodeString(r.Hash)
		if id == "" || id != r.ID || r.UserID == "" || r.Version == 0 || e != nil || len(hash) != 32 || r.CreatedAt.IsZero() || (r.Permanent && (state.Schema != 2 || r.ExpiresAt != nil)) || (!r.Permanent && (r.ExpiresAt == nil || !r.ExpiresAt.After(r.CreatedAt))) {
			return nil, ErrUnavailable
		}
	}
	s.tokens = state.Tokens
	return s, nil
}
func digest(raw string) string { h := sha256.Sum256([]byte(raw)); return hex.EncodeToString(h[:]) }
func random(prefix string, n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", ErrUnavailable
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}
func scopes() []string {
	return []string{"files.upload", "files.list", "files.download", "files.links.create"}
}
func (s *Service) user(id string) (User, error) {
	u, err := s.lookup(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return User{}, ErrUnauthorized
		}
		return User{}, ErrUnavailable
	}
	if !u.Active || u.ID != id {
		return User{}, ErrUnauthorized
	}
	return u, nil
}
func (s *Service) status(r stored) (Status, error) {
	if r.RevokedAt != nil {
		return Revoked, nil
	}
	u, err := s.user(r.UserID)
	if errors.Is(err, ErrUnauthorized) {
		return AccountInvalid, nil
	}
	if err != nil {
		return "", err
	}
	if u.Version != r.Version {
		return AccountInvalid, nil
	}
	if !r.Permanent && !s.now().Before(*r.ExpiresAt) {
		return Expired, nil
	}
	return Active, nil
}
func copyRecord(rec Record) Record {
	if rec.ExpiresAt != nil {
		v := *rec.ExpiresAt
		rec.ExpiresAt = &v
	}
	return rec
}
func (s *Service) public(r stored) (Record, error) {
	status, err := s.status(r)
	rec := copyRecord(r.Record)
	rec.Status = status
	rec.Scopes = scopes()
	if rec.RevokedAt != nil {
		v := *rec.RevokedAt
		rec.RevokedAt = &v
	}
	return rec, err
}
func (s *Service) persist() error {
	if s.path == "" {
		return nil
	}
	b, err := json.Marshal(diskState{Schema: 2, Tokens: s.tokens})
	if err != nil {
		return ErrUnavailable
	}
	if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return ErrUnavailable
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".file-tokens-*")
	if err != nil {
		return ErrUnavailable
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, s.path)
	}
	if err != nil {
		return ErrUnavailable
	}
	return nil
}
func (s *Service) Create(userID, name string, days int) (Record, string, error) {
	name = strings.TrimSpace(name)
	if days == 0 {
		days = 365
	}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 64 || (days != -1 && days != 30 && days != 90 && days != 365) {
		return Record{}, "", ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.user(userID)
	if err != nil {
		return Record{}, "", err
	}
	count := 0
	for _, r := range s.tokens {
		if r.UserID == userID {
			status, e := s.status(r)
			if e != nil {
				return Record{}, "", e
			}
			if status == Active {
				count++
			}
		}
	}
	if count >= 20 {
		return Record{}, "", ErrLimit
	}
	raw, err := random("agt_file_", 32)
	if err != nil {
		return Record{}, "", err
	}
	id, err := random("ft_", 18)
	if err != nil {
		return Record{}, "", err
	}
	now := s.now().UTC()
	r := stored{Record: Record{ID: id, Name: name, CreatedAt: now, Permanent: days == -1}, UserID: userID, Version: u.Version, Hash: digest(raw)}
	if days != -1 {
		expires := now.Add(time.Duration(days) * 24 * time.Hour)
		r.ExpiresAt = &expires
	}
	s.tokens[id] = r
	if err = s.persist(); err != nil {
		delete(s.tokens, id)
		return Record{}, "", err
	}
	current, e := s.user(userID)
	if e != nil || current.Version != u.Version {
		delete(s.tokens, id)
		if err = s.persist(); err != nil {
			return Record{}, "", err
		}
		if e != nil {
			return Record{}, "", e
		}
		return Record{}, "", ErrUnauthorized
	}
	rec := copyRecord(r.Record)
	rec.Status = Active
	rec.Scopes = scopes()
	return rec, raw, nil
}
func (s *Service) List(userID string) ([]Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Record{}
	for _, r := range s.tokens {
		if r.UserID == userID {
			rec, err := s.public(r)
			if err != nil {
				return nil, err
			}
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}
func (s *Service) Revoke(userID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.tokens[id]
	if !ok || r.UserID != userID {
		return ErrNotFound
	}
	if r.RevokedAt != nil {
		return nil
	}
	old := r
	now := s.now().UTC()
	r.RevokedAt = &now
	s.tokens[id] = r
	if err := s.persist(); err != nil {
		s.tokens[id] = old
		return err
	}
	return nil
}
func (s *Service) validate(raw string) (User, error) {
	if !strings.HasPrefix(raw, "agt_file_") || len(raw) != 52 {
		return User{}, ErrUnauthorized
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, "agt_file_"))
	if err != nil || len(b) != 32 {
		return User{}, ErrUnauthorized
	}
	hash := digest(raw)
	for _, r := range s.tokens {
		if r.Hash == hash {
			u, e := s.user(r.UserID)
			if e != nil {
				return User{}, e
			}
			if r.RevokedAt != nil || u.Version != r.Version || (!r.Permanent && !s.now().Before(*r.ExpiresAt)) {
				return User{}, ErrUnauthorized
			}
			return u, nil
		}
	}
	return User{}, ErrUnauthorized
}
func (s *Service) Validate(raw string) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.validate(raw)
}
func (s *Service) Guard(raw string, commit func() error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, err := s.validate(raw); err != nil {
		return err
	}
	if commit == nil {
		return fmt.Errorf("%w: missing commit", ErrUnavailable)
	}
	return commit()
}
