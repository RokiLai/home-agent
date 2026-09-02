package version

import (
	"os"
	"strings"
	"testing"
)

func TestDefaultVersionHasSingleSource(t *testing.T) {
	if defaultVersion != "v0.6.12" {
		t.Fatalf("defaultVersion = %q, want v0.6.12", defaultVersion)
	}
	if Version != defaultVersion {
		t.Fatalf("Version = %q, want defaultVersion %q", Version, defaultVersion)
	}

	source, err := os.ReadFile("version.go")
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(source), `"v0.6.12"`); count != 1 {
		t.Fatalf("version.go contains %d default version literals, want 1", count)
	}
}

func TestGet(t *testing.T) {
	original := Version
	t.Cleanup(func() { Version = original })

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "injected", value: "v1.2.3", want: "v1.2.3"},
		{name: "injected with whitespace", value: "  v1.2.3\t", want: "v1.2.3"},
		{name: "empty", value: "", want: defaultVersion},
		{name: "whitespace", value: " \t\n", want: defaultVersion},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			Version = test.value
			if got := Get(); got != test.want {
				t.Fatalf("Get() = %q, want %q", got, test.want)
			}
		})
	}
}
