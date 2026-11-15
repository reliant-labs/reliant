package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

func TestWebhookEventID(t *testing.T) {
	tests := []struct {
		name   string
		header string
		value  string
		want   string
	}{
		{"generic event id", "X-Event-ID", "evt-1", "evt-1"},
		{"generic webhook id", "X-Webhook-ID", "wh-1", "wh-1"},
		{"generic request id", "X-Request-ID", "req-1", "req-1"},
		{"stripe", "Stripe-Event-Id", "evt_1Pabc", "evt_1Pabc"},
		{"github", "X-GitHub-Delivery", "72d3162e-cc78", "72d3162e-cc78"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			h.Set(tt.header, tt.value)
			if got := WebhookEventID(h); got != tt.want {
				t.Fatalf("WebhookEventID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWebhookEventID_NoneMatch(t *testing.T) {
	h := http.Header{}
	h.Set("X-Acme-Delivery", "acme-1")
	if got := WebhookEventID(h); got != "" {
		t.Fatalf("WebhookEventID() = %q, want \"\" for an unrecognised header", got)
	}
}

func TestWebhookEventID_Priority(t *testing.T) {
	h := http.Header{}
	h.Set("Stripe-Event-Id", "evt_stripe")
	h.Set("X-Event-ID", "evt_generic")
	if got := WebhookEventID(h); got != "evt_generic" {
		t.Fatalf("WebhookEventID() = %q, want evt_generic (first recognised header wins)", got)
	}
}

func hexHMAC(t *testing.T, body []byte, secret string) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyHMACSHA256(t *testing.T) {
	body := []byte(`{"type":"order.created"}`)
	sig := hexHMAC(t, body, "s3cr3t")

	if !VerifyHMACSHA256(body, sig, "s3cr3t") {
		t.Fatal("valid signature rejected")
	}
	if !VerifyHMACSHA256(body, "sha256="+sig, "s3cr3t") {
		t.Fatal("sha256=-prefixed signature rejected")
	}
	if !VerifyHMACSHA256(body, strings.ToUpper(sig), "s3cr3t") {
		t.Fatal("uppercase hex signature rejected")
	}
	if VerifyHMACSHA256(body, sig, "wrong") {
		t.Fatal("signature accepted under the wrong secret")
	}
	if VerifyHMACSHA256([]byte(`{"type":"other"}`), sig, "s3cr3t") {
		t.Fatal("signature accepted over a tampered body")
	}
	if VerifyHMACSHA256(body, "not-hex", "s3cr3t") {
		t.Fatal("malformed signature accepted")
	}
	if VerifyHMACSHA256(body, "", "s3cr3t") {
		t.Fatal("empty signature accepted")
	}
}

// TestVerifyHMACSHA256_EmptySecret — an unconfigured secret must fail
// closed, never turn verification into a no-op that accepts everything.
func TestVerifyHMACSHA256_EmptySecret(t *testing.T) {
	body := []byte("payload")
	if VerifyHMACSHA256(body, hexHMAC(t, body, ""), "") {
		t.Fatal("empty secret accepted a signature")
	}
}
