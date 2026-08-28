package consul

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConsulResolver_ResolveAndBuildDSN(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health/service/mysql-proxy-service" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[
				{
					"Node": {"Node": "node-1", "Address": "192.168.1.100"},
					"Service": {"ID": "mysql-1", "Service": "mysql-proxy-service", "Address": "127.0.0.1", "Port": 3306}
				}
			]`))
			return
		}
		if r.URL.Path == "/v1/health/service/empty-service" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if r.URL.Path == "/v1/health/service/error-service" {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	r := NewResolver(ts.URL, 2*time.Second)
	ctx := context.Background()

	// 1. 正常解析
	host, port, err := r.ResolveServiceAddress(ctx, "mysql-proxy-service")
	if err != nil {
		t.Fatalf("unexpected error resolving service: %v", err)
	}
	if host != "127.0.0.1" || port != 3306 {
		t.Fatalf("expected 127.0.0.1:3306, got %s:%d", host, port)
	}

	// 2. 构建 MySQL DSN
	dsn, err := r.BuildMySQLDSN(ctx, "mysql-proxy-service", "homeagent", "SecretPass123!", "homeagent", "")
	if err != nil {
		t.Fatalf("build MySQL DSN failed: %v", err)
	}
	expectedPrefix := "homeagent:SecretPass123!@tcp(127.0.0.1:3306)/homeagent"
	if !strings.HasPrefix(dsn, expectedPrefix) {
		t.Fatalf("expected DSN to start with %s, got %s", expectedPrefix, dsn)
	}

	// 3. 空节点反例
	_, _, err = r.ResolveServiceAddress(ctx, "empty-service")
	if err == nil || !strings.Contains(err.Error(), "no passing healthy instances") {
		t.Fatalf("expected no passing instances error, got %v", err)
	}

	// 4. 服务端错误反例
	_, _, err = r.ResolveServiceAddress(ctx, "error-service")
	if err == nil || !strings.Contains(err.Error(), "unexpected status code 500") {
		t.Fatalf("expected status code 500 error, got %v", err)
	}
}
