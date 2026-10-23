// Copyright (c) 2025 Reliant Labs
package tools

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestHeadAndTailBytesNeverSplitACharacter(t *testing.T) {
	// "…" is three bytes (e2 80 a6), "é" two, "🙂" four.
	s := "ab…cdé🙂xyz"
	for n := -1; n <= len(s)+1; n++ {
		head, tail := headBytes(s, n), tailBytes(s, n)
		assert.True(t, utf8.ValidString(head), "headBytes(%d) = %q", n, head)
		assert.True(t, utf8.ValidString(tail), "tailBytes(%d) = %q", n, tail)
		assert.LessOrEqual(t, len(head), max(n, 0))
		assert.LessOrEqual(t, len(tail), max(n, 0))
		assert.True(t, strings.HasPrefix(s, head))
		assert.True(t, strings.HasSuffix(s, tail))
	}
	// A cut inside "…" backs off to before it, and never sheds more than the
	// one character it landed in.
	assert.Equal(t, "ab", headBytes(s, 3))
	assert.Equal(t, "ab", headBytes(s, 4))
	assert.Equal(t, "ab…", headBytes(s, 5))
	assert.Equal(t, "xyz", tailBytes(s, 5))
	assert.Equal(t, "🙂xyz", tailBytes(s, 7))
}

// The prod failure (2026-10-21): a viewed line holding "…" was cut inside it
// and given "...", and Postgres rejected the tool result ("invalid byte
// sequence for encoding UTF8: 0xe2 0x80 0x2e").
func TestTruncatedToolOutputIsValidUTF8(t *testing.T) {
	line := strings.Repeat("x", MaxLineLength-2) + "…tail"
	got := truncateLongLines(line + "\n")
	assert.True(t, utf8.ValidString(got), "%q", got[len(got)-10:])
	assert.True(t, strings.HasSuffix(got, "x...\n"), "%q", got[len(got)-10:])

	big := strings.Repeat("…", 200_000)
	for _, tool := range []string{"view", "shell", "fetch", "grep"} {
		out := TruncateOutput(tool, big, true)
		assert.Less(t, len(out), len(big), tool)
		assert.True(t, utf8.ValidString(out), tool)
	}
	assert.True(t, utf8.ValidString(truncateOutputWithLimit(big, 1001)))
	assert.True(t, utf8.ValidString(truncateDetail(strings.Repeat("é", 100))))
	assert.True(t, utf8.ValidString(truncateDescription(strings.Repeat("é", 100), 50)))
}
