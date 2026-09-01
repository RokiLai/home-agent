package qualitygate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalQualityGateUsesSingleRepositoryEntry(t *testing.T) {
	workflow := filepath.Join("..", "..", ".github", "workflows", "quality-gate.yml")
	if _, err := os.Stat(workflow); !os.IsNotExist(err) {
		t.Fatalf("hosted quality-gate workflow must not be present, stat error: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(readme), "./scripts/quality-gate.sh") != 1 {
		t.Fatal("README must expose the single local CI entry exactly once")
	}
}
