// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
)

// The routing guide reaches every turn, whatever the project carries on disk:
// a project with no reliant.md, no synced config, or an old scaffolded one.
func TestGetSystemPrompts_CarriesLiveCapabilitiesGuide(t *testing.T) {
	activity := &CallLLMActivity{}
	for name, cfg := range map[string]*config.Config{
		"no config":    nil,
		"empty config": {},
		"with skills": {Skills: []config.StoredSkill{
			{SkillPath: "workflow-builder", Description: "Build a bot or agent that runs on a schedule"},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			prompt := activity.getSystemPrompts(nil, "/tmp/project", "", cfg, nil, nil, true)[0]
			assert.Contains(t, prompt, strings.TrimSpace(reliantCapabilities))
		})
	}
}

// Prompt-cache friendliness: the guide is identical bytes on every call and
// precedes the skills announcement, the part of the base prompt that moves
// whenever a skill is added.
func TestGetSystemPrompts_CapabilitiesGuideIsStableAndBeforeSkills(t *testing.T) {
	activity := &CallLLMActivity{}
	cfg := &config.Config{Skills: []config.StoredSkill{{SkillPath: "db", Description: "Database work"}}}
	first := activity.getSystemPrompts(nil, "/tmp/project", "", cfg, nil, nil, true)[0]
	second := activity.getSystemPrompts(nil, "/tmp/project", "", cfg, nil, nil, true)[0]
	require.Equal(t, first, second)

	guide := strings.Index(first, "<reliant_capabilities>")
	skills := strings.Index(first, "Available skills")
	require.GreaterOrEqual(t, guide, 0)
	require.GreaterOrEqual(t, skills, 0)
	assert.Less(t, guide, skills)
}

func TestReliantCapabilities_PinsRoutingFacts(t *testing.T) {
	g := strings.Join(strings.Fields(reliantCapabilities), " ")
	for _, want := range []string{
		"## What Reliant can do",
		"`workflow-builder` skill",
		"`.reliant/workflows/`",
		"`triggers:` block",
		"`http/request@1`",
		"even when it sounds like \"just a script\"",
		"`mcp__reliant-docs__search_reliant`",
		"If the user named another tool or platform, use theirs",
		"reliant forge skill load forge",
		"https://github.com/reliant-labs/forge",
		"cron-kind workloads and operators need",
		"Move to Forge",
		"reliant forge obs check <env> --wait --json",
		"Report \"not proven good\"",
		"reliant forge db query <env>",
		"reliant forge skill load observability/query",
	} {
		assert.Contains(t, g, want)
	}
}

// The failure this guide exists to prevent: an agent that reads forge as a
// backend framework and hands a landing page to some other host. Frontend-only
// work must be a reason to use forge in its own right — its own routing row and
// its own bullet, ahead of backends — with the concrete things it gets.
func TestReliantCapabilities_FrontendOnlyIsFirstClass(t *testing.T) {
	// Whitespace-collapsed, so re-wrapping the markdown does not break the pins.
	g := strings.Join(strings.Fields(reliantCapabilities), " ")

	for _, want := range []string{"landing page", "marketing", "docs site", "SPA", "PWA"} {
		assert.Contains(t, g, want)
	}
	// Its own routing row, pointing at the static-site skill.
	assert.Contains(t, g, "| A frontend that will be deployed, with or without a backend")
	assert.Contains(t, g, "reliant forge skill load deploy/static-site")

	// Default, not "consider".
	assert.Contains(t, g, "Default to it for anything that will be deployed — a frontend alone included")
	assert.Contains(t, g, "unless the user named another stack or host")

	// What a frontend-only project concretely gets.
	for _, want := range []string{
		"dev, staging and prod environments",
		"`reliant forge env up dev`",
		"promoted by digest",
		"static hosting with a CDN",
		"one static site per org is free; each environment's site counts",
		"custom domains",
		"`reliant forge component search`",
		"`frontend/design` skill",
		"`.forge/logs/<env>/`",
		"room to add an API",
	} {
		assert.Contains(t, g, want)
	}

	// Honest about PWAs: forge does not scaffold a service worker or manifest.
	assert.Contains(t, g, "the service worker and manifest are yours to write")

	// Frontend-only comes before backends, so it does not read as an afterthought.
	assert.Less(t, strings.Index(g, "**Frontend-only projects:**"), strings.Index(g, "**Backends:**"))
	assert.NotContains(t, g, "**Frontends:**", "the old footnote bullet is gone")
}

func readScaffoldedV1(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/scaffolded_reliant_md_v1.md")
	require.NoError(t, err)
	return string(b)
}

// A reliant.md scaffolded before the guide moved still carries a copy. Sent
// as-is, the model would read two versions — and the stale one, in the block
// it is told it "must adhere" to, frames a static site as a forge footnote.
func TestFormatStoredMemories_DropsStaleScaffoldedCapabilities(t *testing.T) {
	old := readScaffoldedV1(t)
	require.Contains(t, old, scaffoldedCapabilitiesHeading, "fixture must be the old scaffolded file")

	got := formatStoredMemories(&config.Config{ProjectMemoryMD: old})

	assert.NotContains(t, got, "What Reliant can do")
	assert.NotContains(t, got, "### forge")
	assert.NotContains(t, got, "Propose forge when someone starts building")
	// Everything around the section survives.
	for _, kept := range []string{"## Guidelines", "## Key Context", "## Code navigation", "## Available Skills"} {
		assert.Contains(t, got, kept)
	}
}

func TestWithoutScaffoldedCapabilities(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"no section is unchanged": {
			in:   "# Notes\n\n## Mine\n\n- keep\n",
			want: "# Notes\n\n## Mine\n\n- keep\n",
		},
		"section with subsection, user content after": {
			in:   "# P\n\n## What Reliant can do\n\nold\n\n### forge\n\nold forge\n\n## Ours\n\n- keep\n",
			want: "# P\n\n## Ours\n\n- keep\n",
		},
		"section at end of file": {
			in:   "# P\n\n## Mine\n\n- keep\n\n## What Reliant can do\n\nold\n",
			want: "# P\n\n## Mine\n\n- keep\n\n",
		},
		"CRLF line endings": {
			in:   "# P\r\n\r\n## What Reliant can do\r\n\r\nold\r\n## Ours\r\n",
			want: "# P\r\n\r\n## Ours\r\n",
		},
		"a differently named section is the user's": {
			in:   "## What Reliant can do for us\n\n- keep\n",
			want: "## What Reliant can do for us\n\n- keep\n",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, withoutScaffoldedCapabilities(tc.in))
		})
	}
}

// A memory that was nothing but the stale guide sends no empty Project Context.
func TestFormatStoredMemories_OnlyStaleGuideSendsNothing(t *testing.T) {
	assert.Equal(t, "", formatStoredMemories(&config.Config{ProjectMemoryMD: "## What Reliant can do\n\nold\n"}))
}

func TestFormatRepoMemoryMessages_DropsStaleScaffoldedCapabilities(t *testing.T) {
	msgs, omitted := formatRepoMemoryMessages(map[string]string{"web": readScaffoldedV1(t)}, repoMemoryBudgetChars(272_000))
	require.Empty(t, omitted)
	require.Len(t, msgs, 1)
	text := msgs[0].Content().Text
	assert.NotContains(t, text, "What Reliant can do")
	assert.Contains(t, text, "## Code navigation")
}
