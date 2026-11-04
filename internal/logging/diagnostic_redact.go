package logging

import (
	"regexp"
	"strings"
)

// Diagnostic text is less structured than a slog attribute: it can be a
// configuration file, header dump, environment or multiline credential. Reuse
// the log value scrubber, but fail closed for credential-bearing formats it
// cannot safely delimit. In particular do not guess where YAML block scalars,
// folded values, escaped JSON, cookies or quoted shell values end. Losing a
// stream is preferable to publishing an inherited credential.
var diagnosticSensitiveField = regexp.MustCompile(`(?i)(?:^|[\s"'\\{,;-])(?:[a-z0-9_-]*[_-])?(?:token|secret|password|passwd|api[_-]?key|access[_-]?key|session[_-]?key|authorization|proxy[_-]?authorization|cookie|set-cookie|credentials?|private[_-]?key)\s*["'\\]*\s*[:=]`)

// RedactDiagnosticText is for already byte-bounded complete values, NEVER a
// prefix/tail cut at the storage boundary. RedactText is the shared log helper;
// the additional policy omits a complete value containing a recognized
// sensitive field. This is not a detector for arbitrary secret values.
func RedactDiagnosticText(s string) string {
	// Inspect ORIGINAL text, never text whose delimiters have already been
	// partially masked. Even a single line can contain escaped quotes, shell
	// concatenation or literal \\n. A marker is not evidence the whole value was
	// consumed. Omit the entire stream (not only a guessed value/line), since
	// multiline continuations can carry the rest of that credential.
	if diagnosticSensitiveField.MatchString(s) || strings.Contains(s, "-----BEGIN ") {
		return "[REDACTED: credential-bearing diagnostic omitted]"
	}
	return RedactText(s)
}
