// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"regexp"
)

// Secret redaction for workflow logs.
//
// Background: workflow node outputs and tool results frequently carry the raw
// contents of files an agent has read (e.g. a `.env` read via a file-read tool).
// When those maps were logged verbatim as structured-log values, any secret in
// the file content (API keys, webhook signing secrets, etc.) was written to the
// logs in plaintext. The helpers here scrub secret-shaped substrings before such
// values reach a log sink, so we keep useful structure/keys/timings in logs
// without leaking the secret bytes themselves.
//
// IMPORTANT: redaction is applied only to values destined for LOGS. It never
// touches the data the agent sees or the values stored in node outputs / CEL
// context — callers pass a redacted *copy* to the logger.

const redactedPlaceholder = "[REDACTED]"

// secretPatterns matches common secret token shapes. Anchored to value bodies
// (not assignments) so they fire regardless of surrounding context — a leaked
// `sk_live_...` is a secret whether it appears as `KEY=sk_live_x` or bare in a
// JSON tool result.
var secretPatterns = []*regexp.Regexp{
	// Provider keys with well-known prefixes: Stripe (sk_live_/sk_test_/whsec_),
	// Supabase publishable (sb_publishable_), Resend (re_...), generic sk- keys.
	regexp.MustCompile(`\bsk_live_[A-Za-z0-9]+`),
	regexp.MustCompile(`\bsk_test_[A-Za-z0-9]+`),
	regexp.MustCompile(`\bwhsec_[A-Za-z0-9]+`),
	regexp.MustCompile(`\bsb_publishable_[A-Za-z0-9_-]+`),
	regexp.MustCompile(`\bsb_secret_[A-Za-z0-9_-]+`),
	regexp.MustCompile(`\bre_[A-Za-z0-9_]{12,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`\brk_(?:live|test)_[A-Za-z0-9]+`),
	regexp.MustCompile(`\bpk_(?:live|test)_[A-Za-z0-9]+`),
	// GitHub / GitLab tokens.
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{16,}`),
	// AWS access key ids.
	regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
	// Slack tokens.
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`),
	// JWTs / bearer-looking tokens (three base64url segments).
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
}

// assignmentPattern matches `KEY=value` / `key: value` style assignments whose
// key name signals a secret (password, api_key, *_secret, token, ...). The value
// is redacted whatever its shape, since env-style secrets often have no
// recognizable prefix (e.g. a random base64 DB password).
//
// The signal word must be a delimited segment of the key (start/end of key or
// flanked by `_`/`-`), so innocuous keys that merely *contain* a signal word as
// a substring — e.g. `tokens`, `total_tokens`, `token_count`, `authority`,
// `author` — do NOT trigger redaction of their values.
var secretKeyWord = `(?:password|passwd|secret|api[_-]?key|access[_-]?key|private[_-]?key|token|auth|credential|bearer)`
var assignmentPattern = regexp.MustCompile(
	`(?i)\b([A-Za-z0-9_-]*(?:^|_|-|\b)` + secretKeyWord + `(?:$|_|-|\b)[A-Za-z0-9_-]*)\s*[:=]\s*("?)([^\s"',]+)("?)`,
)

// numericValue matches a plain number (optionally signed / decimal). A secret is
// never a bare number, but token COUNTS, limits, temperatures, and timings are —
// so numeric assignment values are left intact to preserve useful debug logging
// even when the key name contains a signal word (e.g. `token_count=42`).
var numericValue = regexp.MustCompile(`^[+-]?\d+(?:\.\d+)?$`)

// redactString scrubs secret-shaped substrings from s, returning a copy safe to
// log. Non-secret text is preserved so the log line stays useful.
func redactString(s string) string {
	if s == "" {
		return s
	}
	// Redact key=value / key: value assignments first (preserves the key name).
	// Skip numeric values so token counts / limits / timings survive.
	out := assignmentPattern.ReplaceAllStringFunc(s, func(match string) string {
		groups := assignmentPattern.FindStringSubmatch(match)
		if groups == nil {
			return match
		}
		key, value := groups[1], groups[3]
		if numericValue.MatchString(value) {
			return match
		}
		return key + "=" + redactedPlaceholder
	})
	// Then redact bare secret-shaped tokens anywhere in the text.
	for _, re := range secretPatterns {
		out = re.ReplaceAllString(out, redactedPlaceholder)
	}
	return out
}

// redactValue returns a deep copy of v with all string leaves run through
// redactString. Maps and slices are walked recursively; the original value is
// never mutated. Pass the result (not v) to a logger when v may carry tool
// results or node-output content.
func redactValue(v interface{}) interface{} {
	switch val := v.(type) {
	case string:
		return redactString(val)
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, item := range val {
			out[k] = redactValue(item)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, item := range val {
			out[i] = redactValue(item)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(val))
		for k, item := range val {
			out[k] = redactString(item)
		}
		return out
	case []string:
		out := make([]string, len(val))
		for i, item := range val {
			out[i] = redactString(item)
		}
		return out
	default:
		// Numbers, bools, nil, protos, etc. carry no free-text secrets we can
		// scrub; return as-is.
		return v
	}
}
