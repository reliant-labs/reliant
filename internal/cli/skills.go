package cli

import "strings"

// SkillAudience names the consumer of a skill emission. Combined with the
// per-skill frontmatter `emit:` field ([SkillEmit]) it decides which skills
// are written and how their bodies are rendered:
//
//	audience=All ("") — every skill, full body (no stripping). Default.
//	audience=General  — emit:general|both skills only; @forge-only blocks stripped.
//	audience=Forge    — emit:forge|both skills only; full body retained.
type SkillAudience string

const (
	// SkillAudienceAll disables filtering entirely. Use when bulk-exporting
	// the canonical catalog where the
	// reader can decide what to surface.
	SkillAudienceAll SkillAudience = ""
	// SkillAudienceGeneral targets consumers outside a forge project. The
	// renderer strips `<!-- @forge-only:start/end -->` blocks from emit:both
	// skills, and drops emit:forge skills entirely.
	SkillAudienceGeneral SkillAudience = "general"
	// SkillAudienceForge targets consumers inside a forge project. Full
	// body is preserved; emit:general skills are also included.
	SkillAudienceForge SkillAudience = "forge"
)

// RenderSkillForAudience returns the skill body filtered for the given
// audience. For SkillAudienceGeneral, `<!-- @forge-only:start -->` ...
// `<!-- @forge-only:end -->` blocks are removed (markers included). For
// every other audience the body is returned unchanged.
//
// Exported so out-of-process consumers (e.g. reliant) can apply the same
// filtering rule when serving a skill loaded via [LoadSkill]. Forge-side
// callers can use it directly.
func RenderSkillForAudience(body []byte, audience SkillAudience) []byte {
	if audience != SkillAudienceGeneral {
		return body
	}
	return stripForgeOnlyBlocks(body)
}

// stripForgeOnlyBlocks removes every `@forge-only` block from the body,
// inclusive of the marker lines, and collapses any runs of blank lines
// created by the removal down to a single blank line. An unterminated
// block drops content from the start marker to EOF — authors notice via
// missing content rather than silent passthrough.
func stripForgeOnlyBlocks(body []byte) []byte {
	lines := strings.Split(string(body), "\n")
	out := make([]string, 0, len(lines))
	inBlock := false
	for _, line := range lines {
		switch {
		case isForgeOnlyMarker(line, "start"):
			inBlock = true
		case isForgeOnlyMarker(line, "end"):
			inBlock = false
		case inBlock:
			// skip
		default:
			out = append(out, line)
		}
	}
	return []byte(collapseBlankRuns(strings.Join(out, "\n")))
}

// isForgeOnlyMarker reports whether a single line is the `<!-- @forge-only:start -->`
// or `<!-- @forge-only:end -->` HTML comment, tolerating surrounding
// whitespace both around the line and inside the comment.
func isForgeOnlyMarker(line, kind string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "<!--") || !strings.HasSuffix(trimmed, "-->") {
		return false
	}
	inner := strings.TrimSpace(trimmed[4 : len(trimmed)-3])
	return inner == "@forge-only:"+kind
}

// collapseBlankRuns rewrites consecutive blank lines as a single blank
// line — used after block stripping so removed sections don't leave a
// visible double-blank gap in the rendered output.
func collapseBlankRuns(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	prevBlank := false
	for _, line := range lines {
		blank := strings.TrimSpace(line) == ""
		if blank && prevBlank {
			continue
		}
		out = append(out, line)
		prevBlank = blank
	}
	return strings.Join(out, "\n")
}
