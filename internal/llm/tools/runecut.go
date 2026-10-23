// Copyright (c) 2025 Reliant Labs
package tools

import "unicode/utf8"

// The tools cap their output by BYTES — a budget is bytes on the wire and in
// the database — but a byte offset can land inside a multi-byte character.
// Cutting there left half a rune behind, and Postgres refuses the result
// outright: "invalid byte sequence for encoding UTF8: 0xe2 0x80 0x2e" is the
// first two bytes of "…" followed by the "..." a truncation appended, and it
// failed a tool result's write in prod (2026-10-19, 10-21). Every byte cut in
// this package goes through headBytes / tailBytes.

// headBytes is the longest prefix of s that is at most n bytes and does not
// end inside a character.
func headBytes(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n <= 0 {
		return ""
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// tailBytes is the longest suffix of s that is at most n bytes and does not
// begin inside a character.
func tailBytes(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n <= 0 {
		return ""
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
