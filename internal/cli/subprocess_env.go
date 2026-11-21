package cli

import (
	"os"
	"os/exec"
	"strings"
)

// logEnvVars are environment variables that make a third-party CLI emit
// human-oriented diagnostics on STDOUT, corrupting machine-readable output.
//
// LOG_LEVEL is the one that bites in practice: k3d reads it, and `LOG_LEVEL=debug`
// is an entirely reasonable thing for the app under development to export. k3d
// then writes ANSI-coloured DEBU lines to stdout, ahead of the JSON document,
// and forge's `k3d cluster list -o json` fails to parse with
//
//	invalid character '\x1b' looking for beginning of value
//
// which names an escape byte and nothing a developer could act on. The env var
// belongs to the project, not to forge, so forge must not require it to be
// unset — it scrubs the variable for its OWN machine-readable subprocess calls
// and leaves the developer's environment alone.
var logEnvVars = []string{
	"LOG_LEVEL",
	"K3D_LOG_LEVEL",
	"LOG_FORMAT",
	"DEBUG",
	"VERBOSE",
	"TRACE",
}

// scrubSubprocessLogEnv seeds cmd.Env from the process environment with the
// log-verbosity variables removed, so a tool forge parses cannot be told to
// interleave diagnostics into its structured output.
//
// Use this ONLY for calls whose stdout forge parses (JSON/columns). A
// subprocess whose output is streamed to the user should inherit the ambient
// environment untouched — the developer asked for that verbosity.
//
// Seeding from os.Environ() (rather than setting a bare Env) keeps PATH, HOME
// and DOCKER_* intact, which the tools need in order to run at all.
func scrubSubprocessLogEnv(cmd *exec.Cmd) {
	base := cmd.Env
	if base == nil {
		base = os.Environ()
	}
	filtered := make([]string, 0, len(base))
	for _, kv := range base {
		name, _, found := strings.Cut(kv, "=")
		if found && isLogEnvVar(name) {
			continue
		}
		filtered = append(filtered, kv)
	}
	cmd.Env = filtered
}

// firstLine returns s's first line, trimmed and bounded, for use in an error
// that quotes unexpected subprocess output. ANSI escapes are stripped so the
// message stays readable in a log file.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	line = strings.TrimSpace(ansiEscape.ReplaceAllString(line, ""))
	if len(line) > 120 {
		line = line[:120] + "…"
	}
	if line == "" {
		return "(blank)"
	}
	return line
}

// isLogEnvVar reports whether name is a log-verbosity variable. Compared
// case-insensitively: these are conventionally upper-case, but a lower-case
// spelling reaches the subprocess just the same.
func isLogEnvVar(name string) bool {
	for _, candidate := range logEnvVars {
		if strings.EqualFold(name, candidate) {
			return true
		}
	}
	return false
}
