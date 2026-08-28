// Package cloudflare 实现基于 Cloudflare v4 REST API 的 DNSPublisher 接口。
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// Config 封装 Cloudflare API 客户端的身份令牌与域名配置。
type Config struct {
	APIToken   string
	ZoneID     string // 可选；留空时自动根据域名记录后缀反查
	BaseURL    string // 可选；默认使用 https://api.cloudflare.com/client/v4
	HTTPClient *http.Client
}

// Client 封装与 Cloudflare DNS API 的 HTTP 通信客户端。
type Client struct {
	token      string
	zoneID     string
	baseURL    string
	httpClient *http.Client
}

// NewClient 校验 API 令牌并初始化 Cloudflare 客户端。
func NewClient(cfg Config) (*Client, error) {

	if strings.TrimSpace(cfg.APIToken) == "" {
		return nil, errors.New("cloudflare api token is required")
	}
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.cloudflare.com/client/v4"
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{
		token:      strings.TrimSpace(cfg.APIToken),
		zoneID:     strings.TrimSpace(cfg.ZoneID),
		baseURL:    baseURL,
		httpClient: client,
	}, nil
}

type cfResponse[T any] struct {
	Success  bool     `json:"success"`
	Errors   []cfErr  `json:"errors"`
	Messages []string `json:"messages"`
	Result   T        `json:"result"`
}

type cfErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cfZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cfDNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
}

func (c *Client) getZoneID(ctx context.Context, record string) (string, error) {
	if c.zoneID != "" {
		return c.zoneID, nil
	}

	// Try extracting apex domain or query matching zones
	parts := strings.Split(record, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid record domain: %s", record)
	}

	reqURL := fmt.Sprintf("%s/zones?status=active", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to query zones: %w", err)
	}
	defer resp.Body.Close()

	var res cfResponse[[]cfZone]
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", fmt.Errorf("invalid response from cloudflare: %w", err)
	}
	if !res.Success || len(res.Result) == 0 {
		return "", fmt.Errorf("no active zone found for domain %s", record)
	}

	// Find the best matching zone (longest suffix)
	var bestZone string
	var bestLen int
	for _, z := range res.Result {
		if strings.HasSuffix(record, z.Name) && len(z.Name) > bestLen {
			bestZone = z.ID
			bestLen = len(z.Name)
		}
	}
	if bestZone == "" {
		return "", fmt.Errorf("no matching zone found for record %s", record)
	}
	return bestZone, nil
}

// GetAAAA 向 Cloudflare 查询匹配指定域名的当前活动 AAAA 解析记录。
func (c *Client) GetAAAA(ctx context.Context, record string) ([]netip.Addr, error) {
	zoneID, err := c.getZoneID(ctx, record)
	if err != nil {
		return nil, err
	}

	reqURL := fmt.Sprintf("%s/zones/%s/dns_records?type=AAAA&name=%s", c.baseURL, zoneID, record)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var res cfResponse[[]cfDNSRecord]
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	if !res.Success {
		return nil, formatErrors(res.Errors)
	}

	var addrs []netip.Addr
	for _, rec := range res.Result {
		ip, err := netip.ParseAddr(rec.Content)
		if err == nil && ip.IsValid() {
			addrs = append(addrs, ip)
		}
	}
	return addrs, nil
}

// UpsertAAAA 创建或幂等更新指定域名的 AAAA DNS 解析记录。
func (c *Client) UpsertAAAA(ctx context.Context, record string, address netip.Addr, ttl int) error {
	if ttl <= 0 {
		ttl = 120
	}
	zoneID, err := c.getZoneID(ctx, record)
	if err != nil {
		return err
	}

	// List existing records to determine create vs update
	reqURL := fmt.Sprintf("%s/zones/%s/dns_records?type=AAAA&name=%s", c.baseURL, zoneID, record)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var listRes cfResponse[[]cfDNSRecord]
	if err := json.NewDecoder(resp.Body).Decode(&listRes); err != nil {
		return err
	}
	if !listRes.Success {
		return formatErrors(listRes.Errors)
	}

	payload := map[string]any{
		"type":    "AAAA",
		"name":    record,
		"content": address.String(),
		"ttl":     ttl,
		"proxied": false,
	}
	bodyBytes, _ := json.Marshal(payload)

	if len(listRes.Result) > 0 {
		existing := listRes.Result[0]
		if existing.Content == address.String() && existing.TTL == ttl {
			// Idempotent no-op
			return nil
		}
		// Update record
		putURL := fmt.Sprintf("%s/zones/%s/dns_records/%s", c.baseURL, zoneID, existing.ID)
		putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, putURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return err
		}
		putReq.Header.Set("Authorization", "Bearer "+c.token)
		putReq.Header.Set("Content-Type", "application/json")

		putResp, err := c.httpClient.Do(putReq)
		if err != nil {
			return err
		}
		defer putResp.Body.Close()

		var putRes cfResponse[cfDNSRecord]
		if err := json.NewDecoder(putResp.Body).Decode(&putRes); err != nil {
			return err
		}
		if !putRes.Success {
			return formatErrors(putRes.Errors)
		}
		return nil
	}

	// Create record
	postURL := fmt.Sprintf("%s/zones/%s/dns_records", c.baseURL, zoneID)
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, postURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	postReq.Header.Set("Authorization", "Bearer "+c.token)
	postReq.Header.Set("Content-Type", "application/json")

	postResp, err := c.httpClient.Do(postReq)
	if err != nil {
		return err
	}
	defer postResp.Body.Close()

	var postRes cfResponse[cfDNSRecord]
	if err := json.NewDecoder(postResp.Body).Decode(&postRes); err != nil {
		return err
	}
	if !postRes.Success {
		return formatErrors(postRes.Errors)
	}
	return nil
}

// DeleteAAAA 从 Cloudflare 中删除指定域名的所有 AAAA 记录。
func (c *Client) DeleteAAAA(ctx context.Context, record string) error {

	zoneID, err := c.getZoneID(ctx, record)
	if err != nil {
		return err
	}

	reqURL := fmt.Sprintf("%s/zones/%s/dns_records?type=AAAA&name=%s", c.baseURL, zoneID, record)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var listRes cfResponse[[]cfDNSRecord]
	if err := json.NewDecoder(resp.Body).Decode(&listRes); err != nil {
		return err
	}
	if !listRes.Success {
		return formatErrors(listRes.Errors)
	}

	for _, rec := range listRes.Result {
		delURL := fmt.Sprintf("%s/zones/%s/dns_records/%s", c.baseURL, zoneID, rec.ID)
		delReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, delURL, nil)
		if err != nil {
			return err
		}
		delReq.Header.Set("Authorization", "Bearer "+c.token)

		delResp, err := c.httpClient.Do(delReq)
		if err != nil {
			return err
		}
		delBody, _ := io.ReadAll(delResp.Body)
		delResp.Body.Close()

		var delRes cfResponse[any]
		if err := json.Unmarshal(delBody, &delRes); err == nil && !delRes.Success {
			return formatErrors(delRes.Errors)
		}
	}
	return nil
}

func formatErrors(errs []cfErr) error {
	var msgs []string
	for _, e := range errs {
		msgs = append(msgs, fmt.Sprintf("[%d] %s", e.Code, e.Message))
	}
	if len(msgs) == 0 {
		return errors.New("cloudflare request failed")
	}
	return errors.New(strings.Join(msgs, "; "))
}
