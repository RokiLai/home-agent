package fileaccess

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLifecycleAndPersistence(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	user := User{ID: "u", Version: 1, Active: true}
	lookup := func(string) (User, error) { return user, nil }
	p := filepath.Join(t.TempDir(), "tokens.json")
	s, err := Open(p, lookup, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	rec, raw, err := s.Create("u", " iPhone ", 365)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 52 || !strings.HasPrefix(raw, "agt_file_") || rec.Name != "iPhone" || rec.Status != Active {
		t.Fatalf("bad record: %+v", rec)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), raw) {
		t.Fatal("plaintext persisted")
	}
	mode, _ := os.Stat(p)
	if mode.Mode().Perm() != 0600 {
		t.Fatal("permissions")
	}
	s, err = Open(p, lookup, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Validate(raw); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Validate("agt_file_bad"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	if err = s.Revoke("other", rec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	user.Version++
	if _, err = s.Validate(raw); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("version not rejected")
	}
	list, _ := s.List("u")
	if list[0].Status != AccountInvalid {
		t.Fatal(list)
	}
	if err = s.Revoke("u", rec.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Revoke("u", rec.ID); err != nil {
		t.Fatal(err)
	}
	s, err = Open(p, lookup, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	list, _ = s.List("u")
	if list[0].Status != Revoked {
		t.Fatal(list)
	}
}

func TestLimitsExpiryAndFailedPersistence(t *testing.T) {
	now := time.Now()
	lookup := func(string) (User, error) { return User{ID: "u", Version: 1, Active: true}, nil }
	p := filepath.Join(t.TempDir(), "tokens.json")
	s, _ := Open(p, lookup, func() time.Time { return now })
	for _, tc := range []struct {
		name string
		days int
	}{{"", 365}, {strings.Repeat("界", 65), 365}, {"x", 1}} {
		if _, _, err := s.Create("u", tc.name, tc.days); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	var raw string
	for i := 0; i < 20; i++ {
		_, r, err := s.Create("u", "phone", 30)
		if err != nil {
			t.Fatal(err)
		}
		raw = r
	}
	if _, _, err := s.Create("u", "extra", 30); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	now = now.Add(30 * 24 * time.Hour)
	if _, err := s.Validate(raw); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("expiry boundary")
	}
	if _, _, err := s.Create("u", "new", 90); err != nil {
		t.Fatal(err)
	}
	old, _ := s.List("u")
	s.path = filepath.Join(p, "bad")
	if _, r, err := s.Create("u", "failed", 365); err == nil || r != "" {
		t.Fatal("leaked token")
	}
	next, _ := s.List("u")
	if len(old) != len(next) {
		t.Fatal("dirty memory")
	}
	if err := s.Revoke("u", next[0].ID); err == nil {
		t.Fatal("revoke write failure")
	}
	next, _ = s.List("u")
	if next[0].Status != Active {
		t.Fatal("revoke not rolled back")
	}
}

func TestConcurrentLimitAndCommitRevokeOrdering(t *testing.T) {
	lookup := func(string) (User, error) { return User{ID: "u", Version: 1, Active: true}, nil }
	s, _ := Open("", lookup, time.Now)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.Create("u", "phone", 365) }()
	}
	wg.Wait()
	list, _ := s.List("u")
	if len(list) != 20 {
		t.Fatal(len(list))
	}
	s, _ = Open("", lookup, time.Now)
	rec, raw, _ := s.Create("u", "phone", 365)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- s.Guard(raw, func() error { close(entered); <-release; return nil }) }()
	<-entered
	revoked := make(chan error, 1)
	go func() { revoked <- s.Revoke("u", rec.ID) }()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	called := false
	if err := s.Guard(raw, func() error { called = true; return nil }); !errors.Is(err, ErrUnauthorized) || called {
		t.Fatal("revoked commit")
	}
}

func TestCorruptStoreAndUnavailableUser(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokens.json")
	os.WriteFile(p, []byte(`{"schema_version":99}`), 0600)
	lookup := func(string) (User, error) { return User{}, ErrUnavailable }
	if _, err := Open(p, lookup, time.Now); err == nil {
		t.Fatal("accepted unknown schema")
	}
	b, _ := os.ReadFile(p)
	if string(b) != `{"schema_version":99}` {
		t.Fatal("overwritten")
	}
	s, _ := Open("", lookup, time.Now)
	if _, _, err := s.Create("u", "phone", 365); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestCreateRechecksUserAfterPersistenceAndStateIsolation(t *testing.T) {
	calls := 0
	lookup := func(id string) (User, error) {
		calls++
		version := uint64(1)
		if calls > 1 {
			version = 2
		}
		return User{ID: id, Version: version, Active: true}, nil
	}
	p := filepath.Join(t.TempDir(), "tokens.json")
	s, _ := Open(p, lookup, time.Now)
	if _, raw, err := s.Create("u", "phone", 365); !errors.Is(err, ErrUnauthorized) || raw != "" {
		t.Fatal("stale create")
	}
	s, _ = Open(p, func(id string) (User, error) { return User{ID: id, Version: 1, Active: true}, nil }, time.Now)
	list, _ := s.List("u")
	if len(list) != 0 {
		t.Fatal("stale create persisted")
	}
	rec, raw, _ := s.Create("u", "phone", 365)
	list, _ = s.List("other")
	if len(list) != 0 {
		t.Fatal("other records disclosed")
	}
	if err := s.Revoke("other", rec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("other revoke")
	}
	if _, err := s.Validate(raw); err != nil {
		t.Fatal(err)
	}
	s.lookup = func(string) (User, error) { return User{}, ErrNotFound }
	if _, err := s.Validate(raw); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("deleted user")
	}
}

func TestValidateUsesOneCurrentAccountSnapshot(t *testing.T) {
	version := uint64(1)
	reads := 0
	s, err := Open(filepath.Join(t.TempDir(), "tokens.json"), func(string) (User, error) {
		reads++
		return User{ID: "u", Version: version, Active: true}, nil
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, raw, err := s.Create("u", "phone", 30)
	if err != nil {
		t.Fatal(err)
	}
	reads = 0
	if _, err = s.Validate(raw); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatalf("validation must use one account snapshot, got %d reads", reads)
	}
	version++
	if _, err = s.Validate(raw); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("stale account version accepted")
	}
}
