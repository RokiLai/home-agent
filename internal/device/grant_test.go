package device

import (
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
