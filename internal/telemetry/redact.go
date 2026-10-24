// Copyright (c) 2025 Reliant Labs
package telemetry

import "regexp"

const (
	truncatedMarker = " [truncated]"
	redactedMarker  = "[redacted]"
)

// secretPatterns redact credential shapes from text that leaves the process.
var secretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	// URL userinfo: postgres://user:password@host
	{regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.\-]*://)[^\s/@:]+:[^\s/@]+@`), "${1}" + redactedMarker + "@"},
	// key=value / "key": "value" for credential-named keys, including an
	// Authorization header's scheme so the whole credential goes at once.
	{regexp.MustCompile(`(?i)\b(authorization|x-api-key|api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|client[_-]?secret|secret|password|passwd|token)(["']?\s*[:=]\s*["']?)(?:(?:bearer|basic|token)\s+)?[^\s"'&,;]+`), "${1}${2}" + redactedMarker},
	// A bare authorization scheme outside a key=value pair.
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=\-]+`), "${1} " + redactedMarker},
	// JWTs.
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]*`), redactedMarker},
	// Provider and platform key prefixes.
	{regexp.MustCompile(`\b(sk-ant-[A-Za-z0-9_\-]{8,}|sk-[A-Za-z0-9_\-]{16,}|sk_(?:live|test)_[A-Za-z0-9]{8,}|(?:rlat|dpat|gho|ghp|ghu|ghs|ghr|github_pat|glpat|xox[abprs])[_\-][A-Za-z0-9_\-]{10,}|AIza[0-9A-Za-z_\-]{20,}|ya29\.[0-9A-Za-z_\-]+|AKIA[0-9A-Z]{16})`), redactedMarker},
	// Long opaque runs (digests, base64 blobs). '-', '/' and '.' are excluded so
	// UUIDs, paths and dotted names are not swallowed.
	{regexp.MustCompile(`[A-Za-z0-9+=_]{40,}`), redactedMarker},
}

func redactSecrets(s string) string {
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}
