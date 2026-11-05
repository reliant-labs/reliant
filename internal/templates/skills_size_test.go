// skills_size_test.go — structural guard for shipped skill paths.
package templates_test

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli"
)

// TestShippedSkillPathsAreWellFormed keeps a split skill reachable: every
// SKILL.md must sit under skills/forge/... or skills/general/..., because
// listForgeShippedSkills derives a skill's path from exactly those two
// roots and skips anything else. A subtopic dropped in the wrong place
// would vanish from the catalog rather than fail loudly.
func TestShippedSkillPathsAreWellFormed(t *testing.T) {
	t.Parallel()

	for rel, content := range shippedSkills(t) {
		if !strings.HasPrefix(rel, "forge/") && !strings.HasPrefix(rel, "general/") {
			t.Errorf("skills/%s: shipped skills must live under skills/forge/ or skills/general/ — anything else is skipped by the catalog", rel)
		}
		if filepath.Base(rel) != "SKILL.md" {
			t.Errorf("skills/%s: expected a SKILL.md leaf", rel)
		}
		if !strings.HasPrefix(content, "---\n") {
			t.Errorf("skills/%s: must open with YAML frontmatter at byte 0 (the skill loader requires it)", rel)
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Reliant's internal/llm/tools/{skill.go,skill_window.go} delivers at most
// MaxSkillBodySize bytes, INCLUDING the completion footer. Its navigation
// collapses to two list pointers when the full lists do not fit. Reserve room
// for BOTH pointers and the footer, not the catalog-sized expanded lists.
// Keep this contract local: importing the consumer would create a cycle.
const (
	maxSkillBytes         = 24_000
	skillNavigationBytes  = 224
	skillFooterBytes      = 96
	maxRenderedSkillBytes = maxSkillBytes - skillNavigationBytes - skillFooterBytes
	longestShippedCLIName = "reliant forge"
)

// skillSection is one "## " section of a rendered skill, heading included.
type skillSection struct {
	heading string
	bytes   int
}

// skillSections splits a rendered skill at its level-2 headings, outside
// fenced code blocks. Everything before the first heading (frontmatter and
// intro) is one section named "(preamble)".
func skillSections(content string) []skillSection {
	secs := []skillSection{{heading: "(preamble)"}}
	inFence := false
	for _, line := range strings.SplitAfter(content, "\n") {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
		}
		if !inFence && strings.HasPrefix(line, "## ") {
			secs = append(secs, skillSection{heading: strings.TrimSpace(line)})
		}
		secs[len(secs)-1].bytes += len(line)
	}
	return secs
}

// formatLargestSections names the n largest sections, so a budget failure
// says which section to move rather than leaving the author to measure.
func formatLargestSections(content string, n int) string {
	secs := skillSections(content)
	sort.SliceStable(secs, func(i, j int) bool { return secs[i].bytes > secs[j].bytes })
	if len(secs) > n {
		secs = secs[:n]
	}
	var b strings.Builder
	for _, s := range secs {
		fmt.Fprintf(&b, "  %6d bytes  %s\n", s.bytes, s.heading)
	}
	return b.String()
}

func TestSkillSectionsSplitAtHeadingsOutsideFences(t *testing.T) {
	t.Parallel()
	content := "---\nname: x\n---\nintro\n## A\naa\n```\n## not a heading\n```\n## B\nb\n"
	got := skillSections(content)
	want := []skillSection{
		{"(preamble)", len("---\nname: x\n---\nintro\n")},
		{"## A", len("## A\naa\n```\n## not a heading\n```\n")},
		{"## B", len("## B\nb\n")},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("skillSections = %v, want %v", got, want)
	}
	total := 0
	for _, s := range got {
		total += s.bytes
	}
	if total != len(content) {
		t.Fatalf("sections cover %d bytes, content is %d", total, len(content))
	}
	if out := formatLargestSections(content, 1); !strings.Contains(out, "## A") || strings.Contains(out, "## B") {
		t.Fatalf("formatLargestSections(1) = %q, want only the largest (## A)", out)
	}
}

func TestShippedSkillsFitLoadBudget(t *testing.T) {
	// The production renderer resolves user/project overrides. Neither belongs
	// in a guard for shipped content; no project root also avoids version skew.
	t.Setenv("HOME", t.TempDir())
	for rel := range shippedSkills(t) {
		skillPath := strings.TrimSuffix(rel, "/SKILL.md")
		skillPath = strings.TrimPrefix(skillPath, "general/")
		// The root start-here is "forge"; children omit the category prefix.
		if skillPath != "forge" {
			skillPath = strings.TrimPrefix(skillPath, "forge/")
		}
		t.Run(skillPath, func(t *testing.T) {
			content, err := cli.RenderSkillContentAt("", skillPath, cli.SkillAudienceAll, longestShippedCLIName)
			if err != nil {
				t.Fatal(err)
			}
			if over := len(content) - maxRenderedSkillBytes; over > 0 {
				t.Errorf("skills/%s: rendered as %q: %d bytes is %d OVER the %d budget (delivery cap %d minus navigation %d and footer %d).\n"+
					"Trimming %d bytes only buys until the next edit: move a separable section into a discoverable sub-skill "+
					"and leave a one-line pointer. Largest sections:\n%s",
					rel, longestShippedCLIName, len(content), over, maxRenderedSkillBytes, maxSkillBytes, skillNavigationBytes, skillFooterBytes,
					over, formatLargestSections(string(content), 5))
			}
			t.Logf("%d rendered bytes; %d bytes of body margin", len(content), maxRenderedSkillBytes-len(content))

			// Pin the reserve to Reliant's complete-delivery formats. Five-digit
			// catalog counts are conservative; the current catalog is hundreds.
			path := "forge/" + skillPath
			subs := fmt.Sprintf("\n\n---\n%d sub-skills under this one: skill(action=%q, path=%q)\n", 99_999, "list", path)
			parent := path[:strings.LastIndex(path, "/")]
			related := fmt.Sprintf("\n---\n%d other skills beside this one: skill(action=%q, path=%q)\n", 99_999, "list", parent)
			footer := fmt.Sprintf("\n\n[skill %s — %d bytes, complete]", path, maxSkillBytes)
			if len(subs)+len(related) > skillNavigationBytes || len(footer) > skillFooterBytes {
				t.Fatalf("Reliant delivery overhead exceeds reserve: navigation=%d, footer=%d", len(subs)+len(related), len(footer))
			}
		})
	}
}
