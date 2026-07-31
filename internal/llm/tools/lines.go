// Copyright (c) 2025 Reliant Labs
package tools

import "strings"

// splitLines splits file content into its lines and reports whether the content
// ended with a newline. joinLines(splitLines(s)) == s for every s.
//
// Plain strings.Split cannot be used on byte-exact file content: "a\nb\n" is a
// two-line file, but Split yields three elements with a phantom "" at the end.
// That phantom is not harmless — the line tools report it as a real line in
// their bounds errors, let an agent target it by number, and drop the file's
// final newline when the edit lands on it. Carrying the terminator as a flag
// instead keeps line numbering honest and makes the round-trip exact.
func splitLines(content string) (lines []string, terminated bool) {
	if content == "" {
		return nil, false
	}
	if trimmed, ok := strings.CutSuffix(content, "\n"); ok {
		return strings.Split(trimmed, "\n"), true
	}
	return strings.Split(content, "\n"), false
}

// joinLines is the inverse of splitLines: it reassembles lines and restores the
// trailing newline only if the original content had one. A file that ended
// without a newline must not gain one.
func joinLines(lines []string, terminated bool) string {
	// No lines left means an empty file, not a lone newline. Reachable when an
	// edit deletes every line; splitLines itself never returns this pair.
	if len(lines) == 0 {
		return ""
	}
	s := strings.Join(lines, "\n")
	if terminated {
		s += "\n"
	}
	return s
}
