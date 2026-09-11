// Package githubrelease provides a client for querying GitHub Releases,
// computing and fetching SHA256 checksums, and comparing semantic versions.
package githubrelease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config 配置 GitHub Release 客户端。
type Config struct {
	Repo            string        // 格式: "owner/repo" (例如 "RokiLai/home-agent")
	APIBase         string        // 默认为 "https://api.github.com"
	DownloadBaseURL string        // 默认为 "https://github.com"
	MirrorPrefix    string        // 可选加速镜像前缀 (如 "https://ghproxy.net/")
	CacheTTL        time.Duration // 最新 Release 缓存时长 (默认 10 分钟)
	HTTPClient      *http.Client  // 底层 HTTP 客户端
}

// Release 表示 GitHub Release 元数据。
type Release struct {
	ID          int64     `json:"id"`
	TagName     string    `json:"tag_name"`
	Version     string    `json:"-"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	Commitish   string    `json:"target_commitish"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []Asset   `json:"assets"`
}

// Asset 表示 GitHub Release 中冻结的资产身份。
type Asset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Component 标识独立 Release 通道。
type Component string

const (
	ComponentServer Component = "server"
	ComponentAgent  Component = "agent"
)

var strictVersionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// Client 封装 GitHub Release 交互。
type Client struct {
	repo            string
	apiBase         string
	downloadBaseURL string
	mirrorPrefix    string
	cacheTTL        time.Duration
	httpClient      *http.Client

	mu             sync.RWMutex
	cachedRel      *Release
	cachedAt       time.Time
	componentCache map[Component]cachedComponentRelease
}

type cachedComponentRelease struct {
	release Release
	at      time.Time
}

// NewClient 创建新的 GitHub Release 客户端。
func NewClient(cfg Config) *Client {
	apiBase := strings.TrimRight(cfg.APIBase, "/")
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	dlBase := strings.TrimRight(cfg.DownloadBaseURL, "/")
	if dlBase == "" {
		dlBase = "https://github.com"
	}
	repo := strings.Trim(cfg.Repo, "/")
	if repo == "" {
		repo = "RokiLai/home-agent"
	}
	ttl := cfg.CacheTTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	return &Client{
		repo:            repo,
		apiBase:         apiBase,
		downloadBaseURL: dlBase,
		mirrorPrefix:    cfg.MirrorPrefix,
		cacheTTL:        ttl,
		httpClient:      httpClient,
		componentCache:  make(map[Component]cachedComponentRelease),
	}
}

// GetLatestComponentRelease 扫描 Releases 列表并返回指定组件的最高稳定 SemVer。
func (c *Client) GetLatestComponentRelease(ctx context.Context, component Component, forceRefresh bool) (*Release, error) {
	if component != ComponentServer && component != ComponentAgent {
		return nil, fmt.Errorf("unsupported release component %q", component)
	}
	if !forceRefresh {
		c.mu.RLock()
		cached, ok := c.componentCache[component]
		c.mu.RUnlock()
		if ok && time.Since(cached.at) < c.cacheTTL {
			rel := cached.release
			return &rel, nil
		}
	}

	prefix := string(component) + "-"
	var best *Release
	for page := 1; page <= 10; page++ {
		endpoint := fmt.Sprintf("%s/repos/%s/releases?per_page=100&page=%d", c.apiBase, c.repo, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("create releases request: %w", err)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", "HomeAgent-Server")
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list github releases: %w", err)
		}
		var releases []Release
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&releases)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("github api returned status %d for releases list", resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("decode releases list: %w", decodeErr)
		}
		for i := range releases {
			rel := releases[i]
			if rel.Draft || rel.Prerelease || !strings.HasPrefix(rel.TagName, prefix) {
				continue
			}
			ver := strings.TrimPrefix(rel.TagName, prefix)
			if !strictVersionPattern.MatchString(ver) {
				continue
			}
			rel.Version = ver
			if best == nil || CompareVersions(best.Version, rel.Version) < 0 {
				candidate := rel
				best = &candidate
			}
		}
		if !linkHasNext(resp.Header.Get("Link")) {
			if best == nil {
				return nil, fmt.Errorf("no valid %s release found", component)
			}
			c.mu.Lock()
			c.componentCache[component] = cachedComponentRelease{release: *best, at: time.Now()}
			c.mu.Unlock()
			result := *best
			return &result, nil
		}
	}
	return nil, errors.New("release pagination incomplete after 10 pages")
}

func linkHasNext(header string) bool {
	for _, part := range strings.Split(header, ",") {
		sections := strings.Split(strings.TrimSpace(part), ";")
		if len(sections) < 2 || !strings.Contains(strings.Join(sections[1:], ";"), `rel="next"`) {
			continue
		}
		raw := strings.Trim(strings.TrimSpace(sections[0]), "<>")
		if _, err := url.ParseRequestURI(raw); err == nil {
			return true
		}
	}
	return false
}

// RequiredAssetNames returns the complete binary and checksum matrix for a channel.
func RequiredAssetNames(component Component) []string {
	var binaries []string
	if component == ComponentServer {
		name := fmt.Sprintf("homeagent-server-%s-%s", runtime.GOOS, runtime.GOARCH)
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		binaries = []string{name}
	} else if component == ComponentAgent {
		binaries = []string{
			"homeagent-agent-linux-amd64", "homeagent-agent-linux-arm64", "homeagent-agent-linux-arm",
			"homeagent-agent-linux-mips", "homeagent-agent-linux-mipsle", "homeagent-agent-darwin-amd64",
			"homeagent-agent-darwin-arm64", "homeagent-agent-windows-amd64.exe", "homeagent-agent-windows-arm64.exe",
		}
	}
	result := make([]string, 0, len(binaries)*2)
	for _, name := range binaries {
		result = append(result, name, name+".sha256")
	}
	return result
}

// ValidateReleaseAssets rejects a selected release unless its required manifest is complete.
func ValidateReleaseAssets(component Component, release *Release) error {
	if release == nil {
		return errors.New("release is nil")
	}
	assets := make(map[string]Asset, len(release.Assets))
	for _, asset := range release.Assets {
		if _, duplicate := assets[asset.Name]; duplicate {
			return fmt.Errorf("duplicate release asset %q", asset.Name)
		}
		assets[asset.Name] = asset
	}
	for _, name := range RequiredAssetNames(component) {
		asset, ok := assets[name]
		if !ok || asset.ID <= 0 || asset.Size <= 0 || asset.BrowserDownloadURL == "" {
			return fmt.Errorf("required release asset %q is missing or incomplete", name)
		}
	}
	return nil
}

// Repo 返回配置的仓库名称。
func (c *Client) Repo() string {
	return c.repo
}

// MirrorPrefix 返回配置的加速前缀。
func (c *Client) MirrorPrefix() string {
	return c.mirrorPrefix
}

// BuildAssetDownloadURL 根据 tag 与二进制文件名构造公开下载 URL。
func (c *Client) BuildAssetDownloadURL(tag, binaryName string) string {
	rawURL := fmt.Sprintf("%s/%s/releases/download/%s/%s", c.downloadBaseURL, c.repo, tag, binaryName)
	if c.mirrorPrefix != "" {
		return strings.TrimRight(c.mirrorPrefix, "/") + "/" + rawURL
	}
	return rawURL
}

// FetchAssetSHA256 在线拉取指定版本与资产的 .sha256 校验和文本，并提取 64 位 Hex 哈希。
func (c *Client) FetchAssetSHA256(ctx context.Context, tag, binaryName string) (string, error) {
	shaFileName := binaryName
	if !strings.HasSuffix(shaFileName, ".sha256") {
		shaFileName += ".sha256"
	}
	shaURL := c.BuildAssetDownloadURL(tag, shaFileName)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, shaURL, nil)
	if err != nil {
		return "", fmt.Errorf("create sha256 request: %w", err)
	}
	req.Header.Set("User-Agent", "HomeAgent-Server")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch sha256 asset from %s: %w", shaURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch sha256 failed with status %d: %s", resp.StatusCode, shaURL)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", fmt.Errorf("read sha256 body: %w", err)
	}

	return parseSHA256FromText(string(body), binaryName)
}

// GetLatestRelease 获取最新 Release 元数据（支持内存 TTL 缓存）。
func (c *Client) GetLatestRelease(ctx context.Context, forceRefresh bool) (*Release, error) {
	if !forceRefresh {
		c.mu.RLock()
		if c.cachedRel != nil && time.Since(c.cachedAt) < c.cacheTTL {
			rel := *c.cachedRel
			c.mu.RUnlock()
			return &rel, nil
		}
		c.mu.RUnlock()
	}

	url := fmt.Sprintf("%s/repos/%s/releases/latest", c.apiBase, c.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create latest release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "HomeAgent-Server")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get latest release from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned status %d for %s", resp.StatusCode, url)
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release json: %w", err)
	}

	c.mu.Lock()
	c.cachedRel = &rel
	c.cachedAt = time.Now()
	c.mu.Unlock()

	return &rel, nil
}

// CompareVersions 比较两个版本字符串（例如 "v0.6.11" 与 "v0.7.0"）。
// 返回值：-1 (v1 < v2), 0 (v1 == v2), 1 (v1 > v2)。
func CompareVersions(v1, v2 string) int {
	clean1 := strings.TrimPrefix(strings.TrimSpace(v1), "v")
	clean2 := strings.TrimPrefix(strings.TrimSpace(v2), "v")

	if clean1 == clean2 {
		return 0
	}

	parts1 := splitVersion(clean1)
	parts2 := splitVersion(clean2)

	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		p1 := 0
		if i < len(parts1) {
			p1 = parts1[i]
		}
		p2 := 0
		if i < len(parts2) {
			p2 = parts2[i]
		}
		if p1 < p2 {
			return -1
		}
		if p1 > p2 {
			return 1
		}
	}

	// 若数字段相同但含有预发布后缀（例如 "0.6.11" vs "0.6.11-beta"）
	if strings.Contains(clean1, "-") && !strings.Contains(clean2, "-") {
		return -1
	}
	if !strings.Contains(clean1, "-") && strings.Contains(clean2, "-") {
		return 1
	}

	return 0
}

func splitVersion(v string) []int {
	mainPart := strings.SplitN(v, "-", 2)[0]
	segments := strings.Split(mainPart, ".")
	res := make([]int, 0, len(segments))
	for _, seg := range segments {
		num, err := strconv.Atoi(seg)
		if err != nil {
			num = 0
		}
		res = append(res, num)
	}
	return res
}

func parseSHA256FromText(text, expectedName string) (string, error) {
	if strings.ContainsAny(expectedName, "/\\\r\n\x00") {
		return "", errors.New("invalid expected asset name")
	}
	line := strings.TrimSuffix(text, "\n")
	if strings.Contains(line, "\n") || strings.HasSuffix(line, "\r") || (len(line) != 66+len(expectedName) && len(line) != 65+len(expectedName)) {
		return "", errors.New("invalid sha256 sidecar format")
	}
	separatorLength := 2
	if len(line) == 65+len(expectedName) {
		separatorLength = 1
	}
	candidate := strings.ToLower(line[:64])
	if !isHex(candidate) || line[64:64+separatorLength] != strings.Repeat(" ", separatorLength) || line[64+separatorLength:] != expectedName {
		return "", errors.New("sha256 sidecar does not match asset name")
	}
	return candidate, nil
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
