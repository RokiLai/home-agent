package cloudflare

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestCloudflareClient_GetUpsertDelete(t *testing.T) {
	var records = map[string]string{
		"rec-1": "2001:db8:10::1",
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token" {
			w.WriteHeader(401)
			w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`))
			return
		}

		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/zones/test-zone/dns_records"):
			if len(records) == 0 {
				w.Write([]byte(`{"success":true,"result":[],"errors":[]}`))
				return
			}
			var items []string
			for id, ip := range records {
				items = append(items, `{"id":"`+id+`","type":"AAAA","name":"node.example.com","content":"`+ip+`","ttl":120}`)
			}
			w.Write([]byte(`{"success":true,"result":[` + strings.Join(items, ",") + `],"errors":[]}`))

		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/zones/test-zone/dns_records"):
			records["rec-new"] = "2001:db8:10::2"
			w.Write([]byte(`{"success":true,"result":{"id":"rec-new","type":"AAAA","name":"node.example.com","content":"2001:db8:10::2","ttl":120},"errors":[]}`))

		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/zones/test-zone/dns_records/rec-1"):
			records["rec-1"] = "2001:db8:10::2"
			w.Write([]byte(`{"success":true,"result":{"id":"rec-1","type":"AAAA","name":"node.example.com","content":"2001:db8:10::2","ttl":120},"errors":[]}`))

		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/zones/test-zone/dns_records/rec-1"):
			delete(records, "rec-1")
			w.Write([]byte(`{"success":true,"result":{"id":"rec-1"},"errors":[]}`))

		default:
			http.NotFound(w, r)
		}
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	client, err := NewClient(Config{
		APIToken: "test-token",
		ZoneID:   "test-zone",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	// 1. GetAAAA
	addrs, err := client.GetAAAA(ctx, "node.example.com")
	if err != nil {
		t.Fatalf("GetAAAA failed: %v", err)
	}
	if len(addrs) != 1 || addrs[0].String() != "2001:db8:10::1" {
		t.Fatalf("expected 2001:db8:10::1, got %v", addrs)
	}

	// 2. UpsertAAAA (update existing rec-1)
	newIP := netip.MustParseAddr("2001:db8:10::2")
	if err := client.UpsertAAAA(ctx, "node.example.com", newIP, 120); err != nil {
		t.Fatalf("UpsertAAAA failed: %v", err)
	}
	if records["rec-1"] != "2001:db8:10::2" {
		t.Errorf("record was not updated in mock server: %s", records["rec-1"])
	}

	// 3. DeleteAAAA
	if err := client.DeleteAAAA(ctx, "node.example.com"); err != nil {
		t.Fatalf("DeleteAAAA failed: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected records to be deleted, got %v", records)
	}
}
