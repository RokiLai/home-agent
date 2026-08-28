package webhook

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"homeagent/internal/alerting"
	"homeagent/internal/health"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestWebhook_ChannelDelivery(t *testing.T) {
	var receivedEvent string
	var receivedSig string
	var receivedBody []byte

	secret := "test-secret-key-at-least-32-bytes-long"

	mockTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		receivedEvent = req.Header.Get("X-HomeAgent-Event")
		receivedSig = req.Header.Get("X-HomeAgent-Signature")
		receivedBody, _ = io.ReadAll(req.Body)

		tsHeader := req.Header.Get("X-HomeAgent-Timestamp")
		expectedSig := ComputeSignature(secret, tsHeader, receivedBody)
		if receivedSig != expectedSig {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Body:       io.NopCloser(bytes.NewReader([]byte("unauthorized"))),
				Header:     make(http.Header),
			}, nil
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"status":"ok"}`))),
			Header:     make(http.Header),
		}, nil
	})

	ch, err := NewChannel(Config{
		ID:        "wh-test",
		URL:       "https://webhook.example.com/alerts",
		Secret:    secret,
		Timeout:   2 * time.Second,
		Transport: mockTransport,
	})
	if err != nil {
		t.Fatalf("failed to create webhook channel: %v", err)
	}

	notif := alerting.Notification{
		SchemaVersion: 1,
		Event:         "alert.firing",
		DeliveryID:    "dlv-test-1",
		SentAt:        time.Now().UTC(),
		Alert: alerting.NotificationAlert{
			ID:         "alt-1",
			Status:     "firing",
			Severity:   health.SeverityCritical,
			ReasonCode: "device_offline",
			Summary:    "设备离线测试",
		},
		Device: alerting.NotificationDev{
			ID:           "dev-1",
			DisplayName:  "Test Dev",
			HealthStatus: health.StatusOffline,
		},
	}

	res := ch.Deliver(context.Background(), notif)
	if res.StatusCode != 200 || res.Retryable {
		t.Fatalf("expected 200 OK delivery, got %+v", res)
	}
	if receivedEvent != "alert.firing" {
		t.Fatalf("expected alert.firing event header, got %s", receivedEvent)
	}
	if !strings.Contains(string(receivedBody), "dlv-test-1") {
		t.Fatalf("expected delivery id in body")
	}
}

func TestWebhook_RedirectAndErrors(t *testing.T) {
	// 测试重定向拒绝
	mockTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		hdr := make(http.Header)
		hdr.Set("Location", "https://other.example.com/alerts")
		return &http.Response{
			StatusCode: http.StatusFound,
			Body:       io.NopCloser(bytes.NewReader([]byte("redirecting"))),
			Header:     hdr,
		}, nil
	})

	chRedir, err := NewChannel(Config{
		ID:        "wh-redir",
		URL:       "https://webhook.example.com/alerts",
		Secret:    "secret-12345678901234567890123456789012",
		Timeout:   2 * time.Second,
		Transport: mockTransport,
	})
	if err != nil {
		t.Fatal(err)
	}

	notif := alerting.Notification{
		Event:      "alert.firing",
		DeliveryID: "dlv-2",
		SentAt:     time.Now().UTC(),
	}

	resRedir := chRedir.Deliver(context.Background(), notif)
	if resRedir.StatusCode != http.StatusFound || resRedir.Retryable || resRedir.ErrorCode != "redirect_rejected" {
		t.Fatalf("expected redirect_rejected, got %+v", resRedir)
	}
}

