// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	for _, v := range []string{
		// email
		"jane@example.com", "/invite/jane.doe+gtm@mail.example.co.uk", "mailto:a@b.io",
		// IPv4 and IPv6
		"10.1.2.3", "client 192.168.0.1 said hi", "2001:db8::1", "fe80::1ff:fe23:4567:890a", "::1", "::ffff:10.0.0.1",
		// UUID
		"123e4567-e89b-12d3-a456-426614174000", "/u/123E4567-E89B-12D3-A456-426614174000/settings",
		// long hex and base64 tokens
		"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "abcdef0123456789abcdef01",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl", "key_" + strings.Repeat("aB3x", 7), "dGhpc0lzQVRva2VuMTIzNDU2Nzg5MA==",
		// phone numbers
		"+1 415 555 2671", "+44 20 7946 0958", "(415) 555-2671", "415.555.2671", "4155552671", "+4915112345678",
	} {
		if got := redact(v); got != Redacted {
			t.Errorf("redact(%q) = %q, want %q", v, got, Redacted)
		}
	}
	for _, v := range []string{
		"", "/pricing", "/docs/getting-started/installation", "page_viewed", "gtm:item-sent", "chat.started",
		"2026-11-26", "2026-11-26 10:00:00", "10:30", "v1.2.3", "1.2.3.4.5", "Chrome 131.0.6778.86", "en-US", "US",
		"desktop", "pro", "monthly", "42", "1234567", "release-2026.11.26", "a:b:c", "/blog/how-we-built-forge-releases",
		"utm_source=newsletter", "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "abcdefghijklmnopqrstuvwxyz", "Mozilla/5.0 (X11; Linux x86_64)",
	} {
		if got := redact(v); got != v {
			t.Errorf("redact(%q) = %q, want it kept", v, got)
		}
	}
}
