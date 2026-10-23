package generator

import "fmt"

// Harness identifies which AI tool's conventions to scaffold for at
// project creation time: the top-level memory file's path, and (for
// harnesses that have a native skills concept) the on-disk directory
// where forge skills should be emitted. The default is "reliant".
type Harness string

// The harnesses forge can scaffold for. Each maps to a memory-file path
// and, where the harness has one, a skills directory.
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

// MemoryFilePath returns the project-root-relative path for the top-level
// memory file corresponding to the given harness.
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
