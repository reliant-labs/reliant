package prpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
)

// Signature identifies what a fix is trying to clear: a hash of the failing
// checks and tests, or of the blocking findings, stable across the noise that
// changes between two runs of the same failure. It is `pr_pipeline.
// fix_signature` and `pipeline_attempts.signature`. Two signatures are the
// same failure exactly when they are equal.
type Signature string

// signatureVersion prefixes every signature. A change to what is hashed or
// how it is normalized changes it, so a signature never matches one computed
// by other rules.
const signatureVersion = "v1:"

// Short is the signature shortened for display.
func (s Signature) Short() string {
	const n = len(signatureVersion) + 12
	if len(s) <= n {
		return string(s)
	}
	return string(s[:n])
}

// CheckFailure is one failing check or test.
type CheckFailure struct {
	// Check names the check or gate stage ("test", "lint").
	Check string
	// Test names the failing test or target within it, if any
	// ("internal/foo TestBar/baz").
	Test string
	// Detail is the failure's own text: the assertion or error, not the
	// whole log. Noise in it is normalized away (see [FailureSignature]).
	Detail string
}

// FailureSignature is the signature of a set of failing checks and tests.
// Order and repeats do not matter. Each failure's text is normalized first,
// so these do not change it: timestamps and clock times, durations ("1.2s",
// "(0.03s)", "1m2s"), temp paths (/tmp, /var/folders, a Windows Temp dir, a
// Reliant worktree), hex addresses ("0xc000123abc"), long hex ids and
// UUIDs, goroutine numbers, loopback ports, process ids, line and column
// numbers ("foo.go:42:7", "line 42"), and whitespace. A different check, a
// different test, or a different message is a different signature.
func FailureSignature(failures []CheckFailure) Signature {
	entries := make([]string, 0, len(failures))
	for _, f := range failures {
		entries = append(entries, strings.Join([]string{
			normalizeText(f.Check), normalizeText(f.Test), normalizeText(f.Detail),
		}, "\x1f"))
	}
	return hashEntries("failures", entries)
}

// FindingsSignature is the signature of a review round's blocking findings
// (nits are left out): each by its stable id and its path, so a reworded
// body or a moved line is the same blocker. A finding with no id is known
// by its normalized body instead. Order and repeats do not matter.
func FindingsSignature(findings []Finding) Signature {
	var entries []string
	for _, f := range findings {
		if !f.isBlocker() {
			continue
		}
		key := "id:" + f.ID
		if f.ID == "" {
			key = "body:" + normalizeText(f.Body)
		}
		p := ""
		if f.Path != "" {
			p = normPath(f.Path)
		}
		entries = append(entries, key+"\x1f"+p)
	}
	return hashEntries("findings", entries)
}

func hashEntries(kind string, entries []string) Signature {
	slices.Sort(entries)
	entries = slices.Compact(entries)
	h := sha256.New()
	h.Write([]byte(kind))
	for _, e := range entries {
		h.Write([]byte{0})
		h.Write([]byte(e))
	}
	return Signature(signatureVersion + hex.EncodeToString(h.Sum(nil)))
}

// noiseRules rewrite what changes between two runs of the same failure. The
// order matters: paths go first, a date-time before a port or a file:line,
// those before a bare clock time, and that before a duration.
var noiseRules = []struct {
	re   *regexp.Regexp
	with string
}{
	// Temp and checkout paths, before anything inside them is rewritten.
	{regexp.MustCompile(`(?:/private)?/(?:tmp|var/folders)/\S*`), "<tmp>"},
	{regexp.MustCompile(`(?i)[a-z]:\\\S*\\temp\\\S*`), "<tmp>"},
	{regexp.MustCompile(`\S*/\.reliant/(?:worktrees|scratch)/[^/\s]+/`), "<checkout>/"},
	// Dates and date-times.
	{regexp.MustCompile(`\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?)?`), "<time>"},
	// UUIDs, then hex: addresses, and long runs (shas, ids).
	{regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`), "<uuid>"},
	{regexp.MustCompile(`\b0x[0-9a-fA-F]+\b`), "0x?"},
	{regexp.MustCompile(`\b[0-9a-f]{12,}\b`), "<hex>"},
	// Loopback ports, before line numbers would read "127.0.0.1:8080" as
	// a file ".1" and its line.
	{regexp.MustCompile(`(127\.0\.0\.1|localhost|0\.0\.0\.0|\[::1?\]):\d+`), "$1:<port>"},
	// Line (and column) numbers.
	{regexp.MustCompile(`(\.[A-Za-z0-9]{1,10}):\d+(?::\d+)?`), "$1:N"},
	{regexp.MustCompile(`(?i)\b(line|col|column)\s+\d+`), "$1 N"},
	// Goroutines and process ids.
	{regexp.MustCompile(`\bgoroutine \d+`), "goroutine N"},
	{regexp.MustCompile(`(?i)\bpid[ =:]\s*\d+`), "pid N"},
	// Clock times, then durations ("1.2s", "350ms", "1m2.5s", "(0.03s)").
	{regexp.MustCompile(`\b\d{1,2}:\d{2}:\d{2}(?:[.,]\d+)?\b`), "<time>"},
	{regexp.MustCompile(`\b(?:\d+(?:\.\d+)?(?:ns|us|µs|ms|h|m|s))+\b`), "<dur>"},
}

var spaceRun = regexp.MustCompile(`[ \t]+`)

// normalizeText is s with its noise rewritten (noiseRules), each line's
// whitespace collapsed and trimmed, and blank lines dropped.
func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	for _, r := range noiseRules {
		s = r.re.ReplaceAllString(s, r.with)
	}
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(spaceRun.ReplaceAllString(l, " "))
		if l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}
