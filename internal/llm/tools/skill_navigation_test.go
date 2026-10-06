// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// The sub-skill and related-skill lists are reliant's navigation, not the
// skill's content. Their size scales with the CATALOG — every skill added to a
// namespace lengthens the list on every sibling — so a skill whose author kept
// it inside the delivery budget could be windowed by someone else adding an
// unrelated skill. That is how forge's start-here skill (21.4KB of body, under
// budget) stopped arriving whole: forge/ had grown ~35 siblings, and their
// 2.7KB list pushed it over.

const navTailMarker = "BODY TAIL MARKER: the last line the author wrote.\n"

// navBody returns a skill body of exactly n bytes ending in navTailMarker, so a
// test can tell a whole body from a windowed one by its last line.
func navBody(n int) string {
	head := "# Main\n\nHEAD MARKER\n\n"
	filler := strings.Repeat("Guidance the skill's author budgeted for.\n", n/40+1)
	return head + filler[:n-len(head)-len(navTailMarker)] + navTailMarker
}

// navCatalog builds pkg/main with the given body, `children` sub-skills under
// it and `siblings` other skills beside it under pkg/.
func navCatalog(body string, children, siblings int) []config.StoredSkill {
	skills := []config.StoredSkill{{SkillPath: "pkg/main", Name: "main", Description: "the skill under test", Body: body}}
	for i := range children {
		skills = append(skills, config.StoredSkill{
			SkillPath: fmt.Sprintf("pkg/main/child-%02d", i), Name: fmt.Sprintf("child-%02d", i),
			Description: "a child topic with a description long enough to cost real bytes", Body: "# Child",
		})
	}
	for i := range siblings {
		skills = append(skills, config.StoredSkill{
			SkillPath: fmt.Sprintf("pkg/sibling-%02d", i), Name: fmt.Sprintf("sibling-%02d", i),
			Description: "a sibling topic with a description long enough to cost real bytes", Body: "# Sibling",
		})
	}
	return skills
}

func loadMain(t *testing.T, skills []config.StoredSkill) string {
	t.Helper()
	env := &skillTestEnv{tool: &skillTool{skills: skills}}
	resp := env.execute(t, SkillParams{Action: "load", Path: "pkg/main"})
	require.False(t, resp.IsError, "load should succeed: %s", resp.Content)
	return resp.Content
}

// A body that fits the budget on its own must arrive whole, however many
// siblings the catalog has. The related list gives way to the one call that
// prints it.
func TestSkillTool_Load_SiblingListNeverEvictsTheBody(t *testing.T) {
	t.Parallel()
	skills := navCatalog(navBody(MaxSkillBodySize-2_000), 0, 60)

	got := loadMain(t, skills)

	assert.LessOrEqual(t, len(got), MaxSkillBodySize)
	assert.Contains(t, got, navTailMarker, "the body fits the budget alone, so it must arrive whole")
	assert.NotContains(t, got, "BYTES REMAIN", "an in-budget body must not be windowed")
	assert.Contains(t, got, "complete]", "the delivery must report itself complete")
	assert.NotContains(t, got, "pkg/sibling-59", "the full sibling list does not fit beside this body")
	assert.Contains(t, got, `skill(action="list", path="pkg")`,
		"the collapsed list must name the call that prints it, or the siblings become undiscoverable")
	assert.Contains(t, got, "60 other skills", "the collapsed list must say how much it stands for")
}

// The sub-skill list is the more specific navigation — it is how a reader finds
// this skill's own children — so it is kept in full for as long as collapsing
// the sibling list alone makes room.
func TestSkillTool_Load_SubSkillsOutliveTheSiblingList(t *testing.T) {
	t.Parallel()
	skills := navCatalog(navBody(MaxSkillBodySize-3_000), 10, 60)

	got := loadMain(t, skills)

	assert.LessOrEqual(t, len(got), MaxSkillBodySize)
	assert.Contains(t, got, navTailMarker)
	for i := range 10 {
		assert.Contains(t, got, fmt.Sprintf("pkg/main/child-%02d", i), "sub-skills fit once siblings collapse")
	}
	assert.NotContains(t, got, "pkg/sibling-59")
	assert.Contains(t, got, `skill(action="list", path="pkg")`)
}

// When even the sub-skill list will not fit beside the body, it collapses too.
func TestSkillTool_Load_SubSkillListCollapsesLast(t *testing.T) {
	t.Parallel()
	skills := navCatalog(navBody(MaxSkillBodySize-1_000), 40, 60)

	got := loadMain(t, skills)

	assert.LessOrEqual(t, len(got), MaxSkillBodySize)
	assert.Contains(t, got, navTailMarker)
	assert.NotContains(t, got, "pkg/main/child-39")
	assert.Contains(t, got, `skill(action="list", path="pkg/main")`)
	assert.Contains(t, got, "40 sub-skills")
}

// The invariant, swept across the boundary rather than sampled at one size:
// whether a skill arrives whole depends only on whether its BODY fits, never on
// how many skills sit around it.
func TestSkillTool_Load_BodyThatFitsAloneIsNeverWindowed(t *testing.T) {
	t.Parallel()
	seen := map[bool]int{}
	for n := MaxSkillBodySize - 600; n <= MaxSkillBodySize+40; n += 7 {
		body := navBody(n)
		bodyFits := fitsWhole("pkg/main", body)
		seen[bodyFits]++

		got := loadMain(t, navCatalog(body, 40, 60))

		whole := strings.Contains(got, navTailMarker)
		require.LessOrEqual(t, len(got), MaxSkillBodySize, "body %d bytes: over the budget", n)
		require.Equal(t, bodyFits, whole, "body %d bytes: fits alone=%v, arrived whole=%v", n, bodyFits, whole)
	}
	require.Positive(t, seen[true], "the sweep must cover bodies that fit")
	require.Positive(t, seen[false], "the sweep must cover bodies that do not")
}

// Navigation is only collapsed to make room. A skill with room to spare keeps
// both lists in full.
func TestSkillTool_Load_NavigationListedInFullWhenItFits(t *testing.T) {
	t.Parallel()
	skills := navCatalog(navBody(4_000), 10, 60)

	got := loadMain(t, skills)

	assert.Contains(t, got, "pkg/main/child-09")
	assert.Contains(t, got, "pkg/sibling-59")
	assert.NotContains(t, got, `skill(action="list"`)
}

// A body that is over budget on its own is a publishing defect, not a
// navigation problem: it is windowed exactly as before, with its full
// navigation reachable in the tail.
func TestSkillTool_Load_OversizeBodyStillWindowsWithFullNavigation(t *testing.T) {
	t.Parallel()
	skills := navCatalog(navBody(MaxSkillBodySize+2_000), 0, 60)
	env := &skillTestEnv{tool: &skillTool{skills: skills}}

	got := loadMain(t, skills)
	assert.Contains(t, got, "BYTES REMAIN")
	assert.NotContains(t, got, navTailMarker)

	tail := env.execute(t, SkillParams{Action: "load", Path: "pkg/main", Regex: "sibling-59"})
	assert.Contains(t, tail.Content, "pkg/sibling-59", "an oversize skill's full navigation stays reachable")
}

// The collapse must not split the two delivery paths: a preloaded skill and a
// hand-loaded one are the same bytes, through the ToolWrapper included.
func TestSkillNavigation_PreloadAndHandLoadAgree(t *testing.T) {
	t.Parallel()
	skills := navCatalog(navBody(MaxSkillBodySize-2_000), 10, 60)

	_, body, ok := LoadSkillForInjection(skills, "pkg/main")
	require.True(t, ok)
	preloaded, truncated := DeliverSkillContent("pkg/main", body)
	require.False(t, truncated, "the preload must not be reported oversized")

	handLoaded, err := NewSkillTool(skills).Run(
		&rctx.ToolContext{Context: context.Background()},
		ToolCall{ID: "c1", Name: ToolSkill, Input: `{"action":"load","path":"pkg/main"}`},
	)
	require.NoError(t, err)
	assert.Equal(t, preloaded, handLoaded.Content)
	assert.Contains(t, preloaded, navTailMarker)
}
