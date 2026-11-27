package cliutil

import (
	"io"
	"os"

	"golang.org/x/term"
)

// StdinIsTTY reports whether forge's standard input is connected to an
// interactive terminal.
//
// Forge is an LLM-first tool: the common driver is an agent or CI runner,
// neither of which has a TTY on stdin. Any code path that would otherwise
// block on an interactive prompt MUST gate on this helper first. When it
// returns false the command must NOT prompt — it either applies a safe
// default (for non-destructive actions) or fails fast with an actionable
// error that names the flag which avoids the prompt (for destructive ones).
//
// Centralising the check here keeps the TTY policy consistent across the
// CLI surface and gives us a single seam to override in tests if needed.
func StdinIsTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// IsTerminal reports whether w is an interactive terminal a status line can
// be redrawn on: an *os.File on a terminal, with TERM not "dumb" (a dumb
// terminal cannot return the cursor to the start of the line). Anything else
// — a pipe, a file, a buffer, an agent's captured output — is not.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || f == nil {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return term.IsTerminal(int(f.Fd())) //nolint:gosec // a file descriptor fits in int
}

// terminalWidth is w's width in cells, or 80 when it cannot be read.
func terminalWidth(w io.Writer) int {
	if f, ok := w.(*os.File); ok && f != nil {
		if width, _, err := term.GetSize(int(f.Fd())); err == nil && width > 0 { //nolint:gosec // a file descriptor fits in int
			return width
		}
	}
	return 80
}
