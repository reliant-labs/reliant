package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

// DefaultWebhookMaxBodySize bounds how much of a webhook payload a
// handler reads. Webhook senders are unauthenticated until the signature
// is checked — and the signature is computed over the body — so the read
// has to be bounded BEFORE anything is trusted:
//
//	body, err := io.ReadAll(io.LimitReader(r.Body, middleware.DefaultWebhookMaxBodySize))
const DefaultWebhookMaxBodySize int64 = 1 << 20 // 1MiB

// WebhookEvent is one received webhook delivery, assembled by a handler
// after the signature checks out and handed to the processing function.
// It carries the raw body rather than a parsed payload because the shape
// is provider-specific — unmarshal Body into your own type.
type WebhookEvent struct {
	// ID uniquely identifies this delivery and is the deduplication key
	// (see WebhookEventID and DedupeStore).
	ID string
	// Source names the webhook the delivery arrived on.
	Source string
	// Headers are the inbound request headers.
	Headers http.Header
	// Body is the raw, size-limited request body the signature covered.
	Body []byte
	// ReceivedAt is when the delivery was accepted.
	ReceivedAt time.Time
}

// webhookEventIDHeaders are the delivery-ID headers, in priority order,
// that WebhookEventID recognises.
var webhookEventIDHeaders = []string{
	"X-Event-ID",
	"X-Webhook-ID",
	"X-Request-ID",
	"Stripe-Event-Id",
	"X-GitHub-Delivery",
}

// WebhookEventID returns the sender's unique delivery ID from the first
// recognised header, or "" when none is present.
//
// Providers do not agree on a header, and none of them use the
// Idempotency-Key convention that REST clients follow, so this covers the
// common ones: the generic X-Event-ID / X-Webhook-ID / X-Request-ID
// spellings plus Stripe-Event-Id and X-GitHub-Delivery. A provider that
// stamps something else is one header read away — pull it directly and
// fall back to this:
//
//	id := r.Header.Get("X-Acme-Delivery")
//	if id == "" {
//	    id = middleware.WebhookEventID(r.Header)
//	}
func WebhookEventID(headers http.Header) string {
	for _, h := range webhookEventIDHeaders {
		if id := headers.Get(h); id != "" {
			return id
		}
	}
	return ""
}

// VerifyHMACSHA256 reports whether signature is a valid HMAC-SHA256 of
// body under secret. signature is hex, with or without the "sha256="
// prefix some providers (GitHub) prepend, in either letter case.
//
// The comparison is constant-time, and a malformed signature is a plain
// false rather than an error, so call sites stay a single `if`:
//
//	if !middleware.VerifyHMACSHA256(body, r.Header.Get("X-Signature-256"), secret) {
//	    http.Error(w, "unauthorized", http.StatusUnauthorized)
//	    return
//	}
//
// An empty secret always fails: an unconfigured secret must not silently
// turn signature verification into a no-op.
func VerifyHMACSHA256(body []byte, signature, secret string) bool {
	if secret == "" || signature == "" {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), got)
}
