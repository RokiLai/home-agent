package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"homeagent/internal/domainbinding"
)

type ErrorKind string

const (
	ErrorAuthentication  ErrorKind = "authentication"
	ErrorPermission      ErrorKind = "permission"
	ErrorZone            ErrorKind = "zone"
	ErrorRecordConflict  ErrorKind = "record_conflict"
	ErrorRecordIdentity  ErrorKind = "record_identity"
	ErrorRateLimited     ErrorKind = "rate_limited"
	ErrorUpstream        ErrorKind = "upstream"
	ErrorTimeout         ErrorKind = "timeout"
	ErrorMalformed       ErrorKind = "malformed_response"
	ErrorDNSNotConverged ErrorKind = "dns_not_converged"
)

type Error struct {
	Kind      ErrorKind
	Retryable bool
	Status    int
	Code      int
	Message   string
}

func (err *Error) Error() string    { return fmt.Sprintf("cloudflare %s: %s", err.Kind, err.Message) }
func (err *Error) Category() string { return string(err.Kind) }
func (err *Error) CanRetry() bool   { return err.Retryable }

type DNSVerifier interface {
	LookupAAAA(context.Context, string) ([]string, error)
}

type Config struct {
	APIToken          string
	ZoneID            string
	ManagedSuffix     string
	BaseURL           string
	HTTPClient        *http.Client
	DNSVerifier       DNSVerifier
	VerificationDelay time.Duration
}

type Client struct {
	token             string
	zoneID            string
	managedSuffix     string
	baseURL           string
	httpClient        *http.Client
	dnsVerifier       DNSVerifier
	verificationDelay time.Duration
}

type response[T any] struct {
	Success bool       `json:"success"`
	Errors  []apiError `json:"errors"`
	Result  T          `json:"result"`
}
type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Paused bool   `json:"paused"`
}
type record struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
}

func New(config Config) (*Client, error) {
	if strings.TrimSpace(config.APIToken) == "" || strings.TrimSpace(config.ZoneID) == "" || strings.TrimSpace(config.ManagedSuffix) == "" {
		return nil, errors.New("cloudflare token, zone id and managed suffix are required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.cloudflare.com/client/v4"
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	verifier := config.DNSVerifier
	if verifier == nil {
		verifier = authoritativeVerifier{}
	}
	delay := config.VerificationDelay
	if delay == 0 {
		delay = 2 * time.Minute
	}
	return &Client{token: strings.TrimSpace(config.APIToken), zoneID: strings.TrimSpace(config.ZoneID), managedSuffix: strings.TrimSpace(config.ManagedSuffix), baseURL: baseURL, httpClient: httpClient, dnsVerifier: verifier, verificationDelay: delay}, nil
}

func (client *Client) Observe(ctx context.Context, request domainbinding.ObserveRequest) (domainbinding.RecordObservation, error) {
	if err := client.validateZone(ctx); err != nil {
		return domainbinding.RecordObservation{}, err
	}
	records, err := client.listRecords(ctx, request.FQDN)
	if err != nil {
		return domainbinding.RecordObservation{}, err
	}
	if len(records) == 0 {
		return domainbinding.RecordObservation{}, nil
	}
	if len(records) != 1 || records[0].Type != "AAAA" {
		return domainbinding.RecordObservation{}, &Error{Kind: ErrorRecordConflict, Message: "expected exactly one AAAA record"}
	}
	item := records[0]
	if request.ProviderRecordID != "" && request.ProviderRecordID != item.ID {
		return domainbinding.RecordObservation{}, &Error{Kind: ErrorRecordIdentity, Message: "record identity changed"}
	}
	if _, err := netip.ParseAddr(item.Content); err != nil {
		return domainbinding.RecordObservation{}, &Error{Kind: ErrorMalformed, Message: "record contains invalid IPv6"}
	}
	return observation(item), nil
}

func (client *Client) Apply(ctx context.Context, request domainbinding.ApplyRequest) (domainbinding.RecordObservation, error) {
	desired, err := netip.ParseAddr(strings.TrimSpace(request.DesiredIPv6))
	if err != nil || !desired.Is6() || desired.Is4In6() {
		return domainbinding.RecordObservation{}, &Error{Kind: ErrorMalformed, Message: "invalid desired IPv6"}
	}
	if err := client.validateZone(ctx); err != nil {
		return domainbinding.RecordObservation{}, err
	}
	var applied record
	if request.ProviderRecordID == "" {
		existing, listErr := client.listRecords(ctx, request.FQDN)
		if listErr != nil {
			return domainbinding.RecordObservation{}, listErr
		}
		if len(existing) > 0 {
			return domainbinding.RecordObservation{}, &Error{Kind: ErrorRecordConflict, Message: "record appeared before create"}
		}
		payload := map[string]any{"type": "AAAA", "name": request.FQDN, "content": desired.String(), "ttl": normalizedTTL(request.TTL), "proxied": request.Proxied}
		if err := client.call(ctx, http.MethodPost, "/zones/"+url.PathEscape(client.zoneID)+"/dns_records", payload, &applied); err != nil {
			return domainbinding.RecordObservation{}, err
		}
	} else {
		existing, listErr := client.listRecords(ctx, request.FQDN)
		if listErr != nil {
			return domainbinding.RecordObservation{}, listErr
		}
		if len(existing) != 1 || existing[0].Type != "AAAA" || existing[0].ID != request.ProviderRecordID {
			return domainbinding.RecordObservation{}, &Error{Kind: ErrorRecordIdentity, Message: "record identity changed before update"}
		}
		if err := client.call(ctx, http.MethodPatch, "/zones/"+url.PathEscape(client.zoneID)+"/dns_records/"+url.PathEscape(request.ProviderRecordID), map[string]any{"content": desired.String()}, &applied); err != nil {
			return domainbinding.RecordObservation{}, err
		}
	}
	if applied.ID == "" {
		return domainbinding.RecordObservation{}, &Error{Kind: ErrorMalformed, Message: "missing record id"}
	}
	if client.verificationDelay > 0 {
		timer := time.NewTimer(client.verificationDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return domainbinding.RecordObservation{}, ctx.Err()
		case <-timer.C:
		}
	}
	var reread record
	if err := client.call(ctx, http.MethodGet, "/zones/"+url.PathEscape(client.zoneID)+"/dns_records/"+url.PathEscape(applied.ID), nil, &reread); err != nil {
		return domainbinding.RecordObservation{}, err
	}
	if reread.Content != desired.String() || reread.TTL != applied.TTL || reread.Proxied != applied.Proxied {
		return domainbinding.RecordObservation{}, &Error{Kind: ErrorRecordIdentity, Message: "record changed after apply"}
	}
	addresses, err := client.dnsVerifier.LookupAAAA(ctx, request.FQDN)
	if err != nil {
		return observation(reread), &Error{Kind: ErrorDNSNotConverged, Retryable: true, Message: "authoritative DNS query failed"}
	}
	if len(addresses) != 1 || addresses[0] != desired.String() {
		return observation(reread), &Error{Kind: ErrorDNSNotConverged, Retryable: true, Message: "authoritative DNS does not match desired IPv6"}
	}
	return observation(reread), nil
}

func normalizedTTL(value int) int {
	if value <= 0 {
		return 120
	}
	return value
}
func observation(item record) domainbinding.RecordObservation {
	return domainbinding.RecordObservation{Exists: true, RecordID: item.ID, IPv6: item.Content, TTL: item.TTL, Proxied: item.Proxied, Comment: item.Comment}
}

func (client *Client) validateZone(ctx context.Context) error {
	var item zone
	if err := client.call(ctx, http.MethodGet, "/zones/"+url.PathEscape(client.zoneID), nil, &item); err != nil {
		return err
	}
	if item.Name != client.managedSuffix || item.Status != "active" || item.Paused {
		return &Error{Kind: ErrorZone, Message: "configured zone is not the active managed suffix"}
	}
	return nil
}

func (client *Client) listRecords(ctx context.Context, fqdn string) ([]record, error) {
	query := url.Values{"name": []string{fqdn}}
	var records []record
	err := client.call(ctx, http.MethodGet, "/zones/"+url.PathEscape(client.zoneID)+"/dns_records?"+query.Encode(), nil, &records)
	return records, err
}

func (client *Client) call(ctx context.Context, method, path string, payload any, result any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+client.token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return &Error{Kind: ErrorTimeout, Retryable: true, Message: "request timed out"}
		}
		return &Error{Kind: ErrorUpstream, Retryable: true, Message: "request failed"}
	}
	defer resp.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 2<<20))
	var envelope response[json.RawMessage]
	if err := decoder.Decode(&envelope); err != nil {
		return &Error{Kind: ErrorMalformed, Status: resp.StatusCode, Message: "invalid JSON response"}
	}
	if !envelope.Success || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return classify(resp.StatusCode, envelope.Errors)
	}
	if result != nil && len(envelope.Result) > 0 && string(envelope.Result) != "null" {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return &Error{Kind: ErrorMalformed, Status: resp.StatusCode, Message: "invalid result payload"}
		}
	}
	return nil
}

func classify(status int, apiErrors []apiError) error {
	code := 0
	message := "request rejected"
	if len(apiErrors) > 0 {
		code = apiErrors[0].Code
		message = apiErrors[0].Message
	}
	kind := ErrorUpstream
	retryable := status == http.StatusTooManyRequests || status >= 500
	switch status {
	case http.StatusUnauthorized:
		kind = ErrorAuthentication
		retryable = false
	case http.StatusForbidden:
		kind = ErrorPermission
		retryable = false
	case http.StatusNotFound:
		kind = ErrorRecordIdentity
		retryable = false
	case http.StatusTooManyRequests:
		kind = ErrorRateLimited
		retryable = true
	}
	return &Error{Kind: kind, Retryable: retryable, Status: status, Code: code, Message: message}
}

type authoritativeVerifier struct{}

func (authoritativeVerifier) LookupAAAA(ctx context.Context, fqdn string) ([]string, error) {
	parts := strings.Split(fqdn, ".")
	if len(parts) < 2 {
		return nil, errors.New("invalid fqdn")
	}
	zoneName := strings.Join(parts[len(parts)-2:], ".")
	nameservers, err := net.DefaultResolver.LookupNS(ctx, zoneName)
	if err != nil || len(nameservers) == 0 {
		return nil, fmt.Errorf("lookup authoritative nameserver: %w", err)
	}
	addresses, err := net.DefaultResolver.LookupHost(ctx, strings.TrimSuffix(nameservers[0].Host, "."))
	if err != nil || len(addresses) == 0 {
		return nil, fmt.Errorf("resolve authoritative nameserver: %w", err)
	}
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		dialer := net.Dialer{}
		return dialer.DialContext(ctx, "udp", net.JoinHostPort(addresses[0], "53"))
	}}
	items, err := resolver.LookupNetIP(ctx, "ip6", fqdn)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.String())
	}
	return result, nil
}
