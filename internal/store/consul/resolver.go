// Package consul 提供基于 Go 标准库 net/http 原生实现 Consul 服务发现解析能力
package consul

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config 定义 Consul 服务发现配置
type Config struct {
	Address     string        `json:"address" yaml:"address"`           // 例如 127.0.0.1:8500
	ServiceName string        `json:"service_name" yaml:"service_name"` // 例如 mysql-proxy-service
	Timeout     time.Duration `json:"timeout" yaml:"timeout"`           // 默认 3s
}

// ServiceHealthEntry 定义 Consul /v1/health/service 响应结构
type serviceHealthEntry struct {
	Node struct {
		Node    string `json:"Node"`
		Address string `json:"Address"`
	} `json:"Node"`
	Service struct {
		ID      string `json:"ID"`
		Service string `json:"Service"`
		Address string `json:"Address"`
		Port    int    `json:"Port"`
	} `json:"Service"`
}

// Resolver 提供基于 Consul 的服务发现动态解析器
type Resolver struct {
	client  *http.Client
	address string
	timeout time.Duration
}

// NewResolver 创建 Consul 服务解析器
func NewResolver(address string, timeout time.Duration) *Resolver {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	addr := strings.TrimSpace(address)
	if addr == "" {
		addr = "127.0.0.1:8500"
	}
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		addr = "http://" + addr
	}
	return &Resolver{
		client:  &http.Client{Timeout: timeout},
		address: strings.TrimRight(addr, "/"),
		timeout: timeout,
	}
}

// ResolveServiceAddress 查询指定服务名称的首个通过健康检查（passing）的 Host 与 Port
func (r *Resolver) ResolveServiceAddress(ctx context.Context, serviceName string) (string, int, error) {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return "", 0, fmt.Errorf("consul: service name cannot be empty")
	}

	reqURL := fmt.Sprintf("%s/v1/health/service/%s?passing=true", r.address, url.PathEscape(serviceName))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", 0, fmt.Errorf("consul: create request: %w", err)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("consul: query service %q: %w", serviceName, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("consul: unexpected status code %d from %s", resp.StatusCode, reqURL)
	}

	var entries []serviceHealthEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return "", 0, fmt.Errorf("consul: decode health entries: %w", err)
	}

	if len(entries) == 0 {
		return "", 0, fmt.Errorf("consul: no passing healthy instances found for service %q", serviceName)
	}

	entry := entries[0]
	host := entry.Service.Address
	if host == "" {
		host = entry.Node.Address
	}
	port := entry.Service.Port
	if port <= 0 {
		port = 3306
	}

	return host, port, nil
}

// BuildMySQLDSN 结合 Consul 动态解析出的 Host/Port 与用户名密码构建 MySQL DSN
func (r *Resolver) BuildMySQLDSN(ctx context.Context, serviceName, user, password, database string, params string) (string, error) {
	host, port, err := r.ResolveServiceAddress(ctx, serviceName)
	if err != nil {
		return "", err
	}

	// 若服务地址为 localhost 容器内部名且客户端在宿主机上，允许做 loopback 适配
	if host == "mysql-proxy" || host == "rent-mysql" {
		// 针对本地常见开发拓扑，如果无法直接解析容器名，保留使用配置或尝试直连
	}

	var authPart string
	if password != "" {
		authPart = fmt.Sprintf("%s:%s@", user, password)
	} else if user != "" {
		authPart = fmt.Sprintf("%s@", user)
	}

	if params == "" {
		params = "charset=utf8mb4&parseTime=True&loc=Local"
	}
	if !strings.HasPrefix(params, "?") {
		params = "?" + params
	}

	dsn := fmt.Sprintf("%stcp(%s:%d)/%s%s", authPart, host, port, database, params)
	return dsn, nil
}
