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
		"10.1.2.3", "client 192.168.0.1 said hi", "10.1.2.0", "192.168.1.0", "34.120.5.0", "srv.10.1.2.3",
		"ip-10-1-2-3", "ip-10-1-2-3.ec2.internal", "2001:db8::1", "fe80::1ff:fe23:4567:890a", "::1", "::ffff:10.0.0.1",
		"10%2E1%2E2%2E3", "00:1A:2B:3C:4D:5E", "00-1a-2b-3c-4d-5e", "001a.2b3c.4d5e", "device 3c:22:fb:01:02:03",
		// UUID
		"123e4567-e89b-12d3-a456-426614174000", "/u/123E4567-E89B-12D3-A456-426614174000/settings",
		// tokens: long hex, base64, JWT, ULID, nanoid, cuid, an API key
		"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "abcdef0123456789abcdef01",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl", "dGhpc0lzQVRva2VuMTIzNDU2Nzg5MA==",
		"01ARZ3NDEKTSV4RRFFQ69G5FAV", "V1StGXR8_Z5jdHi6B-myT", "cjld2cjxh0000qzrmn831i7rn",
		"sk_" + "live_" + strings.Repeat("a1B2", 6), "sk_" + "live_" + "51HxYz" + "AbCdEfGhIjKlMn0pQr",
		"key_" + strings.Repeat("aB3x", 7), "ref=abc123def456ghi789", "01HZY8Q4N3K7XWJ2M5RTVB6C9D", "clx2k9p0q0000abcd1234efgh",
		// analytics client ids
		"GA1.2.1234567890.1732615200", "_ga=GA1.1.98765.43210",
		// prefixed ids and known secret formats (built here so no literal
		// in this file reads as a live secret)
		"cus_" + "NffrFeUfNV2Hib", "acct_" + "1032D82eZvKYlo2C", "org_" + "Ab12cd34Ef", "sub_" + "abcdefghij1",
		"AK" + "IA" + "IOSFODNN7EXAMPLE", "AS" + "IA" + "Y34FZKBOKMUTVV7A", "gh" + "o_" + strings.Repeat("a1B2", 6),
		"github" + "_pat_" + strings.Repeat("a1B2", 6), "rk_" + "test_" + "abcdEFGH1234", "pk_" + "live_" + "Zz9Yy8Xx7Ww6",
		"xox" + "b-" + "1234-5678-abcdEF", "AI" + "za" + strings.Repeat("Sy9_", 6), "glpat-" + strings.Repeat("xY3-", 5),
		"SG." + strings.Repeat("aB3_", 5) + "." + strings.Repeat("cD4-", 5), "-----" + "BEGIN CERTIFICATE-----",
		// numeric ids and numeric path segments
		"123456789012", "order 48213095512345", "12345678", "987654321", "/orders/4821309", "/u/123456/settings",
		"/a/999999?tab=1",
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
		"/blog/2026/11/26/launch", "/archive/1999", "/archive/2100", "20261126",
		// versions
		"v1.2.3", "1.75.0-beta.1", "131.0.6778.86", "Chrome 131.0.6778.86", "go1.23.4", "node-v22.11.0",
		"release-2026.11.26", "1.2.3.4.5", "4.0.0.0", "v10.0.0.1", "10.0.0.1.2", "300.1.2.3",
		// other ordinary values
		"en-US", "US", "desktop", "pro", "monthly", "42", "1234567", "19.99", "a:b:c", "/blog/how-we-built-forge-releases",
		"utm_source=newsletter", "Mozilla/5.0 (X11; Linux x86_64)",
		"100%", "/search?q=a%20b", "/p/123", "black_friday_2026", "internationalization",
		"/pricing/5000", "/1080", "/orders/48213", "/u/1234/settings", "/v/0042", "/a/99999?tab=1", "1234567",
		// campaign, product and category names with numbers in them (third review of #3843)
		"Black-Friday-2026", "Spring_Sale_2026", "Q4_Product_Launch", "Q4EnterpriseLaunch2026", "spring2026newsletter",
		"blackfriday2026sale", "Enterprise-Annual-v3", "gtm-strategy-2026Q4", "/docs/Getting-Started-v2",
		"Windows10Enterprise", "Spring-Sale-2026-Black-Friday-Q4-Launch",
		"0.1.2.3", "10.0.0.0", "4.0.0.0",
		// everyday names and paths
		"getting_started_guide", "/docs/api/reference", "kubernetes", "newsletter_signup_footer",
		"pricing_page_cta_click", "ENTERPRISE_ANNUAL_PLAN", "internationalization_settings", "checkout-completed",
		"/blog/announcing-forge-environments", "onboarding_checklist_step_3", "MacBook Pro", "Safari 18.1",
	} {
		if got := redact(v); got != v {
			t.Errorf("redact(%q) = %q, want it kept", v, got)
		}
	}
}

// acceptedFalsePositives are ordinary values redact redacts, kept here so the
// cost of the rule "catching an id wins over keeping a name" stays visible.
// Each is redacted; the reason says which detector takes it.
var acceptedFalsePositives = map[string]string{
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ": "a run of upper-case letters whose pairs do not read as English is base32-like",
	"abcdefghijklmnopqrstuvwxyz": "a run of letters whose pairs do not read as English is random letters",
	"AbCdEfGhIjKlMnOpQrStUvWxYz": "case that changes at every letter is a mixed-case token",
	"utm_newsletter2026":         "a lowercase prefix, _ and a body with a digit is a prefixed id (cus_..., org_...)",
	"MacBookPro16inch2023":       "a piece under 21 that switches between letters and digits 3 times is a token",
}

func TestAcceptedFalsePositives(t *testing.T) {
	for v, why := range acceptedFalsePositives {
		if got := redact(v); got != Redacted {
			t.Errorf("redact(%q) = %q: no longer a false positive (%s); move it to the keep list", v, got, why)
		}
	}
}
