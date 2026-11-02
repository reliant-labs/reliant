package generator

import "fmt"

// Harness identifies which AI tool's conventions to scaffold for at
// project creation time: the file(s) that point that tool at the project
// guide. The guide itself is always AGENTS.md; a harness adds the thin
// files its tool needs to find it. The default is "reliant".
type Harness string

// The harnesses forge can scaffold for.
const (
	HarnessReliant Harness = "reliant"
	HarnessClaude  Harness = "claude"
	HarnessCursor  Harness = "cursor"
	HarnessCopilot Harness = "copilot"
	HarnessCodex   Harness = "codex"
)

// ValidHarnesses lists all accepted --harness values. Returned as a
// getter to satisfy the no-exported-vars rule; callers should treat the
// returned slice as read-only (it is rebuilt on each call).
func ValidHarnesses() []Harness {
	return []Harness{
		HarnessReliant,
		HarnessClaude,
		HarnessCursor,
		HarnessCopilot,
		HarnessCodex,
	}
}

// Normalized resolves the zero value to the default harness. A
// ProjectGenerator built by a caller that never set Harness carries "",
// which every switch below already treats as reliant; Normalized makes
// that resolution explicit for the callers that SERIALIZE the value, so
// what lands in forge.yaml is a real harness name rather than a blank.
func (h Harness) Normalized() Harness {
	if h == "" {
		return HarnessReliant
	}
	return h
}

// MemoryFilePath returns the project-root-relative path of the harness's own
// top-level instruction file. For claude, cursor and copilot it is a thin
// pointer to AGENTS.md (see agentFiles); codex reads AGENTS.md itself, and
// reliant renders the guide in memory.
func (h Harness) MemoryFilePath() string {
	switch h {
	case HarnessClaude:
		return "CLAUDE.md"
	case HarnessCursor:
		return ".cursorrules"
	case HarnessCopilot:
		return ".github/copilot-instructions.md"
	case HarnessCodex:
		return "AGENTS.md"
	default: // reliant or unset
		return "reliant.md"
	}
}

// ParseHarness validates and normalises a user-supplied string.
func ParseHarness(s string) (Harness, error) {
	switch Harness(s) {
	case HarnessReliant, HarnessClaude, HarnessCursor, HarnessCopilot, HarnessCodex:
		return Harness(s), nil
	case "":
		return HarnessReliant, nil
	default:
		return "", fmt.Errorf("unknown harness %q; valid harnesses: reliant, claude, cursor, copilot, codex", s)
	}
}

// Detection markers: an environment variable each agent harness sets in the
// shell commands it runs. Only markers confirmed against the harness's own
// docs or source are listed; copilot sets none we could confirm, so it is
// never auto-detected.
//
//   - reliant: RELIANT_AGENT_SHELL=1, set on every agent shell by the
//     reliant daemon (internal/daemonpolicy.AgentShellEnv).
//   - claude:  CLAUDECODE=1 (code.claude.com/docs/en/env-vars).
//   - codex:   CODEX_THREAD_ID / CODEX_SESSION_ID, injected into every shell
//     command by codex-rs (core/src/exec_env.rs); not in OpenAI's public docs.
//   - cursor:  CURSOR_AGENT (cursor.com/docs/agent/tools/terminal).
//
// Reliant is checked first: its daemon passes its own environment through, so
// a daemon started from a Claude Code or Cursor terminal still carries those
// markers, and the innermost agent is the one running the command.
func harnessMarkers() []struct {
	harness Harness
	vars    []string
} {
	return []struct {
		harness Harness
		vars    []string
	}{
		{HarnessReliant, []string{"RELIANT_AGENT_SHELL"}},
		{HarnessClaude, []string{"CLAUDECODE"}},
		{HarnessCodex, []string{"CODEX_THREAD_ID", "CODEX_SESSION_ID"}},
		{HarnessCursor, []string{"CURSOR_AGENT"}},
	}
}

// DetectHarness reports which agent harness is running this process, from the
// markers above, and the variable that identified it. It returns ("", "")
// when none is set. getenv is os.Getenv in production.
func DetectHarness(getenv func(string) string) (Harness, string) {
	for _, m := range harnessMarkers() {
		for _, v := range m.vars {
			if val := getenv(v); val != "" && val != "0" {
				return m.harness, v
			}
		}
	}
	return "", ""
}

// ResolveHarness applies the precedence for `forge project new`: an explicit
// --harness value wins, then a detected harness, then reliant. detected is the
// marker variable when detection decided it, "" otherwise.
func ResolveHarness(flag string, getenv func(string) string) (h Harness, detected string, err error) {
	if flag != "" {
		h, err = ParseHarness(flag)
		return h, "", err
	}
	if h, marker := DetectHarness(getenv); h != "" {
		return h, marker, nil
	}
	return HarnessReliant, "", nil
}
