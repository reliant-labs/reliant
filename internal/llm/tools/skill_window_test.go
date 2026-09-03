// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -----------------------------------------------------------------------------
// Fixtures
// -----------------------------------------------------------------------------

// oversizeSkillBody builds a markdown skill that comfortably exceeds
// MaxSkillBodySize and carries recognisable `## ` sections, so a test can assert
// both that the tail is unreachable by default and that it becomes reachable
// through a window or a section fetch.
//
// The size is derived FROM the budget rather than hard-coded: a literal byte
// count silently stops exercising the oversize path the moment the ceiling
// moves, and the test then passes while proving nothing.
func oversizeSkillBody() string {
	var b strings.Builder
	b.WriteString("# DB\n\nHEAD MARKER: guidance for the database layer.\n\n")

	filler := strings.Repeat("Every migration is the single source of truth for the schema.\n", 40)

	b.WriteString("## Overview\n\n")
	b.WriteString(filler)
	b.WriteString("\n")

	// Pad until we are well past the budget, then append the two sections the
	// assertions care about so they are guaranteed to land in the dropped tail.
	for b.Len() < MaxSkillBodySize+4_000 {
		fmt.Fprintf(&b, "## Filler Section %d\n\n%s\n", b.Len(), filler)
	}

	b.WriteString("## FKs And Diamonds\n\nDIAMOND MARKER: resolve diamond dependencies by ordering the FK writes.\n\n")
	b.WriteString("## Seeding\n\nTAIL MARKER: seed data belongs in db/seeds.\n")
	return b.String()
}

func newOversizeSkillEnv() *skillTestEnv {
	return &skillTestEnv{tool: &skillTool{skills: []config.StoredSkill{
		{
			SkillPath:   "db",
			Name:        "db",
			Description: "Database work",
			Scope:       "project",
			Body:        oversizeSkillBody(),
		},
	}}}
}

// -----------------------------------------------------------------------------
// Requirement 1: always report total size and whether more remains
// -----------------------------------------------------------------------------

// An oversize load must state the skill's true total size and say that content
// remains. Without this an agent cannot tell a complete result from a cut one,
// which is what drives the observed "load then sed -n" recovery round-trips.
func TestSkillTool_Load_Oversize_ReportsTotalSizeAndHasMore(t *testing.T) {
	t.Parallel()
	env := newOversizeSkillEnv()
	full := oversizeSkillBody()

	resp := env.execute(t, SkillParams{Action: "load", Path: "db"})
	require.False(t, resp.IsError, "load should succeed")

	// Assert on the tail only. The body is ~30KB of filler, and a failing
	// Contains on the whole thing buries the reason in a screenful of fixture.
	tail := lastBytes(resp.Content, 700)

	if !strings.Contains(resp.Content, fmt.Sprintf("%d", len(full))) {
		t.Errorf("result must state the skill's true total byte size (%d); tail was:\n%s", len(full), tail)
	}
	if !regexp.MustCompile(`(?i)remain`).MatchString(resp.Content) {
		t.Errorf("result must say that more content remains; tail was:\n%s", tail)
	}
}

// lastBytes returns the final n bytes of s, for compact failure messages.
func lastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

var continueOffsetRe = regexp.MustCompile(`offset=(\d+)\)`)

// extractContinueOffset reads the offset out of the continuation call the tool
// printed, or returns -1 when the result reported nothing remaining.
//
// Tests follow the tool's OWN reported offset rather than computing one. That
// is the contract an agent depends on: if the printed call were wrong, an agent
// following it would loop or skip content, and a test that recomputed the
// offset itself would never notice.
func extractContinueOffset(t *testing.T, resp ToolResponse) int {
	t.Helper()
	m := continueOffsetRe.FindStringSubmatch(resp.Content)
	if m == nil {
		return -1
	}
	var n int
	_, err := fmt.Sscanf(m[1], "%d", &n)
	require.NoError(t, err, "continuation offset must parse")
	return n
}

// A skill that fits must ALSO report its size. This is the case that removes
// defensive paging: the observed agent paged a 15,231-byte skill that was never
// truncated, purely because the result did not say it was complete.
func TestSkillTool_Load_UnderBudget_ReportsCompleteDelivery(t *testing.T) {
	t.Parallel()
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "load", Path: "go/defer"})
	require.False(t, resp.IsError, "load should succeed: %s", resp.Content)

	assert.Regexp(t, `(?i)complete`, resp.Content,
		"a fully delivered skill must say so, so the agent does not page defensively")
}

// -----------------------------------------------------------------------------
// Requirement 2: the tail must be reachable, and the notice must say how
// -----------------------------------------------------------------------------

// The truncation notice is the agent's only instruction for recovering the
// tail. Telling it to open SKILL.md on disk is not actionable when skills are
// embedded in a binary and synced through the config pipeline.
func TestSkillTruncationNotice_NamesToolParametersNotDisk(t *testing.T) {
	t.Parallel()
	notice := skillTruncationNotice(50_000, 26_000)

	assert.NotContains(t, notice, "SKILL.md",
		"notice must not send the agent to a file it cannot read")
	assert.NotContains(t, notice, ".claude/skills/",
		"notice must not send the agent to a directory it cannot read")
	assert.Contains(t, notice, "offset",
		"notice must name the parameter that fetches the next window")
}

// The whole point: content dropped from the default load must be reachable
// through the tool. This walks the exact recovery an agent would perform.
func TestSkillTool_Load_Oversize_TailReachableByOffset(t *testing.T) {
	t.Parallel()
	env := newOversizeSkillEnv()

	first := env.execute(t, SkillParams{Action: "load", Path: "db"})
	require.False(t, first.IsError, "first load should succeed")
	require.NotContains(t, first.Content, "TAIL MARKER",
		"fixture precondition: the tail must not fit in the default load")

	// Walk windows until the tail arrives, following the offsets the tool
	// reports — never computing them independently. If the reported offsets
	// were wrong, this loop would not terminate on the marker.
	offset, found := 0, false
	for range 10 {
		next := extractContinueOffset(t, env.execute(t, SkillParams{Action: "load", Path: "db", Offset: offset}))
		if next < 0 {
			break // this window reported nothing remaining
		}
		offset = next
		resp := env.execute(t, SkillParams{Action: "load", Path: "db", Offset: offset})
		require.False(t, resp.IsError, "windowed load should succeed: %s", resp.Content)
		if strings.Contains(resp.Content, "TAIL MARKER") {
			found = true
			break
		}
	}
	assert.True(t, found, "the dropped tail must be reachable by following the reported offsets")
}

// A window must not exceed the delivery budget — including its own footer.
// Capping in the tool rather than leaving it to the wrapper is what keeps the
// footer (the part naming the next call) from being cut off.
func TestSkillTool_Load_Window_RespectsBudgetIncludingFooter(t *testing.T) {
	t.Parallel()
	env := newOversizeSkillEnv()

	for _, offset := range []int{0, 1, 12_000, 23_999} {
		resp := env.execute(t, SkillParams{Action: "load", Path: "db", Offset: offset})
		require.False(t, resp.IsError, "offset %d should succeed", offset)
		assert.LessOrEqual(t, len(resp.Content), MaxSkillBodySize,
			"offset %d delivered %d bytes, over the %d budget", offset, len(resp.Content), MaxSkillBodySize)
		assert.Contains(t, resp.Content, "SKILL WINDOW",
			"offset %d must keep its delivery footer", offset)
	}
}

// Section-aware retrieval: an agent that saw the outline can fetch the section
// it needs in one targeted call rather than guessing a byte range.
func TestSkillTool_Load_Section_FetchesNamedSectionOnly(t *testing.T) {
	t.Parallel()
	env := newOversizeSkillEnv()

	resp := env.execute(t, SkillParams{Action: "load", Path: "db", Section: "FKs And Diamonds"})
	require.False(t, resp.IsError, "section load should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "DIAMOND MARKER", "the requested section must be delivered")
	assert.NotContains(t, resp.Content, "TAIL MARKER", "a later section must not be included")
	assert.NotContains(t, resp.Content, "HEAD MARKER", "an earlier section must not be included")
}

// The outline is what makes section= usable: it must ship with every load, so
// the agent learns the section names from the same call that told it the skill
// was too big.
func TestSkillTool_Load_Oversize_ListsSectionsForFollowUp(t *testing.T) {
	t.Parallel()
	env := newOversizeSkillEnv()

	resp := env.execute(t, SkillParams{Action: "load", Path: "db"})
	require.False(t, resp.IsError, "load should succeed")

	tail := lastBytes(resp.Content, 1500)
	assert.Contains(t, tail, "FKs And Diamonds",
		"the outline must name sections that live in the DROPPED tail — those are\n"+
			"exactly the ones the agent cannot otherwise discover; got tail:\n"+tail)
}

// Section names are matched case-insensitively and by unique substring, so an
// agent that half-remembers a heading still lands on it.
func TestSkillTool_Load_Section_ResolvesLoosely(t *testing.T) {
	t.Parallel()
	env := newOversizeSkillEnv()

	resp := env.execute(t, SkillParams{Action: "load", Path: "db", Section: "diamonds"})
	require.False(t, resp.IsError, "loose section match should succeed: %s", resp.Content)
	assert.Contains(t, resp.Content, "DIAMOND MARKER")
}

// A miss must name the real sections rather than dead-ending.
func TestSkillTool_Load_Section_UnknownNamesRealSections(t *testing.T) {
	t.Parallel()
	env := newOversizeSkillEnv()

	resp := env.execute(t, SkillParams{Action: "load", Path: "db", Section: "no-such-section"})
	require.True(t, resp.IsError, "an unknown section should be an error")
	assert.Contains(t, resp.Content, "Seeding", "the error must name sections that do exist")
}

// The filter path: locate content in a large skill without paging to it.
func TestSkillTool_Load_Regex_DeliversMatchingLinesWithNumbers(t *testing.T) {
	t.Parallel()
	env := newOversizeSkillEnv()

	resp := env.execute(t, SkillParams{Action: "load", Path: "db", Regex: "DIAMOND MARKER"})
	require.False(t, resp.IsError, "regex load should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "DIAMOND MARKER",
		"a match living in the dropped tail must be reachable by filter alone")
	assert.Regexp(t, `> Line \d+:`, resp.Content,
		"filtered lines must be numbered so a hit converts into a targeted read")
	assert.NotContains(t, resp.Content, "HEAD MARKER", "non-matching lines must be filtered out")
}

func TestSkillTool_Load_Regex_ContextLinesAreIncluded(t *testing.T) {
	t.Parallel()
	env := newOversizeSkillEnv()

	resp := env.execute(t, SkillParams{
		Action: "load", Path: "db", Regex: "DIAMOND MARKER", RegexContextBefore: 2,
	})
	require.False(t, resp.IsError, "regex with context should succeed: %s", resp.Content)

	assert.Contains(t, resp.Content, "## FKs And Diamonds",
		"context_before must include the heading above the match")
}

func TestSkillTool_Load_Regex_CaseInsensitive(t *testing.T) {
	t.Parallel()
	env := newOversizeSkillEnv()

	sensitive := env.execute(t, SkillParams{Action: "load", Path: "db", Regex: "diamond marker"})
	assert.NotContains(t, sensitive.Content, "DIAMOND MARKER",
		"a case-sensitive filter must not match")

	insensitive := env.execute(t, SkillParams{
		Action: "load", Path: "db", Regex: "diamond marker", RegexCaseInsensitive: true,
	})
	require.False(t, insensitive.IsError, "case-insensitive filter should succeed")
	assert.Contains(t, insensitive.Content, "DIAMOND MARKER")
}

// A filter with no hits must still tell the agent what the skill contains.
func TestSkillTool_Load_Regex_NoMatchesOffersSections(t *testing.T) {
	t.Parallel()
	env := newOversizeSkillEnv()

	resp := env.execute(t, SkillParams{Action: "load", Path: "db", Regex: "zzz-not-present"})
	require.False(t, resp.IsError, "an empty filter result is not an error")

	assert.Contains(t, resp.Content, "No lines")
	assert.Contains(t, resp.Content, "Seeding", "a dead end must offer the section outline as a next move")
}

// Headings inside fenced code blocks are shell comments, not sections.
func TestSkillSections_IgnoresFencedCodeBlocks(t *testing.T) {
	t.Parallel()
	body := "# Title\n\n```bash\n# not a heading\nls\n```\n\n## Real Section\n\nbody\n"

	var titles []string
	for _, s := range skillSections(body) {
		titles = append(titles, s.Title)
	}

	assert.Equal(t, []string{"Title", "Real Section"}, titles,
		"a '# comment' inside a code fence must not register as a section")
}

func TestSkillTool_Load_RejectsContradictorySelectors(t *testing.T) {
	t.Parallel()
	env := newOversizeSkillEnv()

	resp := env.execute(t, SkillParams{Action: "load", Path: "db", Section: "Seeding", Regex: "x"})
	require.True(t, resp.IsError, "section + regex is not a meaningful combination")
	assert.Contains(t, resp.Content, "pick one selector")
}

// -----------------------------------------------------------------------------
// Requirement 4: a normal load is unchanged
// -----------------------------------------------------------------------------

// The skill content itself — body, sub-skill list, related skills, suggested
// tools — must be byte-identical to today. The delivery footer is additive and
// appended after it.
func TestSkillTool_Load_NormalSkill_ContentPrefixUnchanged(t *testing.T) {
	t.Parallel()
	env := newSkillTestEnv(t)

	resp := env.execute(t, SkillParams{Action: "load", Path: "go"})
	require.False(t, resp.IsError, "load should succeed: %s", resp.Content)

	// This is the exact text today's loadSkill produces for "go".
	want := "# Go\n\nGuidance for writing idiomatic Go code." +
		"\n\n---\nSub-skills available (use skill tool with action=list or action=load):\n" +
		"- go/defer: Defer patterns for resource cleanup in Go\n" +
		"- go/error-handling: Error handling conventions in Go (has sub-skills)\n"

	assert.True(t, strings.HasPrefix(resp.Content, want),
		"today's rendered skill content must survive verbatim as the prefix of the result;\ngot:\n%q", resp.Content)
}

// The preload path must stay byte-identical to today: call_llm seeds these
// bytes into the cached prompt prefix, where a per-call delivery footer would
// be both meaningless and permanently resident.
func TestLoadSkillForInjection_ExcludesDeliveryFooter(t *testing.T) {
	t.Parallel()
	env := newSkillTestEnv(t)

	_, body, ok := LoadSkillForInjection(env.tool.skills, "go/defer")
	require.True(t, ok, "injection should resolve go/defer")

	assert.Contains(t, body, "# Defer", "preloaded body must carry the skill content")
	assert.NotContains(t, body, "SKILL DELIVERY",
		"preloaded body must not carry a per-call delivery footer: call_llm seeds "+
			"these bytes into the cached prompt prefix, where offset advice is both "+
			"meaningless and permanently resident")
}
