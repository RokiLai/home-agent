package device

import (
	"errors"
	"regexp"
	"sync"
	"testing"
)

func TestGrantLevelValidation(t *testing.T) {
	validLevels := []GrantLevel{GrantLevelRead, GrantLevelOperate, GrantLevelManage}
	for _, l := range validLevels {
		if !IsValidGrantLevel(l) {
			t.Fatalf("Expected valid grant level: %s", l)
		}
	}

	invalidLevels := []GrantLevel{"", "admin", "owner", "write", "all"}
	for _, l := range invalidLevels {
		if IsValidGrantLevel(l) {
			t.Fatalf("Expected invalid grant level: %s", l)
		}
	}

	id1 := GenerateGrantID()
	id2 := GenerateGrantID()
	if id1 == "" || id2 == "" || id1 == id2 {
		t.Fatalf("Generated grant IDs must be non-empty and unique: %s, %s", id1, id2)
	}
}

func TestGenerateGrantIDFormatAndConcurrentUniqueness(t *testing.T) {
	const count = 1000
	ids := make(chan string, count)
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids <- GenerateGrantID()
		}()
	}
	wg.Wait()
	close(ids)

	pattern := regexp.MustCompile(`^grant_[0-9a-f]{32}$`)
	seen := make(map[string]struct{}, count)
	for id := range ids {
		if !pattern.MatchString(id) {
			t.Fatalf("Generated grant ID %q does not match %s", id, pattern)
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("Generated duplicate grant ID: %s", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("Generated %d unique grant IDs, want %d", len(seen), count)
	}
}

func TestGenerateGrantIDRejectsRandomSourceFailure(t *testing.T) {
	_, err := generateGrantID(failingReader{})
	if !errors.Is(err, errGrantIDEntropy) {
		t.Fatalf("generateGrantID() error = %v, want %v", err, errGrantIDEntropy)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}
