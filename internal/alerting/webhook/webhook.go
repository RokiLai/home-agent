// Package webhook 提供基于 HMAC-SHA256 签名与防重定向的通用 HTTP Webhook 告警通道适配器。
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"homeagent/internal/alerting"
)

// Config 包含 Webhook 通道配置。
type Config struct {
	ID        string           `json:"id"`
	URL       string           `json:"url"`
	Secret    string           `json:"secret"`
	Timeout   time.Duration    `json:"timeout"`
	AllowHTTP bool             `json:"allow_http"` // 仅允许本机测试
	Transport http.RoundTripper `json:"-"`
}

// Channel 实现 alerting.Channel 接口。
type Channel struct {
	cfg    Config
	client *http.Client
}

// NewChannel 创建 Webhook 通道实例。
func NewChannel(cfg Config) (*Channel, error) {
	if strings.TrimSpace(cfg.ID) == "" {
		return nil, errors.New("empty webhook channel id")
	}
	if len([]byte(cfg.Secret)) < 32 {
		return nil, errors.New("webhook secret must be at least 32 bytes for secure HMAC-SHA256")
	}
	u, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil {
		return nil, fmt.Errorf("invalid webhook url: %w", err)
	}
	if u.User != nil {
		return nil, errors.New("webhook url must not contain userinfo credentials")
	}
	if u.Scheme != "https" {
		if !(cfg.AllowHTTP && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
			return nil, errors.New("webhook url must use https scheme")
		}
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	tr := cfg.Transport
	if tr == nil {
		tr = &http.Transport{
			MaxIdleConns:        10,
			IdleConnTimeout:     30 * time.Second,
			DisableCompression: false,
		}
	}

	client := &http.Client{
		Transport: tr,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// 严格禁止跟随 3xx 重定向，防止签名凭据泄露至未授权目标
			return http.ErrUseLastResponse
		},
	}

	return &Channel{
		cfg:    cfg,
		client: client,
	}, nil
}

// ID 返回通道 ID。
func (c *Channel) ID() string {
	return c.cfg.ID
}

// Type 返回通道类型。
func (c *Channel) Type() string {
	return "webhook"
}

// Deliver 执行 HTTP POST 告警投递与响应分类。
func (c *Channel) Deliver(ctx context.Context, n alerting.Notification) alerting.DeliveryResult {
	bodyBytes, err := json.Marshal(n)
	if err != nil {
		return alerting.DeliveryResult{
			Retryable:    false,
			ErrorCode:    "encode_error",
			ErrorMessage: err.Error(),
		}
	}

	timestamp := strconv.FormatInt(n.SentAt.Unix(), 10)
	sigPayload := timestamp + "." + string(bodyBytes)
	h := hmac.New(sha256.New, []byte(c.cfg.Secret))
	h.Write([]byte(sigPayload))
	signature := "v1=" + hex.EncodeToString(h.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(bodyBytes))
	if err != nil {
		return alerting.DeliveryResult{
			Retryable:    false,
			ErrorCode:    "request_build_error",
			ErrorMessage: err.Error(),
		}
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "HomeAgent-Webhook/1")
	req.Header.Set("X-HomeAgent-Event", n.Event)
	req.Header.Set("X-HomeAgent-Delivery", n.DeliveryID)
	req.Header.Set("X-HomeAgent-Timestamp", timestamp)
	req.Header.Set("X-HomeAgent-Signature", signature)

	resp, err := c.client.Do(req)
	if err != nil {
		return alerting.DeliveryResult{
			Retryable:    true,
			ErrorCode:    "transport_error",
			ErrorMessage: err.Error(),
		}
	}
	defer resp.Body.Close()

	// 限制读取最多 4 KiB 响应
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msgSummary := strings.TrimSpace(string(respBody))

	// 2xx 成功
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return alerting.DeliveryResult{
			ProviderMessageID: resp.Header.Get("X-Request-ID"),
			Retryable:         false,
			StatusCode:        resp.StatusCode,
		}
	}

	// 3xx 拒绝重定向视为配置错误
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return alerting.DeliveryResult{
			StatusCode:   resp.StatusCode,
			Retryable:    false,
			ErrorCode:    "redirect_rejected",
			ErrorMessage: fmt.Sprintf("server returned redirect status %d, webhook redirects are disallowed", resp.StatusCode),
		}
	}

	// 408, 425, 429 或 5xx 可重试
	if resp.StatusCode == 408 || resp.StatusCode == 425 || resp.StatusCode == 429 || resp.StatusCode >= 500 {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now().UTC())
		return alerting.DeliveryResult{
			StatusCode:   resp.StatusCode,
			Retryable:    true,
			RetryAfter:   retryAfter,
			ErrorCode:    fmt.Sprintf("http_%d", resp.StatusCode),
			ErrorMessage: msgSummary,
		}
	}

	// 其他 4xx 为永久配置/客户端错误
	return alerting.DeliveryResult{
		StatusCode:   resp.StatusCode,
		Retryable:    false,
		ErrorCode:    fmt.Sprintf("http_%d", resp.StatusCode),
		ErrorMessage: msgSummary,
	}
}

func parseRetryAfter(header string, now time.Time) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(header); err == nil {
		if seconds <= 0 {
			return 0
		}
		d := time.Duration(seconds) * time.Second
		if d > time.Hour {
			d = time.Hour
		}
		return d
	}
	if t, err := http.ParseTime(header); err == nil {
		d := t.Sub(now)
		if d <= 0 {
			return 0
		}
		if d > time.Hour {
			d = time.Hour
		}
		return d
	}
	return 0
}

// ComputeSignature 供单元测试验证签名生成一致性。
func ComputeSignature(secret, timestamp string, rawBody []byte) string {
	sigPayload := timestamp + "." + string(rawBody)
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(sigPayload))
	return "v1=" + hex.EncodeToString(h.Sum(nil))
}
