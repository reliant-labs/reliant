// skills_size_test.go — structural guard for shipped skill paths.
package templates_test

import (
	"fmt"
	"path/filepath"
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
			if len(content) > maxRenderedSkillBytes {
				t.Errorf("skills/%s: rendered as %q: %d bytes exceeds %d (delivery cap %d minus navigation %d and footer %d); split a separable topic into a discoverable sub-skill",
					rel, longestShippedCLIName, len(content), maxRenderedSkillBytes, maxSkillBytes, skillNavigationBytes, skillFooterBytes)
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
