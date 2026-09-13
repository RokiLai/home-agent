package githubrelease

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

	// 2. Negative test: a plain digest does not bind the checksum to the asset name.
	if _, err := c.FetchAssetSHA256(context.Background(), "v0.7.0", "plain"); err == nil {
		t.Fatal("expected plain sha256 sidecar to be rejected")
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

func TestGetLatestComponentReleaseFiltersAndPaginates(t *testing.T) {
	requests := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/RokiLai/home-agent/releases?per_page=100&page=2>; rel="next"`, tsURL(r)))
			_, _ = fmt.Fprint(w, `[
				{"id":1,"tag_name":"agent-v9.0.0","draft":false,"prerelease":false},
				{"id":2,"tag_name":"server-v1.2.0","draft":true,"prerelease":false},
				{"id":3,"tag_name":"server-not-semver","draft":false,"prerelease":false}
			]`)
			return
		}
		_, _ = fmt.Fprint(w, `[
			{"id":4,"tag_name":"server-v1.10.0","draft":false,"prerelease":false,"target_commitish":"abc","assets":[{"id":7,"name":"homeagent-server-linux-arm64","size":42,"browser_download_url":"https://example.invalid/bin"}]},
			{"id":5,"tag_name":"server-v1.3.0-beta.1","draft":false,"prerelease":true}
		]`)
	}))
	defer ts.Close()

	c := NewClient(Config{Repo: "RokiLai/home-agent", APIBase: ts.URL})
	rel, err := c.GetLatestComponentRelease(context.Background(), ComponentServer, true)
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "server-v1.10.0" || rel.Version != "v1.10.0" || rel.ID != 4 || len(rel.Assets) != 1 {
		t.Fatalf("unexpected component release: %+v", rel)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestGetLatestComponentReleaseRejectsWrongComponentAndIncompletePagination(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		page := r.URL.Query().Get("page")
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/RokiLai/home-agent/releases?per_page=100&page=%d>; rel="next"`, tsURL(r), parsePage(page)+1))
		_, _ = fmt.Fprintf(w, `[{"id":%d,"tag_name":"agent-v1.0.%d","draft":false,"prerelease":false}]`, parsePage(page), parsePage(page))
	}))
	defer ts.Close()

	c := NewClient(Config{Repo: "RokiLai/home-agent", APIBase: ts.URL})
	if _, err := c.GetLatestComponentRelease(context.Background(), ComponentServer, true); err == nil {
		t.Fatal("expected incomplete pagination/no server release error")
	}
}

func TestValidateReleaseAssetsRejectsIncompleteAgentMatrix(t *testing.T) {
	rel := &Release{TagName: "agent-v1.0.0", Assets: []Asset{{ID: 1, Name: "homeagent-agent-linux-amd64", Size: 10, BrowserDownloadURL: "https://example/bin"}}}
	if err := ValidateReleaseAssets(ComponentAgent, rel); err == nil {
		t.Fatal("missing platform assets and sidecars must be rejected")
	}
	rel.Assets = completeAssets(ComponentAgent)
	if err := ValidateReleaseAssets(ComponentAgent, rel); err != nil {
		t.Fatalf("complete Agent matrix rejected: %v", err)
	}
}

func completeAssets(component Component) []Asset {
	var assets []Asset
	for i, name := range RequiredAssetNames(component) {
		assets = append(assets, Asset{ID: int64(i + 1), Name: name, Size: 10, BrowserDownloadURL: "https://example/" + name})
	}
	return assets
}

func tsURL(r *http.Request) string { return "http://" + r.Host }

func parsePage(raw string) int {
	if raw == "" {
		return 1
	}
	var page int
	_, _ = fmt.Sscanf(raw, "%d", &page)
	return page
}

func TestClientTokenFuncInjectsAuthorizationBearer(t *testing.T) {
	var receivedAuthHeader string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			_, _ = fmt.Fprint(w, `{"id":100,"tag_name":"v1.0.0","name":"v1.0.0"}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/releases") {
			_, _ = fmt.Fprint(w, `[{"id":200,"tag_name":"server-v1.0.0","name":"server-v1.0.0","draft":false,"prerelease":false}]`)
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	c := NewClient(Config{
		Repo:    "RokiLai/home-agent",
		APIBase: ts.URL,
		TokenFunc: func() string {
			return "ghp_test_token_secret_xyz"
		},
	})

	// 1. GetLatestComponentRelease
	rel, err := c.GetLatestComponentRelease(context.Background(), ComponentServer, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel.TagName != "server-v1.0.0" {
		t.Fatalf("unexpected tag: %s", rel.TagName)
	}
	if receivedAuthHeader != "Bearer ghp_test_token_secret_xyz" {
		t.Fatalf("expected Bearer header, got %q", receivedAuthHeader)
	}

	// 2. GetLatestRelease
	receivedAuthHeader = ""
	relLatest, err := c.GetLatestRelease(context.Background(), true)
	if err != nil {
		t.Fatalf("unexpected error on latest release: %v", err)
	}
	if relLatest.TagName != "v1.0.0" {
		t.Fatalf("unexpected tag: %s", relLatest.TagName)
	}
	if receivedAuthHeader != "Bearer ghp_test_token_secret_xyz" {
		t.Fatalf("expected Bearer header on latest release, got %q", receivedAuthHeader)
	}
}

func TestClientTokenFuncNilOrEmptySendsNoAuthorization(t *testing.T) {
	var receivedAuthHeader string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `[{"id":300,"tag_name":"server-v1.0.0","draft":false,"prerelease":false}]`)
	}))
	defer ts.Close()

	// 1. Nil TokenFunc
	cNil := NewClient(Config{
		Repo:      "RokiLai/home-agent",
		APIBase:   ts.URL,
		TokenFunc: nil,
	})
	if _, err := cNil.GetLatestComponentRelease(context.Background(), ComponentServer, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedAuthHeader != "" {
		t.Fatalf("expected no Authorization header when nil, got %q", receivedAuthHeader)
	}

	// 2. Empty or whitespace TokenFunc
	cEmpty := NewClient(Config{
		Repo:    "RokiLai/home-agent",
		APIBase: ts.URL,
		TokenFunc: func() string {
			return "   \t\n  "
		},
	})
	receivedAuthHeader = "dirty"
	if _, err := cEmpty.GetLatestComponentRelease(context.Background(), ComponentServer, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedAuthHeader != "" {
		t.Fatalf("expected no Authorization header when empty string, got %q", receivedAuthHeader)
	}
}

func TestClientTokenFuncDoesNotLeakToAssetDownloads(t *testing.T) {
	rawHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var receivedAuthHeader string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "%s  homeagent-server-darwin-arm64\n", rawHash)
	}))
	defer ts.Close()

	c := NewClient(Config{
		Repo:            "RokiLai/home-agent",
		DownloadBaseURL: ts.URL,
		TokenFunc: func() string {
			return "sensitive_token_do_not_send_to_s3"
		},
	})

	hash, err := c.FetchAssetSHA256(context.Background(), "server-v1.0.0", "homeagent-server-darwin-arm64")
	if err != nil {
		t.Fatalf("unexpected error fetching sha256: %v", err)
	}
	if hash != rawHash {
		t.Fatalf("expected hash %s, got %s", rawHash, hash)
	}
	if receivedAuthHeader != "" {
		t.Fatalf("Authorization header must NOT be sent to static asset download endpoints: got %q", receivedAuthHeader)
	}
}

func TestClientHandles401UnauthorizedAnd403RateLimit(t *testing.T) {
	statusToReturn := http.StatusUnauthorized
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusToReturn)
		if statusToReturn == http.StatusUnauthorized {
			_, _ = fmt.Fprint(w, `{"message":"Bad credentials","status":"401"}`)
		} else if statusToReturn == http.StatusForbidden {
			_, _ = fmt.Fprint(w, `{"message":"API rate limit exceeded","status":"403"}`)
		}
	}))
	defer ts.Close()

	c := NewClient(Config{
		Repo:    "RokiLai/home-agent",
		APIBase: ts.URL,
		TokenFunc: func() string {
			return "bad_revoked_token"
		},
	})

	// 1. 401 Unauthorized
	statusToReturn = http.StatusUnauthorized
	_, err := c.GetLatestComponentRelease(context.Background(), ComponentServer, true)
	if err == nil {
		t.Fatal("expected error on 401 unauthorized")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected error message to contain 401, got %v", err)
	}

	// 2. 403 Rate Limited
	statusToReturn = http.StatusForbidden
	_, err = c.GetLatestComponentRelease(context.Background(), ComponentServer, true)
	if err == nil {
		t.Fatal("expected error on 403 forbidden")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected error message to contain 403, got %v", err)
	}
}
