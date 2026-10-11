// Copyright (c) 2025 Reliant Labs
package logging

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	redacted = "[REDACTED]"

	// maxAttrBytes bounds a string attribute. A log line that carries a whole
	// chat message, tool output or diff is a leak whatever it is masked with,
	// so the cap is what keeps an accidental payload from being dumped.
	maxAttrBytes = 512
	// maxErrorBytes is the bound for an error value: error text is the one
	// attribute worth reading in full, and wrapped chains run long.
	maxErrorBytes = 1024
	// maxMessageBytes bounds the message itself.
	maxMessageBytes = 1024
	// maxScanBytes bounds how much of a value the patterns are run over, so a
	// multi-megabyte payload costs a slice, not a regex pass. The rest is
	// counted in the truncation marker.
	maxScanBytes = 8 * 1024
)

// secretValuePatterns mask a secret wherever it appears inside a string. Each
// replacement keeps what is safe to keep (the scheme, the variable name) so
// the line still says what kind of thing was there.
var secretValuePatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	// Credentials embedded in a URL: scheme://user:pass@host and
	// scheme://token@host.
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s:@"']+(?::[^/\s@"']*)?@`), "${1}" + redacted + "@"},
	// Secrets in a query string.
	{regexp.MustCompile(`(?i)([?&](?:access_token|id_token|refresh_token|token|api_?key|key|secret|signature|sig|password|code)=)[^&\s"']+`), "${1}" + redacted},
	// Authorization schemes.
	{regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "${1} " + redacted},
	// JWTs: three base64url segments, the first two starting with an encoded
	// JSON object.
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*`), redacted},
	// Known token shapes: Reliant access tokens, OpenAI/Anthropic keys,
	// GitHub tokens, Slack tokens, AWS access key ids, Google API keys.
	{regexp.MustCompile(`\brlat_[A-Za-z0-9_-]{8,}`), redacted},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`), redacted},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`), redacted},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`), redacted},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), redacted},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), redacted},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`), redacted},
	// name=value for names that are secrets. "=" only: "token: refused" is a
	// sentence about a token, not a token.
	{regexp.MustCompile(`(?i)\b((?:password|passwd|secret|api[_-]?key|access[_-]?token|auth[_-]?token|token)=)("[^"]*"|'[^']*'|[^\s,;&]+)`), "${1}" + redacted},
	// "name":"value" for the same names, as JSON or a Go %#v dump.
	{regexp.MustCompile(`(?i)("(?:password|passwd|secret|api[_-]?key|access[_-]?token|auth[_-]?token|token|authorization)"\s*:\s*)"[^"]*"`), `${1}"` + redacted + `"`},
}

// secretKeyPattern matches an attribute key that names a secret, whatever its
// value looks like. It is anchored to the end so token_count, max_tokens and
// tokenizer are left alone: those are the numbers an LLM-usage investigation
// needs, and none of them is a credential.
var secretKeyPattern = regexp.MustCompile(`(?i)(^|[_.-])(token|secret|password|passwd|authorization|cookie|set-cookie|credential|credentials|api_?key|apikey|private_?key|pat)$|(token|secret|password|passwd|apikey)$`)

// redactor is the ReplaceAttr that masks secrets and bounds sizes. It is
// stateless, so one value serves every handler derived from the logger.
type redactor struct{}

func newRedactor() redactor { return redactor{} }

// replaceAttr is slog's ReplaceAttr hook. It runs for every leaf attribute,
// including the built-in message, on both the JSON and the text handler.
func (redactor) replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.MessageKey {
		return slog.String(slog.MessageKey, sanitize(a.Value.String(), maxMessageBytes))
	}
	if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == slog.LevelKey || a.Key == slog.SourceKey) {
		return a
	}

	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		if secretKeyPattern.MatchString(a.Key) && v.String() != "" {
			return slog.String(a.Key, redacted)
		}
		return slog.String(a.Key, sanitize(v.String(), maxAttrBytes))
	case slog.KindAny:
		if secretKeyPattern.MatchString(a.Key) {
			return slog.String(a.Key, redacted)
		}
		switch x := v.Any().(type) {
		case nil:
			return a
		case error:
			return slog.String(a.Key, sanitize(x.Error(), maxErrorBytes))
		case []byte:
			// A byte slice is a payload by definition; its size is the
			// useful fact.
			return slog.String(a.Key, fmt.Sprintf("[%d bytes]", len(x)))
		default:
			return slog.String(a.Key, sanitize(fmt.Sprintf("%v", x), maxAttrBytes))
		}
	default:
		return a
	}
}

// sanitize masks secrets in s and then bounds it to limit bytes. Masking comes
// first so a cut can never leave the head of a secret that the patterns would
// have caught whole.
func sanitize(s string, limit int) string {
	if s == "" {
		return s
	}
	dropped := 0
	if len(s) > maxScanBytes {
		dropped = len(s) - maxScanBytes
		s = trimToRuneBoundary(s, maxScanBytes)
		dropped = dropped + (maxScanBytes - len(s))
	}
	for _, p := range secretValuePatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	if len(s) > limit {
		kept := trimToRuneBoundary(s, limit)
		dropped += len(s) - len(kept)
		s = kept
	}
	if dropped > 0 {
		s += fmt.Sprintf("…[truncated %d]", dropped)
	}
	return s
}

func trimToRuneBoundary(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.ToValidUTF8(s[:cut], "")
}
