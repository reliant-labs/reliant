package observe

import (
	"context"
	"log/slog"
	"net"
	"regexp"
	"strings"
	"sync"
)

// The product-event PII guard. A property is a tag: an enum, a boolean, a
// count or a bucket, never free text. Track (and the CLI emitter, cliev)
// drop a property whose value looks like personal data or a credential
// before the record is written, and warn once per property key, naming the
// key and never the value. The patterns match the gateway's browser scrub
// (control-plane deploy/kcl/lib/observability/gateway.k,
// _BROWSER_SECRET_PATTERNS) plus emails and IP addresses; `forge lint`'s
// forgeconv-product-event-pii rule catches the property names that invite
// them (email, name, ip, url, path, token) at review time.
var piiPatterns = []*regexp.Regexp{
	// Email addresses.
	regexp.MustCompile(`[^\s@<>()"',;:]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`),
	// IPv4 addresses (IPv6 is checked with net.ParseIP below).
	regexp.MustCompile(`\b(?:25[0-5]|2[0-4]\d|1?\d?\d)(?:\.(?:25[0-5]|2[0-4]\d|1?\d?\d)){3}\b`),
	// URL userinfo: postgres://user:password@host.
	regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s/@:]+:[^\s/@]+@`),
	// Credential-named key=value pairs.
	regexp.MustCompile(`(?i)\b(?:authorization|x-api-key|api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|client[_-]?secret|secret|password|passwd|token)["']?\s*[:=]`),
	// A bare authorization scheme.
	regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`),
	// JWTs.
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*`),
	// Provider and platform key prefixes.
	regexp.MustCompile(`\b(?:sk-ant-[A-Za-z0-9_-]{8,}|sk-[A-Za-z0-9_-]{16,}|sk_(?:live|test)_[A-Za-z0-9]{8,}|(?:rlat|dpat|gho|ghp|ghu|ghs|ghr|github_pat|glpat|xox[abprs])[_-][A-Za-z0-9_-]{10,}|AIza[0-9A-Za-z_-]{20,}|ya29\.[0-9A-Za-z_-]+|AKIA[0-9A-Z]{16})`),
	// Long opaque runs (digests, base64 blobs). '-', '/' and '.' are
	// excluded so UUIDs, versions and dotted names survive.
	regexp.MustCompile(`[A-Za-z0-9+=_]{40,}`),
}

// LooksLikePII reports whether a product-event property value looks like
// personal data or a credential: an email, an IP address, a JWT, a bearer
// token, a provider key, URL userinfo, a credential key=value pair or a long
// opaque run. Track and cliev drop such values; an emitter of another kind
// can call it to apply the same rule.
func LooksLikePII(v string) bool { return looksLikePII(v) }

func looksLikePII(v string) bool {
	if v == "" {
		return false
	}
	if strings.Contains(v, ":") && net.ParseIP(strings.Trim(v, "[]")) != nil {
		return true
	}
	for _, re := range piiPatterns {
		if re.MatchString(v) {
			return true
		}
	}
	return false
}

// piiWarned holds the property keys already warned about, so a caller that
// sends PII on every event logs once per key per process.
var piiWarned sync.Map

func warnPIIDropped(ctx context.Context, event, key string) {
	if _, seen := piiWarned.LoadOrStore(key, struct{}{}); seen {
		return
	}
	FromContext(ctx).WarnContext(ctx, "product event property dropped: the value looks like personal data or a credential; tags are enums, booleans, counts or buckets",
		slog.String("event", event), slog.String("property", key))
}
