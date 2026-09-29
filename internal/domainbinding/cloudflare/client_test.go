package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homeagent/internal/domainbinding"
)

type fakeDNSVerifier struct {
	addresses []string
	err       error
}

func TestErrorContractAndConfigValidation(t *testing.T) {
	providerErr := &Error{Kind: ErrorRateLimited, Retryable: true, Message: "limited"}
	if providerErr.Category() != "rate_limited" || !providerErr.CanRetry() || !strings.Contains(providerErr.Error(), "limited") {
		t.Fatalf("provider error contract=%v", providerErr)
	}
	if _, err := New(Config{}); err == nil {
		t.Fatal("empty provider configuration accepted")
	}
	if normalizedTTL(0) != 120 || normalizedTTL(300) != 300 {
		t.Fatal("unexpected TTL normalization")
	}
}

func (verifier fakeDNSVerifier) LookupAAAA(context.Context, string) ([]string, error) {
	return verifier.addresses, verifier.err
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func recordListFixture(t *testing.T) []byte {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(fixture(t, "record-success.json"), &response); err != nil {
		t.Fatal(err)
	}
	response["result"] = []any{response["result"]}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestClientObserveAndPatchPreservesRecordIdentityAndAttributes(t *testing.T) {
	var patchBody map[string]any
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatal("missing bearer token")
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone":
			w.Write(fixture(t, "zone-success.json"))
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone/dns_records":
			w.Write(recordListFixture(t))
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone/dns_records/record-id-redacted":
			w.Write(fixture(t, "record-success.json"))
		case r.Method == http.MethodPatch && r.URL.Path == "/zones/zone/dns_records/record-id-redacted":
			if err := json.NewDecoder(r.Body).Decode(&patchBody); err != nil {
				t.Fatal(err)
			}
			w.Write(fixture(t, "record-success.json"))
		default:
			http.NotFound(w, r)
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	client, err := New(Config{APIToken: "token", ZoneID: "zone", ManagedSuffix: "rokilai.online", BaseURL: server.URL, HTTPClient: server.Client(), DNSVerifier: fakeDNSVerifier{addresses: []string{"2001:db8::2"}}, VerificationDelay: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := client.Observe(context.Background(), domainbinding.ObserveRequest{FQDN: "host.rokilai.online"})
	if err != nil || observed.RecordID != "record-id-redacted" || observed.TTL != 120 || observed.Proxied {
		t.Fatalf("observed=%+v err=%v", observed, err)
	}
	applied, err := client.Apply(context.Background(), domainbinding.ApplyRequest{FQDN: "host.rokilai.online", ProviderRecordID: observed.RecordID, DesiredIPv6: "2001:db8::2", TTL: observed.TTL, Proxied: observed.Proxied})
	if err != nil {
		t.Fatal(err)
	}
	if applied.IPv6 != "2001:db8::2" || len(patchBody) != 1 || patchBody["content"] != "2001:db8::2" {
		t.Fatalf("applied=%+v patch=%v", applied, patchBody)
	}
}

func TestClientRejectsConflictsAndClassifiesProviderFailures(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantKind ErrorKind
	}{
		{"auth", http.StatusUnauthorized, "error-auth.json", ErrorAuthentication},
		{"permission", http.StatusForbidden, "error-auth.json", ErrorPermission},
		{"rate", http.StatusTooManyRequests, "error-rate-limit.json", ErrorRateLimited},
		{"upstream", http.StatusBadGateway, "error-rate-limit.json", ErrorUpstream},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				w.Write(fixture(t, test.body))
			}))
			defer server.Close()
			client, err := New(Config{APIToken: "token", ZoneID: "zone", ManagedSuffix: "rokilai.online", BaseURL: server.URL, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Observe(context.Background(), domainbinding.ObserveRequest{FQDN: "host.rokilai.online"})
			var providerErr *Error
			if !errors.As(err, &providerErr) || providerErr.Kind != test.wantKind {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestClientCreatesAAAARecordOnlyWhenExactNameIsEmpty(t *testing.T) {
	var createBody map[string]any
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone":
			w.Write(fixture(t, "zone-success.json"))
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone/dns_records" && requests < 4:
			w.Write(fixture(t, "records-empty-success.json"))
		case r.Method == http.MethodPost && r.URL.Path == "/zones/zone/dns_records":
			if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
				t.Fatal(err)
			}
			w.Write(fixture(t, "record-success.json"))
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone/dns_records/record-id-redacted":
			w.Write(fixture(t, "record-success.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(Config{APIToken: "token", ZoneID: "zone", ManagedSuffix: "rokilai.online", BaseURL: server.URL, HTTPClient: server.Client(), DNSVerifier: fakeDNSVerifier{addresses: []string{"2001:db8::2"}}, VerificationDelay: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Apply(context.Background(), domainbinding.ApplyRequest{FQDN: "host.rokilai.online", DesiredIPv6: "2001:db8::2", TTL: 120})
	if err != nil {
		t.Fatal(err)
	}
	if result.RecordID != "record-id-redacted" || createBody["type"] != "AAAA" || createBody["name"] != "host.rokilai.online" || createBody["content"] != "2001:db8::2" {
		t.Fatalf("result=%+v create=%v", result, createBody)
	}
}

func TestClientRejectsMalformedResponseAndRecordConflicts(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("not-json")) }))
		defer server.Close()
		client, err := New(Config{APIToken: "token", ZoneID: "zone", ManagedSuffix: "rokilai.online", BaseURL: server.URL, HTTPClient: server.Client()})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Observe(context.Background(), domainbinding.ObserveRequest{FQDN: "host.rokilai.online"})
		var providerErr *Error
		if !errors.As(err, &providerErr) || providerErr.Kind != ErrorMalformed {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("multiple records", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/zones/zone" {
				w.Write(fixture(t, "zone-success.json"))
				return
			}
			var envelope map[string]any
			if err := json.Unmarshal(recordListFixture(t), &envelope); err != nil {
				t.Fatal(err)
			}
			envelope["result"] = append(envelope["result"].([]any), envelope["result"].([]any)[0])
			json.NewEncoder(w).Encode(envelope)
		}))
		defer server.Close()
		client, err := New(Config{APIToken: "token", ZoneID: "zone", ManagedSuffix: "rokilai.online", BaseURL: server.URL, HTTPClient: server.Client()})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Observe(context.Background(), domainbinding.ObserveRequest{FQDN: "host.rokilai.online"})
		var providerErr *Error
		if !errors.As(err, &providerErr) || providerErr.Kind != ErrorRecordConflict {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestClientDoesNotReportSyncedWhenAuthoritativeDNSDiffers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if strings.Contains(r.URL.Path, "/dns_records") {
				if strings.HasSuffix(r.URL.Path, "/record-id-redacted") {
					w.Write(fixture(t, "record-success.json"))
					return
				}
				w.Write(recordListFixture(t))
				return
			}
			w.Write(fixture(t, "zone-success.json"))
		case http.MethodPatch:
			w.Write(fixture(t, "record-success.json"))
		}
	}))
	defer server.Close()
	client, err := New(Config{APIToken: "token", ZoneID: "zone", ManagedSuffix: "rokilai.online", BaseURL: server.URL, HTTPClient: server.Client(), DNSVerifier: fakeDNSVerifier{addresses: []string{"2001:db8::9"}}, VerificationDelay: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Apply(context.Background(), domainbinding.ApplyRequest{FQDN: "host.rokilai.online", ProviderRecordID: "record-id-redacted", DesiredIPv6: "2001:db8::2", TTL: 120})
	var providerErr *Error
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorDNSNotConverged {
		t.Fatalf("err=%v", err)
	}
	if result.RecordID != "record-id-redacted" || result.IPv6 != "2001:db8::2" {
		t.Fatalf("DNS convergence failure must retain applied record identity, result=%+v", result)
	}
}
