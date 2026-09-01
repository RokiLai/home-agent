package githubrelease

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		v1       string
		v2       string
		expected int
	}{
		{"v0.6.11", "v0.6.11", 0},
		{"0.6.11", "v0.6.11", 0},
		{"v0.6.11", "v0.7.0", -1},
		{"v0.7.0", "v0.6.11", 1},
		{"v1.0.0", "v0.9.9", 1},
		{"v0.6.10", "v0.6.11", -1},
		{"v0.6.11", "v0.6.11-beta.1", 1},
	}

	for _, tt := range tests {
		got := CompareVersions(tt.v1, tt.v2)
		if got != tt.expected {
			t.Errorf("CompareVersions(%q, %q) = %d; want %d", tt.v1, tt.v2, got, tt.expected)
		}
	}
}

func TestBuildAssetDownloadURL(t *testing.T) {
	c := NewClient(Config{
		Repo: "RokiLai/home-agent",
	})
	url := c.BuildAssetDownloadURL("v0.7.0", "homeagent-agent-darwin-arm64")
	expected := "https://github.com/RokiLai/home-agent/releases/download/v0.7.0/homeagent-agent-darwin-arm64"
	if url != expected {
		t.Fatalf("expected url %s, got %s", expected, url)
	}

	// With mirror prefix
	cMirror := NewClient(Config{
		Repo:         "RokiLai/home-agent",
		MirrorPrefix: "https://ghproxy.net/",
	})
	urlMirror := cMirror.BuildAssetDownloadURL("v0.7.0", "homeagent-agent-darwin-arm64")
	expectedMirror := "https://ghproxy.net/https://github.com/RokiLai/home-agent/releases/download/v0.7.0/homeagent-agent-darwin-arm64"
	if urlMirror != expectedMirror {
		t.Fatalf("expected mirror url %s, got %s", expectedMirror, urlMirror)
	}
}

func TestFetchAssetSHA256(t *testing.T) {
	rawHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/RokiLai/home-agent/releases/download/v0.7.0/homeagent-agent-darwin-arm64.sha256":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "%s  homeagent-agent-darwin-arm64\n", rawHash)
		case "/RokiLai/home-agent/releases/download/v0.7.0/plain.sha256":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, rawHash)
		case "/RokiLai/home-agent/releases/download/v0.7.0/notfound.sha256":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer ts.Close()

	c := NewClient(Config{
		Repo:            "RokiLai/home-agent",
		DownloadBaseURL: ts.URL,
	})

	// 1. Standard format: hash + filename
	hash1, err := c.FetchAssetSHA256(context.Background(), "v0.7.0", "homeagent-agent-darwin-arm64")
	if err != nil {
		t.Fatalf("unexpected error fetching sha256: %v", err)
	}
	if hash1 != rawHash {
		t.Fatalf("expected hash %s, got %s", rawHash, hash1)
	}

	// 2. Plain hash format
	hash2, err := c.FetchAssetSHA256(context.Background(), "v0.7.0", "plain")
	if err != nil {
		t.Fatalf("unexpected error fetching plain sha256: %v", err)
	}
	if hash2 != rawHash {
		t.Fatalf("expected plain hash %s, got %s", rawHash, hash2)
	}

	// 3. Negative test: 404 Not Found
	_, err = c.FetchAssetSHA256(context.Background(), "v0.7.0", "notfound")
	if err == nil {
		t.Fatal("expected error on 404 asset, got nil")
	}
}

func TestGetLatestRelease_AndCaching(t *testing.T) {
	requestCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/RokiLai/home-agent/releases/latest" {
			requestCount++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{
				"tag_name": "v0.7.0",
				"name": "Release v0.7.0",
				"body": "Test release notes",
				"published_at": "2026-09-01T00:00:00Z",
				"html_url": "https://github.com/RokiLai/home-agent/releases/tag/v0.7.0"
			}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	c := NewClient(Config{
		Repo:     "RokiLai/home-agent",
		APIBase:  ts.URL,
		CacheTTL: 10 * time.Minute,
	})

	// 1. First fetch - hits network
	rel1, err := c.GetLatestRelease(context.Background(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel1.TagName != "v0.7.0" || rel1.Body != "Test release notes" {
		t.Fatalf("unexpected release: %+v", rel1)
	}
	if requestCount != 1 {
		t.Fatalf("expected requestCount=1, got %d", requestCount)
	}

	// 2. Second fetch - hits cache
	rel2, err := c.GetLatestRelease(context.Background(), false)
	if err != nil {
		t.Fatalf("unexpected error on cached fetch: %v", err)
	}
	if rel2.TagName != "v0.7.0" {
		t.Fatalf("unexpected release on cache: %+v", rel2)
	}
	if requestCount != 1 {
		t.Fatalf("expected cached requestCount=1, got %d", requestCount)
	}

	// 3. Force refresh - hits network again
	rel3, err := c.GetLatestRelease(context.Background(), true)
	if err != nil {
		t.Fatalf("unexpected error on force refresh: %v", err)
	}
	if rel3.TagName != "v0.7.0" {
		t.Fatalf("unexpected release on force refresh: %+v", rel3)
	}
	if requestCount != 2 {
		t.Fatalf("expected requestCount=2 on force refresh, got %d", requestCount)
	}
}
