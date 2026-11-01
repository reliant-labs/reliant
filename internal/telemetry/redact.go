// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"regexp"
	"strings"
)

const (
	truncatedMarker = " [truncated]"
	redactedMarker  = "[redacted]"
)

// A pattern replaces every match of re with repl, or, when fn is set, with
// fn(groups): groups[0] is the whole match and groups[i] submatch i ("" when
// it did not participate). fn decides what a match becomes, including leaving
// it as it was, for the shapes a regular expression alone cannot tell apart
// (RE2 has no lookaround).
type pattern struct {
	re   *regexp.Regexp
	repl string
	fn   func(groups []string) string
}

// secretPatterns redact credential shapes from text that leaves the process.
// They are forge/pkg/redact's, pattern for pattern; keep the two identical,
// and replace this copy with an import of that package at the forge pin bump
// that carries the AWS secret, cookie and claim-link patterns.
var secretPatterns = []pattern{
	// URL userinfo: postgres://user:password@host
	{re: regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.\-]*://)[^\s/@:]+:[^\s/@]+@`), repl: "${1}" + redactedMarker + "@"},
	// A PEM private key block, whole. It goes first so a JSON "private_key"
	// value is not cut at its first space. An unterminated block (truncated
	// evidence) is redacted to the end of the text.
	{re: regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----(?:.*?-----END [A-Z ]*PRIVATE KEY-----|.*)`), repl: redactedMarker},
	// A Reliant claim link, https://<app>/claim#rcl_<token>: the fragment is
	// the claim token, and whoever holds it takes the deploy over. The whole
	// fragment goes, whatever its shape.
	{re: regexp.MustCompile(`(?i)(/claim/?#)[^\s"'<>()\[\]]+`), repl: "${1}" + redactedMarker},
	// A Cookie or Set-Cookie header (curl -v, a HAR, `"cookie": "..."`, a
	// JSON header list `"set-cookie": ["..."]`): the value runs to the end of
	// the line or the closing quote, so every name=value pair of a Cookie
	// header goes, not just the first.
	{re: regexp.MustCompile(`(?i)\b((?:set-)?cookie["']?[ \t]*:[ \t]*(?:\[[ \t]*["']|["']?))[^\r\n"']+`), repl: "${1}" + redactedMarker},
	// curl --cookie 'a=b; c=d' / --cookie=a=b. --cookie-jar <file> is kept.
	{re: regexp.MustCompile(`(?i)(--cookie(?:=|[ \t]+))(?:"[^"]*"|'[^']*'|[^\s"']+)`), repl: "${1}" + redactedMarker},
	// An AWS secret access key after its key name with only whitespace
	// between (a table row: `aws_secret_access_key   wJalr...`). The
	// key=value forms are the credential-named key rule's.
	{re: regexp.MustCompile(`(?i)\b((?:aws_?)?secret_?(?:access_?)?key[ \t]+)([A-Za-z0-9/+]{40})([^A-Za-z0-9/+=]|$)`), fn: func(g []string) string { return g[1] + redactedMarker + g[3] }},
	// --flag value / --flag=value for a credential-named flag.
	{re: regexp.MustCompile(`(?i)(--[A-Za-z0-9_-]*(?:password|passwd|secret|token|api[_-]?key|private[_-]?key|access[_-]?key)(?:[_-]?key)?)(=|[ \t]+)(["']?)[^\s"'&,;-][^\s"'&,;]*`), repl: "${1}${2}${3}" + redactedMarker},
	// key=value / "key": "value" for credential-named keys, including an
	// Authorization header's scheme so the whole credential goes at once. The
	// keyword may sit inside an identifier (DATABASE_PASSWORD, MY_API_TOKEN,
	// STRIPE_SECRET_KEY, SESSION_COOKIE); the value runs to whitespace, so '/',
	// '+' and '=' stay in it. A JSON list of strings loses its first element.
	{re: regexp.MustCompile(`(?i)([A-Za-z0-9_]*(?:authorization|password|passwd|secret|token|cookie|api[_-]?key|private[_-]?key|access[_-]?key)[A-Za-z0-9_]*)(["']?\s*[:=]\s*(?:\[\s*["']|["']?))(?:(?:bearer|basic|token)\s+)?[^\s"'&,;]+`), repl: "${1}${2}" + redactedMarker},
	// A bare authorization scheme outside a key=value pair.
	{re: regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=\-]+`), repl: "${1} " + redactedMarker},
	// JWTs.
	{re: regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]*`), repl: redactedMarker},
	// Provider and platform key prefixes. rcl_ is a Reliant claim token,
	// base64url, so it may hold '-' and '_'.
	{re: regexp.MustCompile(`\b(sk-ant-[A-Za-z0-9_\-]{8,}|sk-[A-Za-z0-9_\-]{16,}|sk_(?:live|test)_[A-Za-z0-9]{8,}|(?:rlat|dpat|rcl|gho|ghp|ghu|ghs|ghr|github_pat|glpat|xox[abprs])[_\-][A-Za-z0-9_\-]{10,}|AIza[0-9A-Za-z_\-]{20,}|ya29\.[0-9A-Za-z_\-]+|AKIA[0-9A-Z]{16})`), repl: redactedMarker},
	// A bare AWS secret access key (`aws configure get`, a credentials file
	// line without its key name): exactly 40 base64 characters, '/' and '+'
	// included, which the long-run rule below cannot take because it excludes
	// '/'. A random key holds an upper-case letter, a lower-case letter and a
	// digit all but always; requiring all three keeps paths and words of the
	// same length, and lower-case hex such as a git commit id, intact.
	{re: regexp.MustCompile(`[A-Za-z0-9/+]+`), fn: func(g []string) string {
		if len(g[0]) == 40 && hasUpperLowerDigit(g[0]) {
			return redactedMarker
		}
		return g[0]
	}},
	// Long opaque runs (digests, base64 blobs). '-', '/' and '.' are excluded
	// so UUIDs, paths and dotted names are not swallowed, and a git commit id
	// (exactly 40 lower-case hex) is kept: it names a commit, not a secret,
	// and a bug report without it cannot say which build failed.
	{re: regexp.MustCompile(`[A-Za-z0-9+=_]{40,}`), fn: func(g []string) string {
		if isGitCommitID(g[0]) {
			return g[0]
		}
		return redactedMarker
	}},
}

func redactSecrets(s string) string {
	for _, p := range secretPatterns {
		if p.fn != nil {
			s = replaceFunc(p.re, s, p.fn)
			continue
		}
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

// replaceFunc is ReplaceAllStringFunc with the submatches handed to fn.
func replaceFunc(re *regexp.Regexp, s string, fn func(groups []string) string) string {
	matches := re.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		groups := make([]string, len(m)/2)
		for i := range groups {
			if m[2*i] >= 0 {
				groups[i] = s[m[2*i]:m[2*i+1]]
			}
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(fn(groups))
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

func hasUpperLowerDigit(s string) bool {
	var upper, lower, digit bool
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= 'a' && c <= 'z':
			lower = true
		case c >= '0' && c <= '9':
			digit = true
		}
	}
	return upper && lower && digit
}

func isGitCommitID(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
