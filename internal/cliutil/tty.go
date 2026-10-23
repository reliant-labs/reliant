package cliutil

import (
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
