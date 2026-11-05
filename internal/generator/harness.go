package generator

import (
	"fmt"

	"github.com/reliant-labs/forge/internal/buildinfo"
)

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

// DetectHarness reports which agent harness is running this process, and the
// variable that identified it. The markers live in buildinfo.DetectAgent,
// shared with the control-plane client's X-Forge-Client header; an agent forge
// detects but has no scaffold for (gemini) is reported as ("", "") here, so
// it falls through to the default like an undetected one. getenv is os.Getenv
// in production.
func DetectHarness(getenv func(string) string) (Harness, string) {
	agent, marker := buildinfo.DetectAgent(getenv)
	for _, h := range ValidHarnesses() {
		if string(h) == agent {
			return h, marker
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
