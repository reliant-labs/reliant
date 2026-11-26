// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	for _, v := range []string{
		// email, also URL-encoded (once and twice)
		"jane@example.com", "/invite/jane.doe+gtm@mail.example.co.uk", "mailto:a@b.io",
		"jane%40example.com", "/invite?to=jane%2Bgtm%40example.com", "jane%2540example.com",
		// IPv4, IPv6 and MAC addresses
		"10.1.2.3", "client 192.168.0.1 said hi", "2001:db8::1", "fe80::1ff:fe23:4567:890a", "::1", "::ffff:10.0.0.1",
		"10%2E1%2E2%2E3", "00:1A:2B:3C:4D:5E", "00-1a-2b-3c-4d-5e", "001a.2b3c.4d5e", "device 3c:22:fb:01:02:03",
		// UUID
		"123e4567-e89b-12d3-a456-426614174000", "/u/123E4567-E89B-12D3-A456-426614174000/settings",
		// tokens: long hex, base64, JWT, ULID, nanoid, cuid, an API key
		"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "abcdef0123456789abcdef01",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl", "dGhpc0lzQVRva2VuMTIzNDU2Nzg5MA==",
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", "V1StGXR8_Z5jdHi6B-myT", "cjld2cjxh0000qzrmn831i7rn",
		"sk_" + "live_" + strings.Repeat("a1B2", 6), "key_" + strings.Repeat("aB3x", 7), "ref=abc123def456ghi789",
		// numeric ids and numeric path segments
		"123456789012", "order 48213095512345", "/orders/48213", "/u/1234/settings", "/v/0042", "/a/99999?tab=1",
		// phone numbers
		"+1 415 555 2671", "+44 20 7946 0958", "(415) 555-2671", "415.555.2671", "4155552671", "+4915112345678",
		"%2B1%20415%20555%202671",
	} {
		if got := redact(v); got != Redacted {
			t.Errorf("redact(%q) = %q, want %q", v, got, Redacted)
		}
	}
	for _, v := range []string{
		"", "/pricing", "/docs/getting-started/installation", "page_viewed", "gtm:item-sent", "chat.started",
		// dates and times
		"2026-11-26", "2026-11-26 10:00:00", "2026-11-26T10:00:00Z", "2026-11-26T10:00:00.123456789Z",
		"2026-11-26T10:00:00+01:00", "20261126T103000Z", "2026.11.26", "26/11/2026", "Nov 26, 2026", "10:30", "10:30:15",
		"/blog/2026/11/26/launch", "/archive/1999",
		// versions
		"v1.2.3", "1.75.0-beta.1", "131.0.6778.86", "Chrome 131.0.6778.86", "go1.23.4", "node-v22.11.0",
		"release-2026.11.26", "1.2.3.4.5",
		// other ordinary values
		"en-US", "US", "desktop", "pro", "monthly", "42", "1234567", "19.99", "a:b:c", "/blog/how-we-built-forge-releases",
		"utm_source=newsletter", "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "abcdefghijklmnopqrstuvwxyz", "Mozilla/5.0 (X11; Linux x86_64)",
		"100%", "/search?q=a%20b", "/p/123", "black_friday_2026", "internationalization",
	} {
		if got := redact(v); got != v {
			t.Errorf("redact(%q) = %q, want it kept", v, got)
		}
	}
}
